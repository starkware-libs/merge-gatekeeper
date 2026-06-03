package status

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Test_PR14106_RerunStaleFailure reproduces the May 26 false failure on
// sequencer PR #14106 (gatekeeper job 77818462417, reported by Yoav): the
// gatekeeper went red on run-integration-tests 35 seconds after "re-run
// failed jobs", while the re-run later succeeded.
//
// Re-run attempts reuse the check suite, and the gatekeeper lists check runs
// with filter=all, so after a re-run the previous attempt's failure conclusion
// stays visible until the new attempt recreates the check run (a ~5-minute
// window on the real PR: attempt 2 started 06:19:38, the fresh check run only
// appeared 06:24:48). The supersede filter can't help — a suite cannot
// supersede itself — and dedup has nothing newer to pick. The gatekeeper
// failed fast on the stale conclusion at 06:20:14; the re-run succeeded at
// 06:45:37 and nothing re-colored the gatekeeper.
//
// On the real PR (suite 70644822176, workflow run 26401196357):
//   - attempt 1 run-integration-tests (id 77713670787): failure, completed
//     May 25 13:03:59
//   - attempt 2 started May 26 06:19:38; its run-integration-tests
//     (id 77819028940) succeeded at 06:45:37
//
// Post-fix expectation: while a suite's latest run is executing attempt > 1,
// terminal conclusions that predate the attempt's start are treated as
// pending — the attempt may be about to replace them.
func Test_PR14106_RerunStaleFailure(t *testing.T) {
	suite := int64(70644822176)
	mainCIFlowWorkflow := int64(54321)
	attempt2Start := time.Date(2026, 5, 26, 6, 19, 38, 0, time.UTC)

	staleFailure := &github.CheckRun{
		ID:          int64Ptr(77713670787),
		Name:        stringPtr("run-integration-tests"),
		Status:      stringPtr(checkRunCompletedStatus),
		Conclusion:  stringPtr("failure"),
		CompletedAt: timestampPtr(time.Date(2026, 5, 25, 13, 3, 59, 0, time.UTC)),
		CheckSuite:  &github.CheckSuite{ID: &suite},
	}
	rerunSuccess := &github.CheckRun{
		ID:          int64Ptr(77819028940),
		Name:        stringPtr("run-integration-tests"),
		Status:      stringPtr(checkRunCompletedStatus),
		Conclusion:  stringPtr(checkRunSuccessConclusion),
		CompletedAt: timestampPtr(time.Date(2026, 5, 26, 6, 45, 37, 0, time.UTC)),
		CheckSuite:  &github.CheckSuite{ID: &suite},
	}
	attempt2Run := func(status string, conclusion *string) *github.WorkflowRun {
		return &github.WorkflowRun{
			ID:           int64Ptr(26401196357),
			Name:         stringPtr("Main-CI-Flow"),
			WorkflowID:   &mainCIFlowWorkflow,
			RunNumber:    intPtr(137800),
			RunAttempt:   intPtr(2),
			RunStartedAt: timestampPtr(attempt2Start),
			CheckSuiteID: &suite,
			Status:       &status,
			Conclusion:   conclusion,
		}
	}

	tests := map[string]struct {
		checkRuns   []*github.CheckRun
		workflowRun *github.WorkflowRun
		want        []*ghaStatus
		wantErrJob  string // non-empty: Validate must fail citing this job
	}{
		// The race window: attempt 2 is running and hasn't recreated the check
		// run yet. Pre-fix, the stale failure turns the gatekeeper red here.
		"attempt in progress, stale failure pending": {
			checkRuns:   []*github.CheckRun{staleFailure},
			workflowRun: attempt2Run("in_progress", nil),
			want:        []*ghaStatus{{Job: "run-integration-tests", State: pendingState}},
		},
		// The re-run recreated the check run and succeeded: the newer instance
		// must win over the stale failure.
		"attempt completed, job re-ran and succeeded": {
			checkRuns:   []*github.CheckRun{staleFailure, rerunSuccess},
			workflowRun: attempt2Run("completed", stringPtr("success")),
			want:        []*ghaStatus{{Job: "run-integration-tests", State: successState}},
		},
		// The re-run finished without touching this job (e.g. a single-job
		// re-run elsewhere in the workflow): the old failure is final and must
		// still count — the leniency is bounded to the attempt's execution.
		"attempt completed, job not re-run, failure stands": {
			checkRuns:   []*github.CheckRun{staleFailure},
			workflowRun: attempt2Run("completed", stringPtr("failure")),
			want:        []*ghaStatus{{Job: "run-integration-tests", State: errorState}},
			wantErrJob:  "run-integration-tests",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			totalRuns := 1
			client := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{CheckRuns: tt.checkRuns}, nil, nil
				},
				ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
					return &github.WorkflowRuns{
						TotalCount:   &totalRuns,
						WorkflowRuns: []*github.WorkflowRun{tt.workflowRun},
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
			sort.Slice(got, func(i, j int) bool { return got[i].Job < got[j].Job })
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("listGhaStatuses() = %v, want %v", formatStatuses(got), formatStatuses(tt.want))
			}

			gotStatus, err := sv.Validate(context.Background())
			if tt.wantErrJob != "" {
				if err == nil {
					t.Fatalf("Validate() should have failed citing %q; got nil error and status %+v", tt.wantErrJob, gotStatus)
				}
				if !containsString(err.Error(), tt.wantErrJob) {
					t.Errorf("Validate() error should mention %q; got: %v", tt.wantErrJob, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() returned unexpected error: %v", err)
			}
		})
	}
}
