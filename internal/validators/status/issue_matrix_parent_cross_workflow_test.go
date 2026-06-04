package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: matrix-parent detection matches job-name prefixes across ALL
// tracked jobs without checking that the "parent" and the "children" belong
// to the same workflow. The code comment claims "a parent and its children
// belong to the same workflow", but the loop never compares workflow IDs:
//
//	for _, otherKey := range keys {
//	    if key.name != otherKey.name && strings.HasPrefix(otherKey.name, key.name+" (") { ... }
//	}
//
// So a real, independent job named "build" in workflow A is misclassified as
// a matrix parent merely because workflow B happens to define matrix jobs
// "build (ubuntu)" / "build (macos)". Misclassified parents in pending or
// cancelled state are silently dropped from tracking — meaning the gatekeeper
// reports SUCCESS while workflow A's "build" is still running. If that job
// later fails, the PR was already mergeable: a false green, the exact failure
// mode the gatekeeper exists to prevent.
//
// Expected correct behavior: the matrix-parent heuristic must only fire when
// the parent and children belong to the same workflow; an in-progress job in
// another workflow must stay tracked.
func Test_Issue_MatrixParentHeuristicSwallowsJobFromOtherWorkflow(t *testing.T) {
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
						// A real standalone job in workflow A, still running.
						// Not a matrix parent — workflow A has no matrix.
						ID:         int64Ptr(1),
						Name:       stringPtr("build"),
						Status:     stringPtr("in_progress"),
						CheckSuite: &github.CheckSuite{ID: &suiteA},
					},
					{
						// Matrix children in workflow B.
						ID:         int64Ptr(2),
						Name:       stringPtr("build (ubuntu)"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &suiteB},
					},
					{
						ID:         int64Ptr(3),
						Name:       stringPtr("build (macos)"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &suiteB},
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
						Name:         stringPtr("Workflow-A"),
						WorkflowID:   &wfA,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &suiteA,
						Status:       stringPtr("in_progress"),
					},
					{
						ID:           int64Ptr(11),
						Name:         stringPtr("Workflow-B"),
						WorkflowID:   &wfB,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &suiteB,
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
	if gotStatus.IsSuccess() {
		t.Errorf("FALSE GREEN: Validate() reported success while workflow A's standalone "+
			"job \"build\" is still in_progress — it was misclassified as a matrix parent "+
			"of workflow B's \"build (...)\" jobs and silently dropped. Status detail:\n%s",
			gotStatus.Detail())
	}
}
