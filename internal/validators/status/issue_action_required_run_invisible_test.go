package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: the empty-suite failure surfacing (commit e07fe52) tracks completed
// workflow runs that produced no check runs only for the conclusions failure,
// startup_failure and timed_out. A run blocked on approval — a fork PR's CI
// waiting for "Approve and run", which GitHub reports as status=completed,
// conclusion=action_required, with NO check runs ever created — falls through
// the switch and stays invisible. A gatekeeper polling from a
// pull_request_target workflow (or validating a branch ref) then green-lights
// the PR with its CI never having run at all.
//
// The inconsistency makes the hole sharp: a check RUN concluding
// action_required maps to errorState (red) in listGhaStatuses, so the same
// verdict gates red when jobs materialized and green when they never did.
//
// Expected correct behavior: an empty completed run with conclusion
// action_required must not let the gatekeeper succeed — it should surface
// like the other invisible-failure conclusions.
func Test_Issue_ActionRequiredRunWithoutCheckRunsIsInvisible(t *testing.T) {
	doneSuite := int64(100)
	blockedSuite := int64(200)
	wfDone := int64(1)
	wfBlocked := int64(2)
	totalRuns := 2

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						// An unblocked workflow (e.g. pull_request_target) finished green.
						ID:         int64Ptr(1),
						Name:       stringPtr("lint"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &doneSuite},
					},
					// NOTE: no check runs for blockedSuite — approval-gated runs
					// never create any.
				},
			}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			return &github.WorkflowRuns{
				TotalCount: &totalRuns,
				WorkflowRuns: []*github.WorkflowRun{
					{
						ID:           int64Ptr(10),
						Name:         stringPtr("Fast-Lint"),
						WorkflowID:   &wfDone,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &doneSuite,
						Status:       stringPtr("completed"),
						Conclusion:   stringPtr("success"),
					},
					{
						// The fork PR's CI, waiting for a maintainer's
						// "Approve and run".
						ID:           int64Ptr(11),
						Name:         stringPtr("Main-CI"),
						WorkflowID:   &wfBlocked,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &blockedSuite,
						Status:       stringPtr("completed"),
						Conclusion:   stringPtr("action_required"),
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
	if err == nil && gotStatus.IsSuccess() {
		t.Errorf("FALSE GREEN: Validate() reported success while workflow \"Main-CI\" is "+
			"blocked on approval (completed/action_required, zero check runs) — merging "+
			"now ships code whose CI never ran. Status detail:\n%s", gotStatus.Detail())
	}
}
