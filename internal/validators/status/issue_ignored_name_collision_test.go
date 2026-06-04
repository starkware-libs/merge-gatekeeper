package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: ignored-job matching in Validate compares the configured --ignored
// names against the *display* name of each tracked job. When an ignored job
// name exists in two workflows at the same SHA, the cross-workflow collision
// logic renames both instances to "name [Workflow-Name]", and neither renamed
// instance matches the configured ignore anymore. A failure in an explicitly
// ignored job then turns the gatekeeper red (or, in the pending variant,
// makes it wait for a job the user said not to wait for).
//
// This is realistic in the sequencer repo: jobs like "optimize_ci" exist in
// many workflows simultaneously (the gatekeeper's own production logs show
// "optimize_ci [<workflow>]" disambiguated entries), so ANY ignored name that
// appears in more than one workflow silently loses its ignore.
//
// Expected correct behavior: a job whose RAW name is in the ignored list is
// ignored regardless of how its display name is disambiguated.
func Test_Issue_IgnoredJobNameCollisionLosesIgnore(t *testing.T) {
	suiteA := int64(100)
	suiteB := int64(200)
	wfA := int64(1)
	wfB := int64(2)
	totalRuns := 2

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{
				CheckRuns: []*github.CheckRun{
					{
						// The ignored job, failing in workflow A. The user
						// explicitly asked the gatekeeper not to care.
						ID:         int64Ptr(1),
						Name:       stringPtr("flaky-benchmark"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr("failure"),
						CheckSuite: &github.CheckSuite{ID: &suiteA},
					},
					{
						// The same job name in workflow B — this is what
						// triggers the collision disambiguation.
						ID:         int64Ptr(2),
						Name:       stringPtr("flaky-benchmark"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &suiteB},
					},
					{
						// The only real job: already succeeded.
						ID:         int64Ptr(3),
						Name:       stringPtr("build"),
						Status:     stringPtr(checkRunCompletedStatus),
						Conclusion: stringPtr(checkRunSuccessConclusion),
						CheckSuite: &github.CheckSuite{ID: &suiteA},
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
						Name:         stringPtr("Committer-CI"),
						WorkflowID:   &wfA,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &suiteA,
						Status:       stringPtr("completed"),
						Conclusion:   stringPtr("failure"),
					},
					{
						ID:           int64Ptr(11),
						Name:         stringPtr("Blockifier-CI"),
						WorkflowID:   &wfB,
						RunNumber:    intPtr(1),
						RunAttempt:   intPtr(1),
						CheckSuiteID: &suiteB,
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
		ignoredJobs: []string{"flaky-benchmark"},
	}

	gotStatus, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() must keep ignoring %q even when its name collides across "+
			"workflows; instead the failure of the disambiguated instance turned the "+
			"gatekeeper red: %v", "flaky-benchmark", err)
	}
	if !gotStatus.IsSuccess() {
		t.Errorf("Validate() should succeed: the only non-ignored job passed. Status detail:\n%s",
			gotStatus.Detail())
	}
}
