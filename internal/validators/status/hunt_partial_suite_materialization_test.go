package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: the queued-run placeholder protection (commit 5ceffc3) is per-suite
// all-or-nothing — a suite with ANY check run in the listing gets no
// placeholder, even when its workflow run is still in_progress and every
// materialized check run is already terminal.
//
// Concretely: a workflow's fast first job ("lint") finishes in seconds while
// the check runs of its remaining jobs ("integration-tests") have not been
// materialized in the check-runs listing yet — the same eventual-consistency
// window the zero-check-runs placeholder was built for, with n-1 missing
// instead of n. The workflow-runs listing says the run is in_progress, so the
// gatekeeper KNOWS the suite is not done, yet it green-lights on the concluded
// subset: lint=success is the only tracked job, totalJobs == successCnt, and
// Validate reports success while integration-tests is still executing.
//
// Expected correct behavior: a non-completed workflow run whose suite has no
// live (non-completed) check run must hold the gatekeeper pending — exactly
// like the zero-check-runs case — until either a live check run appears or
// the run itself completes.
func Test_Hunt_InProgressRunWithOnlyConcludedCheckRunsGoesGreen(t *testing.T) {
	suiteID := int64(300)
	workflowID := int64(3)
	completed := "completed"
	success := "success"
	inProgress := "in_progress"
	totalRuns := 1
	totalCheckRuns := 1
	totalJobs := 2

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			// Only the fast job's check run has materialized; the slow job's
			// check run is not in the listing yet.
			return &github.ListCheckRunsResults{
				Total: &totalCheckRuns,
				CheckRuns: []*github.CheckRun{
					{
						ID:         int64Ptr(1001),
						Name:       stringPtr("lint"),
						Status:     &completed,
						Conclusion: &success,
						App:        &github.App{Slug: stringPtr("github-actions")},
						CheckSuite: &github.CheckSuite{ID: &suiteID},
					},
				},
			}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			return &github.WorkflowRuns{
				TotalCount: &totalRuns,
				WorkflowRuns: []*github.WorkflowRun{
					{
						// The run is demonstrably still executing.
						ID:           int64Ptr(31),
						Name:         stringPtr("CI"),
						WorkflowID:   &workflowID,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &suiteID,
						Event:        stringPtr("pull_request"),
						Status:       &inProgress,
					},
				},
			}, nil, nil
		},
		ListWorkflowJobsFunc: func(ctx context.Context, owner, repo string, runID int64, opts *github.ListWorkflowJobsOptions) (*github.Jobs, *github.Response, error) {
			// The jobs API already knows both YAML jobs — it is the check-runs
			// listing that lags.
			return &github.Jobs{
				TotalCount: &totalJobs,
				Jobs: []*github.WorkflowJob{
					{ID: int64Ptr(501), Name: stringPtr("lint")},
					{ID: int64Ptr(502), Name: stringPtr("integration-tests")},
				},
			}, nil, nil
		},
	}

	sv := &statusValidator{
		client:      client,
		owner:       "test-owner",
		repo:        "test-repo",
		ref:         "0123456789abcdef0123456789abcdef01234567",
		selfJobName: "self-job",
	}

	gotStatus, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() returned unexpected error: %v", err)
	}
	if gotStatus.IsSuccess() {
		t.Errorf("FALSE GREEN: Validate() reported success while workflow run \"CI\" is in_progress — "+
			"its only materialized check run (lint=success) green-lit the gate although the run-level "+
			"status proves more jobs (integration-tests) are still executing with their check runs "+
			"not yet materialized. Status detail:\n%s", gotStatus.Detail())
	}
}
