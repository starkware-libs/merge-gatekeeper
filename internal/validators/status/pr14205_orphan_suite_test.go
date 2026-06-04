package status

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Test_PR14205_OrphanSuiteListingGap reproduces the May 26 false failure on
// sequencer PR #14205 (gatekeeper job 77873109158, reported by Dori).
//
// Two pull_request batches were triggered one second apart at the same SHA;
// the first batch was cancelled by concurrency within seconds. The gatekeeper
// polled ~35s later and GitHub's workflow-runs-by-head_sha listing transiently
// omitted the just-cancelled batch (proven by the gatekeeper's own log: the
// cancelled jobs were labeled "[workflow:0]", the fallback used when a check
// suite has no entry in the listing, while live jobs of the same workflow
// correctly showed "[Main-CI-Flow]"; querying the same endpoint later returns
// all runs).
//
// With the suite absent from the listing, both protective layers failed:
//   - it was never marked superseded, so its cancelled check runs skipped the
//     pending-conversion, and
//   - dedup keyed it as workflowID 0, so it didn't collapse with the live
//     instances.
//
// The orphaned cancelled jobs then turned the gatekeeper red on the spot.
//
// Post-fix expectations: a github-actions check run whose suite is missing
// from the listing means the listing is inconsistent (every Actions check run
// belongs to a workflow run), so its terminal conclusion is converted to
// pending and re-evaluated on a later poll instead of failing on half-synced
// state.
func Test_PR14205_OrphanSuiteListingGap(t *testing.T) {
	cancelledSuite := int64(70804636280) // missing from the workflow-runs listing
	liveSuite := int64(70804638177)
	mainCIFlowWorkflow := int64(12345)
	actionsApp := &github.App{Slug: stringPtr("github-actions")}
	totalRuns := 1

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
					{
						ID:         int64Ptr(77873206844),
						Name:       stringPtr("run-tests"),
						Status:     stringPtr("in_progress"),
						CheckSuite: &github.CheckSuite{ID: &liveSuite},
						App:        actionsApp,
					},
				},
			}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			// The cancelled batch's run is absent: only the live run is listed.
			return &github.WorkflowRuns{
				TotalCount: &totalRuns,
				WorkflowRuns: []*github.WorkflowRun{
					{
						ID:           int64Ptr(26451451128),
						Name:         stringPtr("Main-CI-Flow"),
						WorkflowID:   &mainCIFlowWorkflow,
						RunNumber:    intPtr(137976),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &liveSuite,
						Status:       stringPtr("in_progress"),
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
		{Job: "run-tests [Main-CI-Flow]", State: pendingState},
		{Job: "run-tests [workflow:0]", State: pendingState},
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Job < got[j].Job })
	if !reflect.DeepEqual(got, want) {
		t.Errorf("listGhaStatuses() = %v, want %v", formatStatuses(got), formatStatuses(want))
	}

	// The gatekeeper must keep waiting (a later poll sees a consistent listing),
	// not fail. Pre-fix, Validate errors with "run-tests [workflow:0]" cancelled.
	gotStatus, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() should wait for a consistent listing instead of failing; got error: %v", err)
	}
	if gotStatus.IsSuccess() {
		t.Errorf("Validate() should report incomplete validation while the orphan suite is unresolved")
	}
}

// Test_PR14205_ThirdPartyUnknownSuiteStillCounts pins the boundary of the
// orphan-suite leniency: check runs from third-party apps never have workflow
// runs, so an unknown suite is normal for them and their terminal conclusions
// must keep counting.
func Test_PR14205_ThirdPartyUnknownSuiteStillCounts(t *testing.T) {
	thirdPartySuite := int64(70804640000)
	liveSuite := int64(70804638177)
	mainCIFlowWorkflow := int64(12345)
	totalRuns := 1

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						ID:         int64Ptr(77873300000),
						Name:       stringPtr("third-party-scan"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr("failure"),
						CheckSuite: &github.CheckSuite{ID: &thirdPartySuite},
						App:        &github.App{Slug: stringPtr("some-security-scanner")},
					},
					{
						ID:         int64Ptr(77873206844),
						Name:       stringPtr("run-tests"),
						Status:     stringPtr("in_progress"),
						CheckSuite: &github.CheckSuite{ID: &liveSuite},
						App:        &github.App{Slug: stringPtr("github-actions")},
					},
				},
			}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			return &github.WorkflowRuns{
				TotalCount: &totalRuns,
				WorkflowRuns: []*github.WorkflowRun{
					{
						ID:           int64Ptr(26451451128),
						Name:         stringPtr("Main-CI-Flow"),
						WorkflowID:   &mainCIFlowWorkflow,
						RunNumber:    intPtr(137976),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &liveSuite,
						Status:       stringPtr("in_progress"),
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

	_, err := sv.Validate(context.Background())
	if err == nil {
		t.Fatalf("Validate() should fail on the third-party failure; unknown suites are only pardoned for github-actions check runs")
	}
	if !containsString(err.Error(), "third-party-scan") {
		t.Errorf("Validate() error should mention 'third-party-scan'; got: %v", err)
	}
}
