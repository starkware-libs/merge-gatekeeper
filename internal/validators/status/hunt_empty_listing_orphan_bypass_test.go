package status

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Test_Hunt_EmptyListingBypassesOrphanSoftening probes the n=all case of the
// PR#14205 listing inconsistency: the workflow-runs-by-head_sha listing can
// transiently omit just-created or just-cancelled runs. The orphan-suite rule
// covers a github-actions check run whose suite is missing from a NON-EMPTY
// listing (its terminal conclusion is converted to pending and re-polled),
// but filterStaleCheckRuns early-returns when the listing has NO runs at all
// — the most inconsistent listing possible gets no protection, and a
// transiently-orphaned cancelled check run turns the gatekeeper red on the
// spot instead of waiting for a consistent listing.
//
// Contract: every Actions check run belongs to a workflow run, so a
// github-actions check run with an empty workflow-runs listing means the
// listing is inconsistent — exactly like the partial case, the gatekeeper
// must keep waiting, not fail.
func Test_Hunt_EmptyListingBypassesOrphanSoftening(t *testing.T) {
	cancelledSuite := int64(70804636280)
	actionsApp := &github.App{Slug: stringPtr("github-actions")}

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						ID:         int64Ptr(77873106844),
						Name:       stringPtr("run-tests"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunCancelledConclusion),
						CheckSuite: &github.CheckSuite{ID: &cancelledSuite},
						App:        actionsApp,
					},
				},
			}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			// The listing is transiently EMPTY: not even the run that owns the
			// cancelled suite is indexed yet.
			return &github.WorkflowRuns{}, nil, nil
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
		{Job: "run-tests", State: pendingState},
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Job < got[j].Job })
	if !reflect.DeepEqual(got, want) {
		t.Errorf("listGhaStatuses() = %v, want %v", formatStatuses(got), formatStatuses(want))
	}

	// The gatekeeper must keep waiting (a later poll sees a consistent
	// listing), not fail — same expectation as the partial-listing orphan
	// case in Test_PR14205_OrphanSuiteListingGap.
	gotStatus, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() should wait for a consistent listing instead of failing; got error: %v", err)
	}
	if gotStatus.IsSuccess() {
		t.Errorf("Validate() should report incomplete validation while the listing is inconsistent")
	}
}
