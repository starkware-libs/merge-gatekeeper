package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: filterStaleCheckRuns keeps settled successes (success/neutral/
// skipped) BEFORE consulting supersededSuites, justified by "re-run failed
// jobs doesn't repeat succeeded jobs". That rationale only holds for re-run
// ATTEMPTS, which reuse the same run and suite — the stale-attempt check
// (commit c87bf3f) covers those. A SUPERSEDING run (new run number: re-opened
// PR, repeated workflow_dispatch, duplicate trigger) is a full fresh
// execution: every job re-runs, so an old suite's success is exactly as stale
// as its failures while the new run is in progress.
//
// Concretely: run 1 completed all-green. The workflow is re-triggered at the
// same SHA (run 2). Run 2's check suite materializes check runs lazily —
// "setup" exists and finished green, but "integration-tests" (a generated
// matrix job) has no check run yet. The only visible state for
// "integration-tests" is run 1's stale success, the keep lets it count, and
// the gatekeeper goes green mid-re-run. If run 2 then fails
// integration-tests, the PR was mergeable on a red commit — the cross-suite
// mirror image of the stale-success-during-rerun-all bug.
//
// (The queued-run placeholder can't help: run 2's suite already has a check
// run. The stale-attempt check can't either: run 2 is attempt 1.)
//
// Expected correct behavior: while a superseding run is in progress, a
// superseded suite's settled successes are converted to pending like any
// other conclusion from that suite; once the superseding run completes they
// are dropped in favor of its fresh results.
func Test_Issue_SupersededSuiteStaleSuccessCountsDuringRerun(t *testing.T) {
	oldSuite := int64(100)
	newSuite := int64(200)
	wfID := int64(1)
	totalRuns := 2

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						// Run 1: everything finished green.
						ID:         int64Ptr(1),
						Name:       stringPtr("setup"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &oldSuite},
					},
					{
						ID:         int64Ptr(2),
						Name:       stringPtr("integration-tests"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &oldSuite},
					},
					{
						// Run 2: "setup" already re-ran green; the
						// "integration-tests" check run is not materialized yet.
						ID:         int64Ptr(3),
						Name:       stringPtr("setup"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &newSuite},
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
						CheckSuiteID: &oldSuite,
						Status:       stringPtr("completed"),
						Conclusion:   stringPtr("success"),
					},
					{
						// The re-trigger: a fresh full execution, in progress.
						ID:           int64Ptr(11),
						Name:         stringPtr("CI"),
						WorkflowID:   &wfID,
						RunNumber:    intPtr(2),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &newSuite,
						Status:       stringPtr("in_progress"),
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

	gotStatus, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() returned unexpected error: %v", err)
	}
	if gotStatus.IsSuccess() {
		t.Errorf("FALSE GREEN: Validate() reported success while run 2 (the superseding "+
			"re-trigger) is in progress — \"integration-tests\"'s only visible conclusion "+
			"is run 1's stale success, and its re-execution hasn't materialized as a check "+
			"run yet. Status detail:\n%s", gotStatus.Detail())
	}
}
