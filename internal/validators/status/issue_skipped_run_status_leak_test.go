package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: listGhaStatuses protects check-run results from being overridden by
// stale commit statuses via the currentJobs set — "currentJobs just prevents
// a combined status from overriding a check run for the same context name"
// (comment in validator.go). But two code paths `continue` out of the
// check-run loop BEFORE the name is registered:
//
//   - skipped check runs (conclusion=skipped), and
//   - matrix parents dropped in pending/cancelled state.
//
// A combined-status context with the same name then slips past the guard and
// is tracked as a job. Concretely: a workflow job "build" was skipped this
// run (path filter), while an old commit-status integration once posted a
// "build" context that is stuck in state pending (commit statuses never
// expire and are SHA-scoped, so a stale "pending" stays forever). The
// gatekeeper now waits the full timeout for a context that will never
// complete — even though the check-run signal for "build" was "intentionally
// not run". Had "build" completed with success instead of skipped, the same
// stale context would have been correctly suppressed: the two
// "this job is fine" shapes are treated inconsistently.
//
// Expected correct behavior: any name that has a check run — including
// skipped ones and dropped matrix parents — suppresses same-name
// combined-status contexts, exactly as the comment promises.
func Test_Issue_SkippedCheckRunDoesNotSuppressSameNameStatusContext(t *testing.T) {
	suiteA := int64(100)
	wfA := int64(1)
	totalRuns := 1

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			one := 1
			return &github.CombinedStatus{
				TotalCount: &one,
				Statuses: []*github.RepoStatus{
					{
						// Stale commit-status context with the same name as
						// the skipped job, stuck pending forever.
						Context: stringPtr("build"),
						State:   stringPtr("pending"),
					},
				},
			}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						// The job was deliberately skipped this run.
						ID:         int64Ptr(1),
						Name:       stringPtr("build"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSkipConclusion),
						CheckSuite: &github.CheckSuite{ID: &suiteA},
					},
					{
						// A real job that already passed.
						ID:         int64Ptr(2),
						Name:       stringPtr("test"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &suiteA},
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
						WorkflowID:   &wfA,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &suiteA,
						Status:       stringPtr("completed"),
						Conclusion:   stringPtr("success"),
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
	if !gotStatus.IsSuccess() {
		t.Errorf("Validate() should succeed: the only real job passed and \"build\" was "+
			"deliberately skipped — but the skipped check run failed to suppress the stale "+
			"same-name commit-status context, so the gatekeeper waits forever for it. "+
			"Status detail:\n%s", gotStatus.Detail())
	}
}
