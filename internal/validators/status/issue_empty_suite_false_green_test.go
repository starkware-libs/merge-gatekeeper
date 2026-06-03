package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: the gatekeeper builds its job list exclusively from check runs (and
// commit statuses). A workflow run that has been triggered but whose jobs
// have not yet been materialized as check runs — queued behind a concurrency
// group, waiting for runner capacity, or in the seconds-wide window right
// after the triggering event — contributes NOTHING to the tracked set. If
// every check run that does exist is green, Validate reports success and the
// PR becomes mergeable while a whole workflow's CI is still pending.
//
// The gatekeeper already fetches the workflow-runs listing (for the
// supersede/orphan/stale-attempt filters) and so holds the evidence — a
// non-completed, non-cancelled workflow run whose check suite has zero check
// runs — but never acts on it.
//
// This is the inverse of the orphan-suite fix (commit 05d4e47): that fix
// pardons check runs whose suite is missing from the workflow listing; this
// is a workflow run in the listing with no check runs against it yet.
//
// Expected correct behavior: a live workflow run with no check runs yet must
// hold the gatekeeper in "incomplete" (pending) state until its jobs appear.
func Test_Issue_QueuedWorkflowRunWithoutCheckRunsIsInvisible(t *testing.T) {
	doneSuite := int64(100)
	queuedSuite := int64(200) // run is queued; no check runs exist yet
	wfDone := int64(1)
	wfQueued := int64(2)
	totalRuns := 2

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						// The fast workflow already finished green.
						ID:         int64Ptr(1),
						Name:       stringPtr("lint"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &doneSuite},
					},
					// NOTE: no check runs at all for queuedSuite — the queued
					// run's jobs haven't been dispatched yet.
				},
			}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			return &github.WorkflowRuns{
				TotalCount: &totalRuns,
				WorkflowRuns: []*github.WorkflowRun{
					{
						ID:           int64Ptr(10),
						Name:         stringPtr("Fast-Lint"),
						WorkflowID:   &wfDone,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &doneSuite,
						Status:       stringPtr("completed"),
						Conclusion:   stringPtr("success"),
					},
					{
						// The heavyweight workflow: triggered, listed, but its
						// jobs are not check runs yet.
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
		t.Errorf("FALSE GREEN: Validate() reported success while workflow "+
			"\"Slow-Integration-Tests\" is queued with no check runs created yet — "+
			"the gatekeeper green-lights the PR before that workflow's CI starts. "+
			"Status detail:\n%s", gotStatus.Detail())
	}
}
