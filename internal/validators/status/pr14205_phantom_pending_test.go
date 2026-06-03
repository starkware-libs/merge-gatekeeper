package status

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Test_PR14205_CancelledDuplicateRunPhantomPending reproduces the May 26
// one-hour timeout on sequencer PR #14205 (gatekeeper job 77889641552,
// reported by Dori): the gatekeeper showed commitlint as incomplete for the
// full hour and died with "context deadline exceeded", even though commitlint
// had succeeded 27 seconds in.
//
// Two Main-CI-PR-Flow runs were triggered one second apart at the same SHA.
// The later-created run B (higher suite ID) was cancelled; the earlier run A
// survived — and contained the gatekeeper job itself. On the real PR:
//   - commitlint in run A (suite 70818287230, id 77889641517): success
//   - commitlint in run B (suite 70818289560, id 77888517091): cancelled
//
// The supersede filter correctly marked run B's suite as superseded by run A,
// but run A could never complete while the gatekeeper inside it was polling,
// so run B's cancelled commitlint was converted to pending instead of dropped.
// The dedup tiebreaker then picked the highest suite ID — run B's phantom —
// over run A's real success, and the gatekeeper deadlocked with itself until
// the timeout.
//
// Post-fix expectation: when instances of the same (workflow, name) span
// multiple suites, the suite of the workflow's latest non-cancelled run wins,
// so the real success is tracked and the gatekeeper turns green.
//
// The mock includes the gatekeeper's own in-progress check run (job
// 77889641552) in the surviving suite, as on the real PR: run A is
// in_progress precisely because the gatekeeper job inside it is still
// executing, so its live check run is part of the listing. Without it the
// partial-materialization placeholder would (correctly) hold the gate for a
// run that claims to be executing invisible jobs.
func Test_PR14205_CancelledDuplicateRunPhantomPending(t *testing.T) {
	survivingSuite := int64(70818287230)
	cancelledSuite := int64(70818289560) // higher suite ID, created 1s later
	mainCIPRFlowWorkflow := int64(67890)
	totalRuns := 2

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						ID:         int64Ptr(77889641517),
						Name:       stringPtr("commitlint"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &survivingSuite},
					},
					{
						ID:         int64Ptr(77888517091),
						Name:       stringPtr("commitlint"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunCancelledConclusion),
						CheckSuite: &github.CheckSuite{ID: &cancelledSuite},
					},
					{
						// The gatekeeper job itself, polling inside run A.
						ID:         int64Ptr(77889641552),
						Name:       stringPtr("self-job"),
						Status:     stringPtr("in_progress"),
						CheckSuite: &github.CheckSuite{ID: &survivingSuite},
					},
				},
			}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			return &github.WorkflowRuns{
				TotalCount: &totalRuns,
				WorkflowRuns: []*github.WorkflowRun{
					{
						// Run A: survived, hosts the gatekeeper — in progress for as
						// long as the gatekeeper polls.
						ID:           int64Ptr(26455592051),
						Name:         stringPtr("Main-CI-PR-Flow"),
						WorkflowID:   &mainCIPRFlowWorkflow,
						RunNumber:    intPtr(134969),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &survivingSuite,
						Status:       stringPtr("in_progress"),
					},
					{
						// Run B: the duplicate trigger, cancelled 5 minutes in.
						ID:           int64Ptr(26455592768),
						Name:         stringPtr("Main-CI-PR-Flow"),
						WorkflowID:   &mainCIPRFlowWorkflow,
						RunNumber:    intPtr(134970),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &cancelledSuite,
						Status:       stringPtr("completed"),
						Conclusion:   stringPtr("cancelled"),
					},
				},
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

	got, err := sv.listGhaStatuses(context.Background())
	if err != nil {
		t.Fatalf("listGhaStatuses returned unexpected error: %v", err)
	}

	want := []*ghaStatus{
		{Job: "commitlint", State: successState},
		{Job: "self-job", State: pendingState},
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Job < got[j].Job })
	if !reflect.DeepEqual(got, want) {
		t.Errorf("listGhaStatuses() = %v, want %v", formatStatuses(got), formatStatuses(want))
	}

	gotStatus, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() returned unexpected error: %v", err)
	}
	if !gotStatus.IsSuccess() {
		t.Errorf("Validate() should succeed: the only tracked job succeeded in the surviving run")
	}
}
