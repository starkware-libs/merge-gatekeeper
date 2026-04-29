package status

import (
	"context"
	"strings"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Test_DetectDuplicateNamedJobs_Fails verifies that when a single workflow run
// has two jobs with the same display name (the case our (workflow_id, name)
// dedup cannot disambiguate from re-runs), Validate fails loudly with a clear
// message naming the offending workflow and job name.
func Test_DetectDuplicateNamedJobs_Fails(t *testing.T) {
	committerCIWorkflow := int64(108994160)
	totalRuns := 1

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			return &github.WorkflowRuns{
				TotalCount: &totalRuns,
				WorkflowRuns: []*github.WorkflowRun{
					{
						ID:         int64Ptr(25076811696),
						Name:       stringPtr("Committer-CI"),
						WorkflowID: &committerCIWorkflow,
						RunNumber:  intPtr(1),
					},
				},
			}, nil, nil
		},
		ListWorkflowJobsFunc: func(ctx context.Context, owner, repo string, runID int64, opts *github.ListWorkflowJobsOptions) (*github.Jobs, *github.Response, error) {
			// The workflow's YAML has two jobs both named "build". This is the
			// configuration we want to refuse to validate.
			total := 3
			return &github.Jobs{
				TotalCount: &total,
				Jobs: []*github.WorkflowJob{
					{ID: int64Ptr(1), Name: stringPtr("build")},
					{ID: int64Ptr(2), Name: stringPtr("build")},
					{ID: int64Ptr(3), Name: stringPtr("test")},
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
		t.Fatal("Validate() should have returned an error for duplicate-named jobs; got nil")
	}
	msg := err.Error()
	for _, want := range []string{"Committer-CI", `"build"`, "Rename"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Validate() error missing %q; got: %v", want, err)
		}
	}
}

// Test_DetectDuplicateNamedJobs_NoFalsePositives verifies the detector accepts
// matrix jobs (which auto-suffix with their matrix values, so each entry is
// uniquely named) and re-runs of failed jobs (which use filter=latest, so only
// the latest attempt's jobs appear — one entry per YAML key).
func Test_DetectDuplicateNamedJobs_NoFalsePositives(t *testing.T) {
	committerCIWorkflow := int64(108994160)
	totalRuns := 1

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			return &github.WorkflowRuns{
				TotalCount: &totalRuns,
				WorkflowRuns: []*github.WorkflowRun{
					{
						ID:         int64Ptr(123),
						Name:       stringPtr("Committer-CI"),
						WorkflowID: &committerCIWorkflow,
						RunNumber:  intPtr(1),
					},
				},
			}, nil, nil
		},
		ListWorkflowJobsFunc: func(ctx context.Context, owner, repo string, runID int64, opts *github.ListWorkflowJobsOptions) (*github.Jobs, *github.Response, error) {
			// Matrix expansions auto-suffix with the matrix combination, so
			// each child has a distinct name. A non-matrix sibling job named
			// "test" is also unique. No collisions.
			total := 4
			return &github.Jobs{
				TotalCount: &total,
				Jobs: []*github.WorkflowJob{
					{ID: int64Ptr(1), Name: stringPtr("build (sequencer, Dockerfile)")},
					{ID: int64Ptr(2), Name: stringPtr("build (recorder, recorder.Dockerfile)")},
					{ID: int64Ptr(3), Name: stringPtr("test")},
					{ID: int64Ptr(4), Name: stringPtr("benchmarking")},
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

	if err := sv.detectDuplicateNamedJobs(context.Background()); err != nil {
		t.Fatalf("detectDuplicateNamedJobs() returned an unexpected error on a non-duplicating workflow: %v", err)
	}
}

// Test_DetectDuplicateNamedJobs_CachedAfterSuccess verifies the check is run
// once per validator instance, not on every Validate() call.
func Test_DetectDuplicateNamedJobs_CachedAfterSuccess(t *testing.T) {
	listJobsCalls := 0

	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			return &github.ListCheckRunsResults{}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			wfID := int64(1)
			total := 1
			return &github.WorkflowRuns{
				TotalCount: &total,
				WorkflowRuns: []*github.WorkflowRun{
					{ID: int64Ptr(123), Name: stringPtr("CI"), WorkflowID: &wfID, RunNumber: intPtr(1)},
				},
			}, nil, nil
		},
		ListWorkflowJobsFunc: func(ctx context.Context, owner, repo string, runID int64, opts *github.ListWorkflowJobsOptions) (*github.Jobs, *github.Response, error) {
			listJobsCalls++
			total := 1
			return &github.Jobs{
				TotalCount: &total,
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

	for i := 0; i < 5; i++ {
		if _, err := sv.Validate(context.Background()); err != nil {
			t.Fatalf("Validate() iteration %d returned an unexpected error: %v", i, err)
		}
	}
	if listJobsCalls != 1 {
		t.Errorf("ListWorkflowJobs called %d times across 5 Validate() calls; expected 1 (cached)", listJobsCalls)
	}
}
