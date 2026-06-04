package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: the empty-run failure tracking (commit e07fe52) and the partial-
// materialization placeholder (commit 8a5a470) leave a gap between them — a
// workflow run that COMPLETED with conclusion=failure whose suite has SOME
// materialized check runs, none of them blocking, is skipped by both: the
// placeholder loop sees "completed && hasCheckRuns" and assumes the
// materialized check runs carry the signal.
//
// Concretely: a workflow's fast job ("lint") succeeds, the slow job
// ("integration-tests") fails and the run completes with conclusion=failure —
// but the failed job's check run has not materialized in the per-ref
// check-runs listing yet (the same PR#14205 eventual-consistency window every
// recent staleness fix handles, in its terminal form). The gatekeeper then
// tracks only lint=success, totalJobs == successCnt, and Validate reports
// success — a false green for a SHA whose CI demonstrably failed.
//
// Expected correct behavior: a completed workflow run with a failure-class
// conclusion whose suite shows no blocking check run (nothing pending, nothing
// failed) must not green-light the gate — hold it pending until the listing
// becomes consistent (or go red), exactly like the zero-check-runs failure
// case already does.
func Test_Hunt_CompletedFailureRunWithOnlyGreenCheckRunsGoesGreen(t *testing.T) {
	suiteID := int64(400)
	workflowID := int64(4)
	completed := "completed"
	success := "success"
	failure := "failure"
	totalRuns := 1
	totalCheckRuns := 1
	totalJobs := 2

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			// Only the fast green job's check run has materialized; the FAILED
			// job's check run is not in the listing yet.
			return &github.ListCheckRunsResults{
				Total: &totalCheckRuns,
				CheckRuns: []*github.CheckRun{
					{
						ID:         int64Ptr(2001),
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
						// The run is COMPLETED and FAILED — CI for this SHA is red.
						ID:           int64Ptr(41),
						Name:         stringPtr("CI"),
						WorkflowID:   &workflowID,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &suiteID,
						Event:        stringPtr("pull_request"),
						Status:       &completed,
						Conclusion:   &failure,
					},
				},
			}, nil, nil
		},
		ListWorkflowJobsFunc: func(ctx context.Context, owner, repo string, runID int64, opts *github.ListWorkflowJobsOptions) (*github.Jobs, *github.Response, error) {
			// The jobs API knows both YAML jobs — it is the per-ref check-runs
			// listing that lags.
			return &github.Jobs{
				TotalCount: &totalJobs,
				Jobs: []*github.WorkflowJob{
					{ID: int64Ptr(601), Name: stringPtr("lint")},
					{ID: int64Ptr(602), Name: stringPtr("integration-tests")},
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
		// Going red on a failed run is correct behavior — not the bug.
		return
	}
	if gotStatus.IsSuccess() {
		t.Errorf("FALSE GREEN: Validate() reported success although workflow run \"CI\" COMPLETED with "+
			"conclusion=failure — its only materialized check run (lint=success) green-lit the gate while "+
			"the failed job's check run (integration-tests) had not materialized in the per-ref listing "+
			"yet. Status detail:\n%s", gotStatus.Detail())
	}
}
