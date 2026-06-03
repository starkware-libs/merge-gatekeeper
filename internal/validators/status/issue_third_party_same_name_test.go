package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: the PR #13859/#13862 fix keys dedup by (workflow_id, name) so that
// same-named jobs in different workflows stay independent. But check runs
// from third-party check apps have no workflow run, so workflowInfoFor
// returns workflowID=0 for ALL of them — every third-party app shares the
// same dedup namespace. Two different apps posting a check run with the same
// name (two scanners both posting "security-scan", an org-level and a
// repo-level install of the same app, SonarQube+SonarCloud "Quality Gate",
// ...) collapse into one tracked entry, and the suite-ID tiebreaker silently
// throws one result away.
//
// This is the exact masking failure the fork was created to fix — a success
// from app B hides a failure from app A — just moved from GitHub Actions
// workflows to check apps. The data to distinguish them is on the check run
// (App.ID / CheckSuite.ID); it just isn't part of the key.
//
// Expected correct behavior: check runs from different apps (different check
// suites, neither owned by a workflow) must be tracked independently, so the
// failure turns the gatekeeper red.
func Test_Issue_ThirdPartyAppsWithSameCheckNameCollapse(t *testing.T) {
	scannerASuite := int64(300)
	scannerBSuite := int64(400) // higher suite ID — wins the tiebreak
	actionsSuite := int64(100)
	wfA := int64(1)
	totalRuns := 1

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						// Scanner A: FAILED.
						ID:         int64Ptr(1),
						Name:       stringPtr("security-scan"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr("failure"),
						CheckSuite: &github.CheckSuite{ID: &scannerASuite},
						App:        &github.App{ID: int64Ptr(901), Slug: stringPtr("scanner-a")},
					},
					{
						// Scanner B: same check name, passed, higher suite ID.
						ID:         int64Ptr(2),
						Name:       stringPtr("security-scan"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &scannerBSuite},
						App:        &github.App{ID: int64Ptr(902), Slug: stringPtr("scanner-b")},
					},
					{
						// A normal Actions job so the workflow listing is in play.
						ID:         int64Ptr(3),
						Name:       stringPtr("build"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &actionsSuite},
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
						ID:           int64Ptr(10),
						Name:         stringPtr("CI"),
						WorkflowID:   &wfA,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &actionsSuite,
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

	gotStatus, err := sv.Validate(context.Background())
	if err == nil {
		t.Fatalf("FALSE GREEN: Validate() should fail on scanner-a's \"security-scan\" "+
			"failure; instead scanner-b's same-named success collapsed onto it via the "+
			"shared workflowID=0 dedup key and masked it. Status detail:\n%s",
			gotStatus.Detail())
	}
	if !containsString(err.Error(), "security-scan") {
		t.Errorf("Validate() error should mention 'security-scan'; got: %v", err)
	}
}
