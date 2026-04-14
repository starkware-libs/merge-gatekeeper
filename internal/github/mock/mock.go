package mock

import (
	"context"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
)

type Client struct {
	GetCombinedStatusFunc            func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error)
	ListCheckRunsForRefFunc          func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error)
	ListRepositoryWorkflowRunsFunc   func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error)
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

var (
	_ github.Client = &Client{}
)
