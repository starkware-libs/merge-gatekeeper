package status

import (
	"context"
	"sort"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: filterStaleCheckRuns picks ONE "latest non-cancelled run" per
// workflow ID and marks every other suite of that workflow as superseded.
// But two runs of the same workflow at the same SHA are not necessarily a
// re-trigger pair: a workflow with `on: [push, pull_request]` runs TWICE for
// every push to a PR branch — one run per event — and both runs are
// legitimate, concurrently-current CI. They differ materially (the
// pull_request run executes the merge ref, the push run the branch head), so
// one must not silently supersede the other.
//
// The workflow-runs listing carries the Event field that distinguishes the
// two, but the code never reads it. Consequences (both reproduced below):
//
//  1. Failure masked: the push-event run FAILED a job, the pull_request-event
//     run (created a second later, higher run number) passed it. The push
//     run's suite is marked superseded, the superseding run is completed, so
//     the failure is DROPPED. The gatekeeper reports success — false green.
//
//  2. In-progress run masked: the push-event run is still executing while the
//     pull_request-event run completed. The running job is dropped the same
//     way, and the gatekeeper goes green while CI is still running.
//
// Expected correct behavior: supersede-detection must be scoped to runs of
// the same (workflow, event) — only a re-trigger of the same event type can
// supersede a run.
func Test_Issue_CrossEventRunSupersedesConcurrentLegitimateRun(t *testing.T) {
	pushSuite := int64(100)
	prSuite := int64(200)
	wfID := int64(1)
	totalRuns := 2

	workflowRuns := func(pushStatus string, pushConclusion *string) []*github.WorkflowRun {
		return []*github.WorkflowRun{
			{
				// The push-event run: created first, lower run number.
				ID:           int64Ptr(10),
				Name:         stringPtr("CI"),
				WorkflowID:   &wfID,
				RunNumber:    intPtr(100),
				RunAttempt:   intPtr(1),
				Event:        stringPtr("push"),
				CheckSuiteID: &pushSuite,
				Status:       &pushStatus,
				Conclusion:   pushConclusion,
			},
			{
				// The pull_request-event run: same workflow, same SHA, created
				// a moment later — higher run number, so the per-workflow
				// "latest" logic crowns it and supersedes the push run.
				ID:           int64Ptr(11),
				Name:         stringPtr("CI"),
				WorkflowID:   &wfID,
				RunNumber:    intPtr(101),
				RunAttempt:   intPtr(1),
				Event:        stringPtr("pull_request"),
				CheckSuiteID: &prSuite,
				Status:       stringPtr("completed"),
				Conclusion:   stringPtr("success"),
			},
		}
	}

	tests := map[string]struct {
		pushTestRun *github.CheckRun
		pushRun     []*github.WorkflowRun
		want        []*ghaStatus
		wantRed     bool // Validate must fail citing the push run's job
	}{
		"push-event failure dropped, gatekeeper goes green": {
			pushTestRun: &github.CheckRun{
				ID:         int64Ptr(1),
				Name:       stringPtr("test"),
				Status:     stringPtr(checkRunCompletedStatus),
				Conclusion: stringPtr("failure"),
				CheckSuite: &github.CheckSuite{ID: &pushSuite},
			},
			pushRun: workflowRuns("completed", stringPtr("failure")),
			// Both event runs are current: the push run's failure must be
			// tracked (disambiguated or not — what matters is it exists and
			// turns the gatekeeper red).
			wantRed: true,
		},
		"push-event in-progress job dropped, gatekeeper goes green early": {
			pushTestRun: &github.CheckRun{
				ID:         int64Ptr(1),
				Name:       stringPtr("test"),
				Status:     stringPtr("in_progress"),
				CheckSuite: &github.CheckSuite{ID: &pushSuite},
			},
			pushRun: workflowRuns("in_progress", nil),
			want: []*ghaStatus{
				{Job: "test", State: pendingState},
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			client := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							tt.pushTestRun,
							{
								// The same job in the pull_request-event run, passing.
								ID:         int64Ptr(2),
								Name:       stringPtr("test"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
								CheckSuite: &github.CheckSuite{ID: &prSuite},
							},
						},
					}, nil, nil
				},
				ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
					return &github.WorkflowRuns{
						TotalCount:   &totalRuns,
						WorkflowRuns: tt.pushRun,
					}, nil, nil
				},
			}

			sv := &statusValidator{
				client:      client,
				owner:       "test-owner",
				repo:        "test-repo",
				ref:         "main",
				selfJobName: "self-job",
			}

			gotStatus, err := sv.Validate(context.Background())
			if tt.wantRed {
				if err == nil {
					t.Fatalf("FALSE GREEN: Validate() should fail on the push-event run's "+
						"\"test\" failure; instead the concurrent pull_request-event run of "+
						"the same workflow superseded it and the failure was dropped. "+
						"Status detail:\n%s", gotStatus.Detail())
				}
				return
			}

			if err != nil {
				t.Fatalf("Validate() returned unexpected error: %v", err)
			}
			if gotStatus.IsSuccess() {
				t.Errorf("EARLY GREEN: Validate() reported success while the push-event run's "+
					"\"test\" is still in_progress — dropped as superseded by the concurrent "+
					"pull_request-event run. Status detail:\n%s", gotStatus.Detail())
			}
			got, err := sv.listGhaStatuses(context.Background())
			if err != nil {
				t.Fatalf("listGhaStatuses returned unexpected error: %v", err)
			}
			// The in-progress push-run job must still be visible (its display
			// name may or may not be disambiguated; assert on raw content).
			sort.Slice(got, func(i, j int) bool { return got[i].Job < got[j].Job })
			foundPending := false
			for _, s := range got {
				if s.State == pendingState {
					foundPending = true
				}
			}
			if !foundPending {
				t.Errorf("listGhaStatuses() lost the in-progress push-event job entirely: %v, want it pending (e.g. %v)",
					formatStatuses(got), formatStatuses(tt.want))
			}
		})
	}
}
