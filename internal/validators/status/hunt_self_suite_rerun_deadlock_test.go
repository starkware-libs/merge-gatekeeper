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

// Test_Hunt_SelfSuiteRerunDeadlock reproduces the 2026-06-07 production hold
// on sequencer run 27087903294 (observed live, first hour of the v1.3.0
// deploy): "re-run failed jobs" on the gatekeeper's OWN workflow re-ran only
// the gatekeeper, so the suite was executing attempt 2 while sibling jobs
// that succeeded in attempt 1 (commitlint) were never going to re-run. The
// stale-attempt rule converted those settled successes to pending "until the
// attempt completes" — but the attempt completes only when the gatekeeper
// itself exits, and the gatekeeper was waiting on the pended sibling:
// a self-referential deadlock that held the gate to its own timeout.
//
// Contract asserted: in the suite that contains the gatekeeper's own check
// run, a settled sibling success that predates the executing attempt is
// FINAL (re-runs recreate check runs for the jobs they actually run; a
// success that was not recreated was not re-run). Stale FAILURES in the same
// suite must still pend — a failed sibling is by definition part of any
// re-run ("re-run failed jobs" re-runs exactly those) — and other suites
// keep the conservative pending for successes too (their runs complete
// independently, so it is a bounded wait, not a deadlock).
func Test_Hunt_SelfSuiteRerunDeadlock(t *testing.T) {
	selfSuite := int64(72756429781)
	otherSuite := int64(200)
	mainWorkflow := int64(999)
	otherWorkflow := int64(888)
	attempt2Start := time.Date(2026, 6, 7, 9, 23, 31, 0, time.UTC)
	attempt1Done := time.Date(2026, 6, 7, 8, 54, 0, 0, time.UTC)

	run := func(id, workflowID, suite int64, name string, attempt int) *github.WorkflowRun {
		status := "in_progress"
		return &github.WorkflowRun{
			ID:           int64Ptr(id),
			Name:         stringPtr(name),
			WorkflowID:   &workflowID,
			RunNumber:    intPtr(100),
			RunAttempt:   intPtr(attempt),
			RunStartedAt: timestampPtr(attempt2Start),
			CheckSuiteID: &suite,
			Status:       &status,
		}
	}
	checkRun := func(id, suite int64, name, status string, conclusion *string, completedAt *time.Time) *github.CheckRun {
		cr := &github.CheckRun{
			ID:         int64Ptr(id),
			Name:       stringPtr(name),
			Status:     &status,
			CheckSuite: &github.CheckSuite{ID: &suite},
		}
		cr.Conclusion = conclusion
		if completedAt != nil {
			cr.CompletedAt = timestampPtr(*completedAt)
		}
		return cr
	}

	tests := map[string]struct {
		checkRuns    []*github.CheckRun
		workflowRuns []*github.WorkflowRun
		want         []*ghaStatus
		wantGreen    bool // also assert Validate() IsSuccess
	}{
		// The deadlock: own suite executing attempt 2 (the re-run is the
		// gatekeeper itself); commitlint succeeded in attempt 1 and will never
		// be recreated. Its success must be FINAL, making the gate green
		// (self-job is exempt while pending).
		"own suite: settled sibling success is final during own re-run attempt": {
			checkRuns: []*github.CheckRun{
				checkRun(1001, selfSuite, "commitlint", checkRunCompletedStatus, stringPtr(checkRunSuccessConclusion), &attempt1Done),
				checkRun(1002, selfSuite, "self-job", checkRunCompletedStatus, stringPtr("failure"), &attempt1Done),
				checkRun(1003, selfSuite, "self-job", "in_progress", nil, nil),
			},
			workflowRuns: []*github.WorkflowRun{run(1, mainWorkflow, selfSuite, "Main-CI-PR-Flow", 2)},
			want: []*ghaStatus{
				{Job: "commitlint", State: successState},
				{Job: "self-job", State: pendingState},
			},
			wantGreen: true,
		},
		// pr14106 protection unchanged inside the own suite: a stale FAILURE
		// is exactly what a re-run re-executes — it must pend, not red.
		"own suite: stale sibling failure still pends during own re-run attempt": {
			checkRuns: []*github.CheckRun{
				checkRun(1001, selfSuite, "commitlint", checkRunCompletedStatus, stringPtr("failure"), &attempt1Done),
				checkRun(1003, selfSuite, "self-job", "in_progress", nil, nil),
			},
			workflowRuns: []*github.WorkflowRun{run(1, mainWorkflow, selfSuite, "Main-CI-PR-Flow", 2)},
			want: []*ghaStatus{
				{Job: "commitlint", State: pendingState},
				{Job: "self-job", State: pendingState},
			},
			wantGreen: false,
		},
		// Suites NOT containing the gatekeeper keep the conservative rule:
		// their runs complete on their own, so pending a pre-attempt success
		// is a bounded wait ("Re-run all jobs" re-executes successes too).
		"other suite: settled success still pends during its re-run attempt": {
			checkRuns: []*github.CheckRun{
				checkRun(2001, otherSuite, "build", checkRunCompletedStatus, stringPtr(checkRunSuccessConclusion), &attempt1Done),
			},
			workflowRuns: []*github.WorkflowRun{run(2, otherWorkflow, otherSuite, "Other-CI", 2)},
			want: []*ghaStatus{
				{Job: "build", State: pendingState},
			},
			wantGreen: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			client := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{CheckRuns: tt.checkRuns}, nil, nil
				},
				ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
					total := len(tt.workflowRuns)
					return &github.WorkflowRuns{TotalCount: &total, WorkflowRuns: tt.workflowRuns}, nil, nil
				},
			}

			sv := &statusValidator{
				client:      client,
				owner:       "test-owner",
				repo:        "test-repo",
				ref:         fullSHARef,
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

			st, err := sv.Validate(context.Background())
			if err != nil {
				t.Fatalf("Validate() returned unexpected error: %v", err)
			}
			if st.IsSuccess() != tt.wantGreen {
				t.Errorf("Validate().IsSuccess() = %v, want %v", st.IsSuccess(), tt.wantGreen)
			}
		})
	}
}
