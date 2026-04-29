package status

import (
	"context"
	"sort"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Test_PR13859_NameCollisionAcrossWorkflows reproduces the bug observed on
// starkware-libs/sequencer PR #13859.
//
// Two distinct workflows for the same head SHA each define a job named
// "benchmarking":
//   - Committer-CI (workflow_id=1, suite=100): benchmarking is still in_progress.
//   - Blockifier-CI (workflow_id=2, suite=200): benchmarking is completed=success.
//
// Both workflows fire on the same PR event, and Blockifier-CI's suite ID happens
// to be higher than Committer-CI's. The dedup-by-name logic in listGhaStatuses
// keeps only the run with the highest suite ID, so the in_progress Committer-CI
// benchmarking is silently dropped and the gatekeeper concludes success.
//
// On real PR #13859 this caused the gatekeeper to report success at 14:48:40Z
// while Committer-CI's benchmarking was still running; that benchmarking then
// failed at 14:54:01Z (caught by the legacy upsidr/merge-gatekeeper, missed by
// the new starkware-libs/merge-gatekeeper).
//
// Correct behavior: both benchmarking jobs must remain visible so the gatekeeper
// waits for Committer-CI's run. This test asserts a result containing two
// distinct entries — one pending, one success — and therefore FAILS on the
// current code (which produces a single success entry).
func Test_PR13859_NameCollisionAcrossWorkflows(t *testing.T) {
	committerCISuite := int64(100)
	blockifierCISuite := int64(200)
	committerCIWorkflow := int64(1)
	blockifierCIWorkflow := int64(2)
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
						Name:       stringPtr("benchmarking"),
						Status:     stringPtr("in_progress"),
						CheckSuite: &github.CheckSuite{ID: &committerCISuite},
					},
					{
						ID:         int64Ptr(2),
						Name:       stringPtr("benchmarking"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &blockifierCISuite},
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
						WorkflowID:   &committerCIWorkflow,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &committerCISuite,
						Status:       stringPtr("in_progress"),
					},
					{
						ID:           int64Ptr(11),
						WorkflowID:   &blockifierCIWorkflow,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &blockifierCISuite,
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

	got, err := sv.listGhaStatuses(context.Background())
	if err != nil {
		t.Fatalf("listGhaStatuses returned unexpected error: %v", err)
	}

	// We don't prescribe the exact disambiguation key the fix should adopt
	// (e.g. "benchmarking [Committer-CI]" vs keying by workflow ID under the
	// same name). The contract that must hold: at least one entry must be in
	// pendingState, because Committer-CI's benchmarking is still running. A
	// single successState entry — which is what the buggy code returns — would
	// let the gatekeeper greenlight a PR while real CI is unfinished.
	pendingCount := 0
	for _, status := range got {
		if status.State == pendingState {
			pendingCount++
		}
	}
	if pendingCount == 0 {
		// Helpful diagnostic: print what we got to make the bug obvious.
		sort.Slice(got, func(i, j int) bool { return got[i].Job < got[j].Job })
		t.Errorf("expected at least one pendingState entry (Committer-CI's benchmarking is in_progress); got %d entries, none pending: %+v",
			len(got), formatStatuses(got))
	}
}

func formatStatuses(statuses []*ghaStatus) []string {
	out := make([]string, len(statuses))
	for i, s := range statuses {
		out[i] = s.Job + "=" + string(s.State)
	}
	return out
}
