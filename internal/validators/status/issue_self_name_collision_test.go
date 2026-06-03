package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: the self-job exclusion in Validate compares sv.selfJobName against
// the *display* name of each tracked job. When the gatekeeper's own job name
// exists in two workflows at the same SHA (e.g. a gatekeeper job in both
// Main-CI-Flow and Main-CI-PR-Flow), the cross-workflow collision logic
// renames both instances to "merge-gatekeeper [Workflow-Name]". Neither
// renamed instance equals selfJobName anymore, so the gatekeeper starts
// tracking its own in-progress check run — which can never complete while it
// is polling — and deadlocks until the timeout, then goes red.
//
// Expected correct behavior: a job whose RAW name matches selfJobName is the
// gatekeeper (in any workflow) and must be excluded from tracking, exactly as
// it is when no collision exists.
func Test_Issue_SelfJobNameCollisionTracksItself(t *testing.T) {
	suiteA := int64(100)
	suiteB := int64(200)
	wfA := int64(1)
	wfB := int64(2)
	totalRuns := 2

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						// The gatekeeper instance in Main-CI-Flow: this very
						// process — in_progress for as long as it polls.
						ID:         int64Ptr(1),
						Name:       stringPtr("merge-gatekeeper"),
						Status:     stringPtr("in_progress"),
						CheckSuite: &github.CheckSuite{ID: &suiteA},
					},
					{
						// The gatekeeper instance in Main-CI-PR-Flow, running
						// concurrently at the same SHA.
						ID:         int64Ptr(2),
						Name:       stringPtr("merge-gatekeeper"),
						Status:     stringPtr("in_progress"),
						CheckSuite: &github.CheckSuite{ID: &suiteB},
					},
					{
						// The only real job: already succeeded.
						ID:         int64Ptr(3),
						Name:       stringPtr("build"),
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
						Name:         stringPtr("Main-CI-Flow"),
						WorkflowID:   &wfA,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &suiteA,
						Status:       stringPtr("in_progress"),
					},
					{
						ID:           int64Ptr(11),
						Name:         stringPtr("Main-CI-PR-Flow"),
						WorkflowID:   &wfB,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &suiteB,
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
		selfJobName: "merge-gatekeeper",
	}

	gotStatus, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() returned unexpected error: %v", err)
	}
	if !gotStatus.IsSuccess() {
		t.Errorf("Validate() must exclude the gatekeeper's own job even when its name "+
			"collides across workflows; got incomplete validation — the gatekeeper is "+
			"waiting for itself and will deadlock until timeout. Status detail:\n%s",
			gotStatus.Detail())
	}
}
