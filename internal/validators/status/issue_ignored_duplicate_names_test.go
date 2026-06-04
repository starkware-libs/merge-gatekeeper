package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: the duplicate-named-jobs guard refuses to validate when any
// workflow defines two YAML jobs with the same display name, because the
// gatekeeper cannot track such jobs reliably. But it doesn't consult the
// ignore list: a duplicate name that the user explicitly excluded with
// --ignored still hard-fails the entire gatekeeper — even though an ignored
// job is never tracked, so the ambiguity is harmless for it. The --ignored
// escape hatch is exactly what a team would reach for when a (possibly
// generated) workflow with duplicate names can't be renamed right away, and
// it doesn't work.
//
// Expected correct behavior: duplicate names that are config-excluded
// (--ignored or --self) don't fail the guard; any other duplicate name still
// does.
func Test_Issue_DuplicateNamedJobsGuardIgnoresIgnoreList(t *testing.T) {
	ciWorkflow := int64(1)
	totalRuns := 1

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			return &github.WorkflowRuns{
				TotalCount: &totalRuns,
				WorkflowRuns: []*github.WorkflowRun{
					{
						ID:         int64Ptr(10),
						Name:       stringPtr("Generated-CI"),
						WorkflowID: &ciWorkflow,
						RunNumber:  intPtr(1),
					},
				},
			}, nil, nil
		},
		ListWorkflowJobsFunc: func(ctx context.Context, owner, repo string, runID int64, opts *github.ListWorkflowJobsOptions) (*github.Jobs, *github.Response, error) {
			// The workflow defines two jobs named "noisy-matrix" — and the
			// user has explicitly ignored that name.
			total := 3
			return &github.Jobs{
				TotalCount: &total,
				Jobs: []*github.WorkflowJob{
					{ID: int64Ptr(1), Name: stringPtr("noisy-matrix")},
					{ID: int64Ptr(2), Name: stringPtr("noisy-matrix")},
					{ID: int64Ptr(3), Name: stringPtr("build")},
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
		ignoredJobs: []string{"noisy-matrix"},
	}

	gotStatus, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() must not fail on duplicate names the user explicitly "+
			"ignored — the job is never tracked, so the ambiguity is harmless and "+
			"--ignored is the documented escape hatch. Got: %v", err)
	}
	if !gotStatus.IsSuccess() {
		t.Errorf("Validate() should succeed: no tracked jobs exist. Status detail:\n%s",
			gotStatus.Detail())
	}
}
