package mock

import (
	"context"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
)

type Client struct {
	GetCombinedStatusFunc            func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error)
	ListCheckRunsForRefFunc          func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error)
	ListRepositoryWorkflowRunsFunc   func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error)
	ListWorkflowJobsFunc             func(ctx context.Context, owner, repo string, runID int64, opts *github.ListWorkflowJobsOptions) (*github.Jobs, *github.Response, error)
}

func (c *Client) GetCombinedStatus(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
	return c.GetCombinedStatusFunc(ctx, owner, repo, ref, opts)
}

func (c *Client) ListCheckRunsForRef(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
	return c.ListCheckRunsForRefFunc(ctx, owner, repo, ref, opts)
}

func (c *Client) ListRepositoryWorkflowRuns(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
	if c.ListRepositoryWorkflowRunsFunc != nil {
		return c.ListRepositoryWorkflowRunsFunc(ctx, owner, repo, opts)
	}
	// Default: return empty result (no workflow runs). This preserves backward compatibility
	// with existing tests that don't set up this mock — the pre-filter becomes a no-op.
	return &github.WorkflowRuns{}, nil, nil
}

func (c *Client) ListWorkflowJobs(ctx context.Context, owner, repo string, runID int64, opts *github.ListWorkflowJobsOptions) (*github.Jobs, *github.Response, error) {
	if c.ListWorkflowJobsFunc != nil {
		return c.ListWorkflowJobsFunc(ctx, owner, repo, runID, opts)
	}
	// Default: return empty job list. The duplicate-name detection is a no-op when
	// no jobs are reported, preserving backward compatibility with existing tests.
	return &github.Jobs{}, nil, nil
}

var (
	_ github.Client = &Client{}
)
