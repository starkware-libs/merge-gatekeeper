package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: detectDuplicateNamedJobs inspects exactly ONE run per workflow — the
// highest run number, cancelled runs included. If that newest run was cancelled
// before any job materialized, its jobs listing is empty forever, so the guard
// passes vacuously on EVERY poll and never falls back to an older run of the
// same workflow whose jobs would prove the duplicate. Meanwhile the older
// completed run is NOT superseded (cancelled runs can't supersede), so its two
// same-named check runs stay live, the (workflow, event, name) dedup collapses
// them, and the higher-check-run-ID success masks the failure — the exact
// masking the guard exists to prevent.
//
// Setup: workflow "CI" has run #1 (completed, two YAML jobs named "test": one
// failed, one succeeded with a higher check-run ID) and run #2 (cancelled with
// zero jobs — cancelled while queued, before job materialization).
//
// Expected correct behavior: the YAML is invariant per SHA, so run #1's jobs
// prove workflow "CI" defines two jobs named "test" — Validate must fail loud
// with the duplicate-named-jobs error (and certainly must not report success).
func Test_Hunt_DupGuardNewestRunEmpty(t *testing.T) {
	wfID := int64(1)
	suite1 := int64(100) // run #1 (completed, has the duplicate jobs)
	suite2 := int64(200) // run #2 (cancelled before any job materialized)
	totalRuns := 2

	jobsListedForRun := make(map[int64]int)

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						// One of the two YAML jobs named "test": failed.
						ID:         int64Ptr(1),
						Name:       stringPtr("test"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr("failure"),
						CheckSuite: &github.CheckSuite{ID: &suite1},
					},
					{
						// The other "test": succeeded, higher check-run ID —
						// the dedup tiebreaker picks this one.
						ID:         int64Ptr(2),
						Name:       stringPtr("test"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &suite1},
					},
				},
			}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			return &github.WorkflowRuns{
				TotalCount: &totalRuns,
				WorkflowRuns: []*github.WorkflowRun{
					{
						ID:           int64Ptr(10),
						Name:         stringPtr("CI"),
						WorkflowID:   &wfID,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &suite1,
						Status:       stringPtr(checkRunCompletedStatus),
						Conclusion:   stringPtr("failure"),
					},
					{
						// Newest run: cancelled while queued — no jobs ever.
						ID:           int64Ptr(20),
						Name:         stringPtr("CI"),
						WorkflowID:   &wfID,
						RunNumber:    intPtr(2),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &suite2,
						Status:       stringPtr(checkRunCompletedStatus),
						Conclusion:   stringPtr("cancelled"),
					},
				},
			}, nil, nil
		},
		ListWorkflowJobsFunc: func(ctx context.Context, owner, repo string, runID int64, opts *github.ListWorkflowJobsOptions) (*github.Jobs, *github.Response, error) {
			jobsListedForRun[runID]++
			if runID == 20 {
				// Cancelled-while-queued run: jobs never materialized.
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

	// Poll a few times: the cancelled-empty newest run never changes, so the
	// guard gets every chance to inspect run #1 instead.
	var lastErr error
	for poll := 1; poll <= 3; poll++ {
		st, err := sv.Validate(context.Background())
		lastErr = err
		if err == nil && st.IsSuccess() {
			t.Fatalf("FALSE GREEN on poll %d: Validate() reported success although workflow \"CI\" "+
				"defines two jobs named \"test\" and one of them failed — the guard only inspected "+
				"the cancelled-empty newest run (jobs listed per run: %v) and dedup masked the "+
				"failure behind the duplicate's success. Status detail:\n%s",
				poll, jobsListedForRun, st.Detail())
		}
	}
	if lastErr == nil {
		t.Fatalf("Validate() returned no duplicate-named-jobs error after 3 polls; jobs listed per run: %v", jobsListedForRun)
	}
}
