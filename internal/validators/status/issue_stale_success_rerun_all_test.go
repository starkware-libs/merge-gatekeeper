package status

import (
	"context"
	"testing"
	"time"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Regression test: the PR #14106 fix converts stale terminal conclusions to
// pending while their suite is executing a re-run attempt — but
// isSettledSuccess short-circuited BEFORE that check, so only
// failures/cancellations were softened. Stale SUCCESSES from the previous
// attempt kept counting during the attempt's execution.
//
// That's correct for "re-run failed jobs" (succeeded jobs don't re-run), but
// "Re-run all jobs" re-executes succeeded jobs too. During the same
// creation window that PR #14106 documented (attempt 2 started 06:19:38, its
// fresh check run only appeared 06:24:48 — a ~5-minute gap), a previously
// green job that is about to re-run is still reported green by its stale
// attempt-1 check run. If every visible conclusion is a stale success — e.g.
// the one failed job's fresh check run already completed green while a slow
// previously-green job hasn't been recreated yet — the gatekeeper goes green
// mid-re-run. If the re-run then fails that job, the PR was mergeable on a
// red commit: false green, the mirror image of the false red that was fixed.
//
// (Symmetry note: converting stale successes to pending while the attempt is
// in progress cannot delay "re-run failed jobs" — the gatekeeper is already
// waiting on the failed jobs' fresh check runs in that window, and once the
// run completes the conversion gate switches off and surviving old successes
// count again, exactly as today.)
//
// Expected correct behavior: while a suite's latest run is executing attempt
// > 1, ALL conclusions that predate the attempt's start are treated as
// pending — successes included.
func Test_Issue_StaleSuccessCountsDuringRerunAllWindow(t *testing.T) {
	suite := int64(500)
	wfID := int64(1)
	attempt2Start := time.Date(2026, 5, 26, 6, 19, 38, 0, time.UTC)
	totalRuns := 1

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						// The job that failed in attempt 1 was recreated
						// quickly in attempt 2 and already passed.
						ID:          int64Ptr(2),
						Name:        stringPtr("fast-job"),
						Status:      stringPtr(checkRunCompletedStatus),
						Conclusion:  stringPtr(checkRunSuccessConclusion),
						CompletedAt: timestampPtr(attempt2Start.Add(40 * time.Second)),
						CheckSuite:  &github.CheckSuite{ID: &suite},
					},
					{
						// The slow job passed in attempt 1 (completed a day
						// ago). "Re-run all jobs" WILL re-execute it, but its
						// fresh check run hasn't been created yet — this
						// stale success is all the API shows right now.
						ID:          int64Ptr(1),
						Name:        stringPtr("slow-job"),
						Status:      stringPtr(checkRunCompletedStatus),
						Conclusion:  stringPtr(checkRunSuccessConclusion),
						CompletedAt: timestampPtr(attempt2Start.Add(-24 * time.Hour)),
						CheckSuite:  &github.CheckSuite{ID: &suite},
					},
				},
			}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			return &github.WorkflowRuns{
				TotalCount: &totalRuns,
				WorkflowRuns: []*github.WorkflowRun{
					{
						// "Re-run all jobs": attempt 2, currently executing.
						ID:           int64Ptr(10),
						Name:         stringPtr("CI"),
						WorkflowID:   &wfID,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(2),
						RunStartedAt: timestampPtr(attempt2Start),
						CheckSuiteID: &suite,
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

	gotStatus, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() returned unexpected error: %v", err)
	}
	if gotStatus.IsSuccess() {
		t.Errorf("FALSE GREEN: Validate() reported success while attempt 2 is executing "+
			"\"Re-run all jobs\" — \"slow-job\"'s only visible conclusion predates the "+
			"attempt (stale attempt-1 success) and its re-execution hasn't been "+
			"materialized as a check run yet. Status detail:\n%s", gotStatus.Detail())
	}
}
