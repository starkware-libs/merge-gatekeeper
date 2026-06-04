package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: filterStaleCheckRuns returns early — BEFORE listing workflow runs —
// when no check run carries a check-suite ID ("Without any check suite there
// is nothing to correlate with workflow runs"). That rationale predates the
// queued-run placeholder (commit c67601f): the placeholder needs no check runs
// to correlate, only the workflow-runs listing itself. The early return
// switches it off in exactly the window it was built for.
//
// Concretely: validating a just-pushed branch/tag ref (a documented --ref
// form, and the deployment shape this fork added SHA resolution for), all
// triggered workflow runs can be queued with ZERO check runs materialized.
// The check-runs listing is empty → hasSuites is false → the workflow-runs
// listing is never fetched → no "[workflow starting]" placeholder → zero
// tracked jobs → Validate reports success. The PR/tag gates green while the
// whole CI pipeline is still queued.
//
// The sibling test issue_empty_suite_false_green_test.go cannot catch this:
// it always has one finished check run, so hasSuites is true and the early
// return is never taken.
//
// Expected correct behavior: a live workflow run with no check runs must hold
// the gatekeeper pending even when NO check runs exist for the ref at all.
func Test_Issue_NoCheckRunsAtAllSkipsQueuedWorkflowTracking(t *testing.T) {
	queuedSuite := int64(200)
	wfQueued := int64(2)
	totalRuns := 1

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			// The just-pushed window: no check runs exist for this ref yet.
			return &github.ListCheckRunsResults{}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			return &github.WorkflowRuns{
				TotalCount: &totalRuns,
				WorkflowRuns: []*github.WorkflowRun{
					{
						// Triggered and listed, but its jobs are not check runs yet.
						ID:           int64Ptr(11),
						Name:         stringPtr("Slow-Integration-Tests"),
						WorkflowID:   &wfQueued,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &queuedSuite,
						Status:       stringPtr("queued"),
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

	gotStatus, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() returned unexpected error: %v", err)
	}
	if gotStatus.IsSuccess() {
		t.Errorf("FALSE GREEN: Validate() reported success with zero check runs while workflow "+
			"\"Slow-Integration-Tests\" is queued — the empty check-runs listing made "+
			"filterStaleCheckRuns return before fetching workflow runs, so the queued run "+
			"placeholder was never synthesized. Status detail:\n%s", gotStatus.Detail())
	}
}
