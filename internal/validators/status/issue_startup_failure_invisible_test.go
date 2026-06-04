package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: a workflow run that fails before any job starts — most commonly
// conclusion "startup_failure" from a workflow-file error introduced at this
// SHA — never creates check runs. The gatekeeper tracks check runs only, so
// the red run is invisible: if everything else is green the gatekeeper
// passes, while the PR's checks UI shows a failed workflow. The exact change
// that broke the workflow file is the change the gatekeeper green-lights.
//
// (The queued-run placeholder fix covers live suites with no check runs yet;
// this is its terminal sibling — completed, failed, and permanently empty.)
//
// Expected correct behavior: a completed workflow run with a failure-class
// conclusion (failure / startup_failure / timed_out) whose suite has no check
// runs is tracked as a failed entry and turns the gatekeeper red.
func Test_Issue_StartupFailureRunInvisible(t *testing.T) {
	okSuite := int64(100)
	brokenSuite := int64(200) // startup_failure: no check runs were ever created
	okWorkflow := int64(1)
	brokenWorkflow := int64(2)
	totalRuns := 2

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						ID:         int64Ptr(1),
						Name:       stringPtr("lint"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &okSuite},
					},
					// No check runs at all for brokenSuite.
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
						WorkflowID:   &okWorkflow,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &okSuite,
						Status:       stringPtr("completed"),
						Conclusion:   stringPtr("success"),
					},
					{
						// This PR broke Heavy-CI's workflow file: the run
						// exists, is red in the UI, and has no check runs.
						ID:           int64Ptr(11),
						Name:         stringPtr("Heavy-CI"),
						WorkflowID:   &brokenWorkflow,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &brokenSuite,
						Status:       stringPtr("completed"),
						Conclusion:   stringPtr("startup_failure"),
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
	if err == nil {
		t.Fatalf("FALSE GREEN: Validate() should fail — workflow \"Heavy-CI\" completed "+
			"with startup_failure and produced no check runs, so the red run is invisible "+
			"to the gatekeeper. Status detail:\n%s", gotStatus.Detail())
	}
	if !containsString(err.Error(), "Heavy-CI") {
		t.Errorf("Validate() error should mention the failed workflow 'Heavy-CI'; got: %v", err)
	}
}
