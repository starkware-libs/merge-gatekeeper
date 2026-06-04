package status

import (
	"context"
	"strings"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: dupVerifiedWorkflows memoizes per workflow ID for the whole
// gatekeeper run, justified by "the YAML is invariant for a given SHA" — but
// --ref is documented to accept a BRANCH name (the deployment shape commit
// ec3ab77 added SHA resolution for), and a branch's head advances mid-run.
//
// Concretely: the gatekeeper validates branch "main". Poll 1 sees head SHA A,
// whose workflow has unique job names — the workflow is memoized as verified.
// Mid-run, a push moves main to SHA B whose YAML defines two jobs named
// "build". Poll 2 resolves the new head and lists its check runs (two
// same-suite "build" runs), but detectDuplicateNamedJobs skips the workflow —
// still memoized from SHA A — so the guard never fires and the dedup silently
// collapses the two distinct jobs, masking one job's CI signal: the exact
// failure class the guard exists to prevent. currentJobNamesByWorkflow goes
// equally stale, feeding SHA A's job set to the matrix-parent heuristic.
//
// Expected correct behavior: when the resolved head SHA changes between
// polls, the per-SHA memoization is invalidated and poll 2 fails loudly with
// the duplicate-named-jobs error.
func Test_Hunt_BranchAdvanceKeepsStaleDuplicateJobsMemo(t *testing.T) {
	workflowID := int64(7)
	completed := "completed"
	success := "success"
	inProgress := "in_progress"

	// Flipped to true between polls to simulate the push to "main".
	branchAdvanced := false

	shaA := strings.Repeat("a", 40)
	shaB := strings.Repeat("b", 40)
	suiteA := int64(700)
	suiteB := int64(701)

	client := &mock.Client{
		GetCommitSHA1Func: func(ctx context.Context, owner, repo, ref string) (string, *github.Response, error) {
			if branchAdvanced {
				return shaB, nil, nil
			}
			return shaA, nil, nil
		},
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			if !branchAdvanced {
				// SHA A: one healthy "build" job, still running.
				return &github.ListCheckRunsResults{
					CheckRuns: []*github.CheckRun{
						{
							ID:         int64Ptr(1001),
							Name:       stringPtr("build"),
							Status:     &inProgress,
							App:        &github.App{Slug: stringPtr("github-actions")},
							CheckSuite: &github.CheckSuite{ID: &suiteA},
						},
					},
				}, nil, nil
			}
			// SHA B: the duplicated YAML produced two same-suite "build" check
			// runs — one succeeded, one still running. Collapsing them would
			// let the settled success mask the running job.
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						ID:         int64Ptr(2001),
						Name:       stringPtr("build"),
						Status:     &completed,
						Conclusion: &success,
						App:        &github.App{Slug: stringPtr("github-actions")},
						CheckSuite: &github.CheckSuite{ID: &suiteB},
					},
					{
						ID:         int64Ptr(2002),
						Name:       stringPtr("build"),
						Status:     &inProgress,
						App:        &github.App{Slug: stringPtr("github-actions")},
						CheckSuite: &github.CheckSuite{ID: &suiteB},
					},
				},
			}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			one := 1
			if !branchAdvanced {
				return &github.WorkflowRuns{
					TotalCount: &one,
					WorkflowRuns: []*github.WorkflowRun{
						{
							ID:           int64Ptr(71),
							Name:         stringPtr("CI"),
							WorkflowID:   &workflowID,
							RunNumber:    intPtr(1),
							RunAttempt:   intPtr(1),
							CheckSuiteID: &suiteA,
							Event:        stringPtr("push"),
							Status:       &inProgress,
						},
					},
				}, nil, nil
			}
			return &github.WorkflowRuns{
				TotalCount: &one,
				WorkflowRuns: []*github.WorkflowRun{
					{
						ID:           int64Ptr(72),
						Name:         stringPtr("CI"),
						WorkflowID:   &workflowID,
						RunNumber:    intPtr(2),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &suiteB,
						Event:        stringPtr("push"),
						Status:       &inProgress,
					},
				},
			}, nil, nil
		},
		ListWorkflowJobsFunc: func(ctx context.Context, owner, repo string, runID int64, opts *github.ListWorkflowJobsOptions) (*github.Jobs, *github.Response, error) {
			if !branchAdvanced {
				// SHA A's YAML: unique job names — the workflow verifies clean.
				two := 2
				return &github.Jobs{
					TotalCount: &two,
					Jobs: []*github.WorkflowJob{
						{ID: int64Ptr(501), Name: stringPtr("build")},
						{ID: int64Ptr(502), Name: stringPtr("test")},
					},
				}, nil, nil
			}
			// SHA B's YAML: two jobs share the display name "build".
			two := 2
			return &github.Jobs{
				TotalCount: &two,
				Jobs: []*github.WorkflowJob{
					{ID: int64Ptr(601), Name: stringPtr("build")},
					{ID: int64Ptr(602), Name: stringPtr("build")},
				},
			}, nil, nil
		},
	}

	sv := &statusValidator{
		client:      client,
		owner:       "test-owner",
		repo:        "test-repo",
		ref:         "main", // branch ref — resolved to a head SHA each poll
		selfJobName: "self-job",
	}

	// Poll 1: SHA A, unique names — must pass and memoize the workflow.
	if _, err := sv.Validate(context.Background()); err != nil {
		t.Fatalf("poll 1: Validate() returned unexpected error: %v", err)
	}

	// The push: main advances to SHA B with duplicate-named jobs.
	branchAdvanced = true

	// Poll 2: the guard must fire on the new head's YAML.
	_, err := sv.Validate(context.Background())
	if err == nil {
		t.Fatalf("STALE MEMO: poll 2 Validate() returned no error although the advanced head SHA's " +
			"workflow defines two jobs named \"build\" — the workflow stayed memoized as verified " +
			"from the previous head SHA, so the duplicate guard never re-inspected it and the dedup " +
			"silently collapsed the two jobs")
	}
	if !strings.Contains(err.Error(), "display name") {
		t.Fatalf("poll 2 Validate() returned an unrelated error (want the duplicate-named-jobs error): %v", err)
	}
}
