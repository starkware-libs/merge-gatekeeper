package status

import (
	"context"
	"strings"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: getIncompleteJobs subtracts completed/failed/cancelled jobs from
// totalJobs via a name SET, but totalJobs legitimately contains the same
// display name twice — `on: [push, pull_request]` runs one workflow's job
// "build" twice at the same SHA, the dedup keys differ by event, and the
// display name is identical (only cross-WORKFLOW collisions get suffixed).
// Once one event's "build" succeeds, the set lookup also swallows the other
// event's still-pending "build": Detail() reports "1 out of 2" alongside
// "Incompleted job count: 0" and an empty incomplete list, contradicting the
// (correctly) still-closed gate and hiding from the user which job is being
// waited on.
//
// Expected correct behavior: counts are consistent — the pending duplicate
// shows up as 1 incomplete job.
func Test_Hunt_IncompleteCountWithDuplicateDisplayNames(t *testing.T) {
	wfID := int64(1)
	suitePush := int64(100)
	suitePR := int64(200)
	totalRuns := 2

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						// The push event's "build": done.
						ID:         int64Ptr(1),
						Name:       stringPtr("build"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &suitePush},
					},
					{
						// The pull_request event's "build": still running.
						ID:         int64Ptr(2),
						Name:       stringPtr("build"),
						Status:     stringPtr("in_progress"),
						CheckSuite: &github.CheckSuite{ID: &suitePR},
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
						Name:         stringPtr("CI"),
						WorkflowID:   &wfID,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &suitePush,
						Event:        stringPtr("push"),
						Status:       stringPtr(checkRunCompletedStatus),
						Conclusion:   stringPtr(checkRunSuccessConclusion),
					},
					{
						ID:           int64Ptr(11),
						Name:         stringPtr("CI"),
						WorkflowID:   &wfID,
						RunNumber:    intPtr(2),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &suitePR,
						Event:        stringPtr("pull_request"),
						Status:       stringPtr("in_progress"),
					},
				},
			}, nil, nil
		},
		ListWorkflowJobsFunc: func(ctx context.Context, owner, repo string, runID int64, opts *github.ListWorkflowJobsOptions) (*github.Jobs, *github.Response, error) {
			totalJobs := 1
			return &github.Jobs{
				TotalCount: &totalJobs,
				Jobs: []*github.WorkflowJob{
					{ID: int64Ptr(1), Name: stringPtr("build")},
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

	st, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() returned an unexpected error: %v", err)
	}
	if st.IsSuccess() {
		t.Fatalf("Validate() must not succeed while the pull_request event's \"build\" is in progress. Detail:\n%s", st.Detail())
	}
	detail := st.Detail()
	if !strings.Contains(detail, "Incompleted job count: 1") {
		t.Errorf("Detail() must count the still-pending duplicate-named job as incomplete "+
			"(want \"Incompleted job count: 1\"); got:\n%s", detail)
	}
}
