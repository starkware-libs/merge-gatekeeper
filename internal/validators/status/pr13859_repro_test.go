package status

import (
	"context"
	"reflect"
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
// to be higher than Committer-CI's. The pre-fix dedup-by-name logic kept only
// the run with the highest suite ID, dropping the in_progress Committer-CI run
// silently and concluding success while Committer-CI's benchmarking was still
// running (it later failed at 14:54:01Z on the real PR).
//
// Post-fix expectations:
//   - Both benchmarking entries are present, keyed by (workflow_id, name).
//   - Display names are disambiguated to "benchmarking [Committer-CI]" and
//     "benchmarking [Blockifier-CI]".
//   - Committer-CI's entry is pendingState; Blockifier-CI's is successState.
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
						Name:         stringPtr("Committer-CI"),
						WorkflowID:   &committerCIWorkflow,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &committerCISuite,
						Status:       stringPtr("in_progress"),
					},
					{
						ID:           int64Ptr(11),
						Name:         stringPtr("Blockifier-CI"),
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

	want := []*ghaStatus{
		{Job: "benchmarking [Blockifier-CI]", State: successState},
		{Job: "benchmarking [Committer-CI]", State: pendingState},
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Job < got[j].Job })
	if !reflect.DeepEqual(got, want) {
		t.Errorf("listGhaStatuses() = %v, want %v", formatStatuses(got), formatStatuses(want))
	}
}

func formatStatuses(statuses []*ghaStatus) []string {
	out := make([]string, len(statuses))
	for i, s := range statuses {
		out[i] = s.Job + "=" + string(s.State)
	}
	return out
}
