package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: the CLI and action.yml document that --ref "can be a SHA, a branch
// name, or tag name", and the check-runs / combined-status APIs do accept all
// three. But listWorkflowRunsForRef forwards the ref verbatim as the
// workflow-runs listing's HeadSHA filter, which GitHub matches EXACTLY
// against commit SHAs. With a branch or tag ref the listing comes back empty,
// filterStaleCheckRuns returns a nil workflow state, and every protection
// built on it switches off silently:
//
//   - dedup falls back to name-only keys → the PR #13859/#13862 masking bug
//     (this fork's founding fix) is back: a higher-suite success hides a
//     lower-suite failure of a same-named job from another workflow;
//   - superseded/orphan/stale-attempt filtering is off;
//   - empty-suite (queued run) tracking is off.
//
// The mock below behaves like the real API: workflow runs are returned only
// when queried with the actual head SHA; the branch-name query finds nothing.
//
// Expected correct behavior: a non-SHA ref is resolved to the commit SHA
// before querying the workflow-runs listing, so the protections work for
// every documented ref form. (Pre-fix this test reproduces the masked
// failure: Validate reports success.)
func Test_Issue_BranchNameRefDisablesWorkflowProtections(t *testing.T) {
	headSHA := "0f5aa78ee3f0b9a5f44c7d1ff342517b8b3dc3e7"
	committerSuite := int64(100)
	blockifierSuite := int64(200) // higher suite ID wins name-only dedup
	committerWorkflow := int64(1)
	blockifierWorkflow := int64(2)
	totalRuns := 2

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			// The check-runs API accepts branch names — it returns the head
			// commit's check runs for ref "main".
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						ID:         int64Ptr(1),
						Name:       stringPtr("benchmarking"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr("failure"),
						CheckSuite: &github.CheckSuite{ID: &committerSuite},
					},
					{
						ID:         int64Ptr(2),
						Name:       stringPtr("benchmarking"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &blockifierSuite},
					},
				},
			}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			// Faithful to the real API: head_sha is an exact SHA match. The
			// branch name "main" matches no run; only the real SHA does.
			if opts.HeadSHA != headSHA {
				return &github.WorkflowRuns{}, nil, nil
			}
			return &github.WorkflowRuns{
				TotalCount: &totalRuns,
				WorkflowRuns: []*github.WorkflowRun{
					{
						ID:           int64Ptr(10),
						Name:         stringPtr("Committer-CI"),
						WorkflowID:   &committerWorkflow,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &committerSuite,
						Status:       stringPtr("completed"),
						Conclusion:   stringPtr("failure"),
					},
					{
						ID:           int64Ptr(11),
						Name:         stringPtr("Blockifier-CI"),
						WorkflowID:   &blockifierWorkflow,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &blockifierSuite,
						Status:       stringPtr("completed"),
						Conclusion:   stringPtr("success"),
					},
				},
			}, nil, nil
		},
		GetCommitSHA1Func: func(ctx context.Context, owner, repo, ref string) (string, *github.Response, error) {
			// The branch resolves to the head commit.
			if ref == "main" {
				return headSHA, nil, nil
			}
			return ref, nil, nil
		},
	}

	sv := &statusValidator{
		client:      client,
		owner:       "test-owner",
		repo:        "test-repo",
		ref:         "main", // documented-supported branch-name ref
		selfJobName: "self-job",
	}

	gotStatus, err := sv.Validate(context.Background())
	if err == nil {
		t.Fatalf("FALSE GREEN: Validate() should fail on Committer-CI's \"benchmarking\" "+
			"failure; with a branch-name ref the workflow-runs listing came back empty "+
			"(HeadSHA is an exact SHA filter) and name-only dedup masked the failure "+
			"behind Blockifier-CI's success — the original PR#13862 bug. Status detail:\n%s",
			gotStatus.Detail())
	}
	if !containsString(err.Error(), "benchmarking") {
		t.Errorf("Validate() error should mention 'benchmarking'; got: %v", err)
	}
}
