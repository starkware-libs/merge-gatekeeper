package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: Validate caches the duplicate-named-jobs guard after its first
// successful run ("the YAML structure is fixed for this ref"). But the guard
// passes VACUOUSLY when the workflow run is still queued and the jobs API
// returns nothing — the standard state on a gatekeeper's first poll seconds
// after the trigger. The cache then disables the guard for the rest of the
// run, so when the two same-named YAML jobs materialize a poll later, nobody
// fails loud; the (workflow, event, name) dedup silently collapses them and
// one job's success masks the other's failure — the exact masking the guard
// exists to prevent (it never inspected any YAML structure before vouching
// for it).
//
// Poll 1: run queued, no jobs, no check runs → guard passes empty, caches.
// Poll 2: two YAML jobs named "test" in ONE run; the higher-ID check run
// succeeded, the lower-ID one failed. Dedup keeps the higher ID → green.
//
// Expected correct behavior: the vacuous pass must not be cached; once jobs
// are visible the guard fails loud on the duplicate name (or, at minimum,
// the duplicate's failure is not masked) — Validate must not report success.
func Test_Issue_DuplicateGuardCachedOnVacuousFirstPoll(t *testing.T) {
	suite := int64(100)
	wfID := int64(1)
	totalRuns := 1
	poll := 0

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			if poll == 1 {
				// First poll: nothing materialized yet.
				return &github.ListCheckRunsResults{}, nil, nil
			}
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						// One of the two YAML jobs named "test": failed.
						ID:         int64Ptr(1),
						Name:       stringPtr("test"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr("failure"),
						CheckSuite: &github.CheckSuite{ID: &suite},
					},
					{
						// The other "test": succeeded, higher check-run ID —
						// the dedup tiebreaker picks this one.
						ID:         int64Ptr(2),
						Name:       stringPtr("test"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &suite},
					},
				},
			}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			status := "queued"
			if poll > 1 {
				status = "completed"
			}
			wr := &github.WorkflowRun{
				ID:           int64Ptr(10),
				Name:         stringPtr("CI"),
				WorkflowID:   &wfID,
				RunNumber:    intPtr(1),
				RunAttempt:   intPtr(1),
				CheckSuiteID: &suite,
				Status:       &status,
			}
			if poll > 1 {
				wr.Conclusion = stringPtr("failure")
			}
			return &github.WorkflowRuns{
				TotalCount:   &totalRuns,
				WorkflowRuns: []*github.WorkflowRun{wr},
			}, nil, nil
		},
		ListWorkflowJobsFunc: func(ctx context.Context, owner, repo string, runID int64, opts *github.ListWorkflowJobsOptions) (*github.Jobs, *github.Response, error) {
			if poll == 1 {
				// Queued run: jobs not materialized yet.
				return &github.Jobs{}, nil, nil
			}
			totalJobs := 2
			return &github.Jobs{
				TotalCount: &totalJobs,
				Jobs: []*github.WorkflowJob{
					{ID: int64Ptr(1), Name: stringPtr("test")},
					{ID: int64Ptr(2), Name: stringPtr("test")},
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

	// Poll 1: queued run, nothing to see. Any outcome but success is fine.
	poll = 1
	if st, err := sv.Validate(context.Background()); err == nil && st.IsSuccess() {
		t.Fatalf("poll 1: Validate() reported success while the only workflow run is queued")
	}

	// Poll 2: the duplicate-named jobs are now visible.
	poll = 2
	gotStatus, err := sv.Validate(context.Background())
	if err == nil && gotStatus.IsSuccess() {
		t.Errorf("FALSE GREEN: Validate() reported success although workflow \"CI\" defines two "+
			"jobs named \"test\" and one of them failed — the vacuous first-poll pass was cached, "+
			"the guard never re-ran, and dedup masked the failure behind the duplicate's success. "+
			"Status detail:\n%s", gotStatus.Detail())
	}
}
