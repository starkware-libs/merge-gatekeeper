package status

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Test_PR13862_NameCollisionMasksFailure reproduces a second occurrence of the
// cross-workflow name-collision bug, this time on starkware-libs/sequencer
// PR #13862.
//
// PR #13859 (covered by Test_PR13859_NameCollisionAcrossWorkflows) showed the
// in_progress variant: the colliding entry was still running when the gatekeeper
// reported success. PR #13862 is the worse variant — the colliding Committer-CI
// "benchmarking" job had already FAILED at 21:06:40Z, and both gatekeepers still
// reported success ~1.5 minutes later. The failure was not just unobserved, it
// was actively masked by the dedup keeping the higher-suite-ID Blockifier-CI
// success.
//
// On the real PR:
//   - Committer-CI/benchmarking (suite 66605356350, run id 73471530173): failure
//   - Blockifier-CI/benchmarking (suite 66605356525, run id 73471533904): success
//
// The Blockifier-CI suite ID is higher, so name-only dedup picked it. (Notably,
// its run ID is also higher, which is why the legacy upsidr gatekeeper missed
// this PR too — its run-ID-based tiebreaker landed on the same wrong winner.
// The legacy gatekeeper happened to land on the right winner for PR #13859 by
// coincidence; the ordering between two workflows' run IDs is not stable.)
//
// Post-fix expectations:
//   - Both benchmarking entries kept, keyed by (workflow_id, name).
//   - Display names disambiguated to "benchmarking [Committer-CI]" and
//     "benchmarking [Blockifier-CI]".
//   - Committer-CI's entry is errorState (conclusion=failure routes through the
//     default branch of the conclusion switch).
//   - Blockifier-CI's entry is successState.
//
// Regression: pre-fix, listGhaStatuses returns a single
//
//	{Job: "benchmarking", State: successState}
//
// entry, hiding the failure. Validate would then mark the gatekeeper as passing.
func Test_PR13862_NameCollisionMasksFailure(t *testing.T) {
	committerCISuite := int64(66605356350)
	blockifierCISuite := int64(66605356525)
	committerCIWorkflow := int64(108994160)  // Committer-CI workflow ID
	blockifierCIWorkflow := int64(106557720) // Blockifier-CI workflow ID
	totalRuns := 2

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						ID:         int64Ptr(73471530173),
						Name:       stringPtr("benchmarking"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr("failure"),
						CheckSuite: &github.CheckSuite{ID: &committerCISuite},
					},
					{
						ID:         int64Ptr(73471533904),
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
						ID:           int64Ptr(25076811696),
						Name:         stringPtr("Committer-CI"),
						WorkflowID:   &committerCIWorkflow,
						RunNumber:    intPtr(33952),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &committerCISuite,
						Status:       stringPtr("completed"),
						Conclusion:   stringPtr("failure"),
					},
					{
						ID:           int64Ptr(25076811751),
						Name:         stringPtr("Blockifier-CI"),
						WorkflowID:   &blockifierCIWorkflow,
						RunNumber:    intPtr(48595),
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
		{Job: "benchmarking [Committer-CI]", State: errorState},
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Job < got[j].Job })
	if !reflect.DeepEqual(got, want) {
		t.Errorf("listGhaStatuses() = %v, want %v", formatStatuses(got), formatStatuses(want))
	}

	// Also verify Validate marks the run as failed (not silently passing). On
	// pre-fix code, listGhaStatuses returns a single success entry and Validate
	// would happily report succeeded=true.
	gotStatus, err := sv.Validate(context.Background())
	if err == nil {
		t.Fatalf("Validate() should have returned an error citing the failed Committer-CI benchmarking job; got nil error and status %+v", gotStatus)
	}
	if !containsString(err.Error(), "benchmarking [Committer-CI]") {
		t.Errorf("Validate() error should mention 'benchmarking [Committer-CI]'; got: %v", err)
	}
}

func containsString(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
