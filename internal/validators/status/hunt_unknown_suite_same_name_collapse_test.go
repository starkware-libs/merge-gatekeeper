package status

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Test_Hunt_UnknownSuiteSameNameCollapse probes the dedup key used for
// github-actions check runs whose suites are missing from the workflow-runs
// listing (the PR#14205 inconsistency window): such runs are keyed by
// (workflowID=0, appID, name), so same-named check runs from two DIFFERENT
// unknown suites collapse into one entry, and preferOverExisting picks the
// higher suite ID. A settled success in one suite can then swallow another
// suite's failed/unresolved run — the PR#13859 masking class (two workflows
// defining the same job name), resurrected exactly while the listing is at
// its most inconsistent. A green poll exits the gatekeeper immediately, so
// this is a real fail-open window, not just a delayed report.
//
// Contract: when the listing knows neither suite, the gatekeeper cannot tell
// re-runs of one job apart from two distinct workflows' jobs — it must stay
// fail-closed (keep waiting) rather than green-light on the half-synced
// state. Distinct unknown github-actions suites must stay independently
// tracked until a consistent listing resolves them.
func Test_Hunt_UnknownSuiteSameNameCollapse(t *testing.T) {
	failedSuite := int64(70804636280)  // lower suite ID, completed/failure
	successSuite := int64(70804638177) // higher suite ID, completed/success
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
						Name:       stringPtr("benchmarking"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr("failure"),
						CheckSuite: &github.CheckSuite{ID: &failedSuite},
						App:        actionsApp,
					},
					{
						ID:         int64Ptr(77873206844),
						Name:       stringPtr("benchmarking"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &successSuite},
						App:        actionsApp,
					},
				},
			}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			// Neither suite is in the listing.
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

	// Both suites must stay tracked: the success as success, the orphaned
	// failure softened to pending by the orphan rule (its suite is unknown).
	want := []*ghaStatus{
		{Job: "benchmarking", State: pendingState},
		{Job: "benchmarking", State: successState},
	}
	sortStatuses := func(s []*ghaStatus) {
		sort.Slice(s, func(i, j int) bool {
			if s[i].Job != s[j].Job {
				return s[i].Job < s[j].Job
			}
			return s[i].State < s[j].State
		})
	}
	sortStatuses(got)
	sortStatuses(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("listGhaStatuses() = %v, want %v", formatStatuses(got), formatStatuses(want))
	}

	// The gatekeeper must NOT go green while one of the same-named unknown
	// suites is unresolved — a later poll sees a consistent listing and keys
	// the runs by their real workflows.
	gotStatus, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() should wait for a consistent listing instead of failing; got error: %v", err)
	}
	if gotStatus.IsSuccess() {
		t.Errorf("Validate() must not green-light while a same-named check run from another unknown suite is unresolved")
	}
}
