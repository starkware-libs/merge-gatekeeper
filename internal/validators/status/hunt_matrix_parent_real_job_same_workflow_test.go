package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: the matrix-parent heuristic drops any pending/cancelled check run
// named "X" whenever the SAME workflow also has a job named "X (...)". But
// "X" and "X (...)" can be two real, distinct YAML jobs of one workflow
// (e.g. a standalone `build` job next to a matrix `build (${{ matrix.v }})`).
// The workflow's own jobs listing — which detectDuplicateNamedJobs already
// fetches — literally contains a current job named "X", proving it is not a
// stuck leftover. Dropping it while pending green-lights the gatekeeper with
// that job still running; dropping it when cancelled hides a cancellation
// that must gate red.
//
// Expected correct behavior: a name that appears as a current job of the
// workflow's latest run is a real job, not a stuck matrix parent — the
// gatekeeper must keep tracking it (pending blocks; cancelled gates red).
func Test_Hunt_MatrixParentHeuristicSwallowsRealSiblingJob(t *testing.T) {
	suite := int64(100)
	wfID := int64(1)
	totalRuns := 1

	checkRunStatus := "in_progress"

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						// A real standalone YAML job, still running. Shares the
						// "build" prefix with the matrix siblings below.
						ID:         int64Ptr(1),
						Name:       stringPtr("build"),
						Status:     &checkRunStatus,
						CheckSuite: &github.CheckSuite{ID: &suite},
					},
					{
						ID:         int64Ptr(2),
						Name:       stringPtr("build (ubuntu)"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &suite},
					},
					{
						ID:         int64Ptr(3),
						Name:       stringPtr("build (macos)"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &suite},
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
						CheckSuiteID: &suite,
						Status:       stringPtr("in_progress"),
					},
				},
			}, nil, nil
		},
		ListWorkflowJobsFunc: func(ctx context.Context, owner, repo string, runID int64, opts *github.ListWorkflowJobsOptions) (*github.Jobs, *github.Response, error) {
			// The latest run's jobs listing proves "build" is a real current
			// job of this workflow, alongside the matrix children.
			totalJobs := 3
			return &github.Jobs{
				TotalCount: &totalJobs,
				Jobs: []*github.WorkflowJob{
					{ID: int64Ptr(1), Name: stringPtr("build")},
					{ID: int64Ptr(2), Name: stringPtr("build (ubuntu)")},
					{ID: int64Ptr(3), Name: stringPtr("build (macos)")},
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

	// Phase 1: "build" is still running — the gatekeeper must not succeed.
	st, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() returned an unexpected error: %v", err)
	}
	if st.IsSuccess() {
		t.Errorf("FALSE GREEN: Validate() reported success while the real job \"build\" is still "+
			"in progress — it was misclassified as a stuck matrix parent because matrix siblings "+
			"\"build (...)\" exist in the same workflow. Status detail:\n%s", st.Detail())
	}

	// Phase 2: "build" gets cancelled — the gatekeeper must gate red, not
	// silently drop the cancellation.
	checkRunStatus = checkRunCompletedStatus
	cancelled := checkRunCancelledConclusion
	client.ListCheckRunsForRefFunc = func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
		return &github.ListCheckRunsResults{
			CheckRuns: []*github.CheckRun{
				{
					ID:         int64Ptr(1),
					Name:       stringPtr("build"),
					Status:     stringPtr(checkRunCompletedStatus),
					Conclusion: &cancelled,
					CheckSuite: &github.CheckSuite{ID: &suite},
				},
				{
					ID:         int64Ptr(2),
					Name:       stringPtr("build (ubuntu)"),
					Status:     stringPtr(checkRunCompletedStatus),
					Conclusion: stringPtr(checkRunSuccessConclusion),
					CheckSuite: &github.CheckSuite{ID: &suite},
				},
				{
					ID:         int64Ptr(3),
					Name:       stringPtr("build (macos)"),
					Status:     stringPtr(checkRunCompletedStatus),
					Conclusion: stringPtr(checkRunSuccessConclusion),
					CheckSuite: &github.CheckSuite{ID: &suite},
				},
			},
		}, nil, nil
	}

	st, err = sv.Validate(context.Background())
	if err == nil && st.IsSuccess() {
		t.Errorf("FALSE GREEN: Validate() reported success although the real job \"build\" was "+
			"cancelled — the cancellation was hidden by the matrix-parent heuristic. Status detail:\n%s",
			st.Detail())
	}
}
