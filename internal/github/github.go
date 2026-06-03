package github

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/go-github/v84/github"
	"golang.org/x/oauth2"
)

const (
	defaultMaxRetries = 5
	defaultRetryDelay = 1 * time.Second
)

type (
	ListOptions    = github.ListOptions
	CombinedStatus = github.CombinedStatus
	RepoStatus     = github.RepoStatus
	Response       = github.Response
)

type (
	App                  = github.App
	CheckRun             = github.CheckRun
	CheckSuite           = github.CheckSuite
	ListCheckRunsOptions = github.ListCheckRunsOptions
	ListCheckRunsResults = github.ListCheckRunsResults
	Timestamp            = github.Timestamp
)

type (
	WorkflowRun              = github.WorkflowRun
	WorkflowRuns             = github.WorkflowRuns
	ListWorkflowRunsOptions  = github.ListWorkflowRunsOptions
	WorkflowJob              = github.WorkflowJob
	Jobs                     = github.Jobs
	ListWorkflowJobsOptions  = github.ListWorkflowJobsOptions
)

type Client interface {
	GetCombinedStatus(ctx context.Context, owner, repo, ref string, opts *ListOptions) (*CombinedStatus, *Response, error)
	ListCheckRunsForRef(ctx context.Context, owner, repo, ref string, opts *ListCheckRunsOptions) (*ListCheckRunsResults, *Response, error)
	ListRepositoryWorkflowRuns(ctx context.Context, owner, repo string, opts *ListWorkflowRunsOptions) (*WorkflowRuns, *Response, error)
	ListWorkflowJobs(ctx context.Context, owner, repo string, runID int64, opts *ListWorkflowJobsOptions) (*Jobs, *Response, error)
}

type client struct {
	ghc *github.Client
}

func NewClient(ctx context.Context, token string) Client {
	return &client{
		ghc: github.NewClient(oauth2.NewClient(ctx, oauth2.StaticTokenSource(
			&oauth2.Token{
				AccessToken: token,
			},
		))),
	}
}

func (c *client) GetCombinedStatus(ctx context.Context, owner, repo, ref string, opts *ListOptions) (*CombinedStatus, *Response, error) {
	return withRetry(ctx, defaultMaxRetries, defaultRetryDelay, func() (*CombinedStatus, *Response, error) {
		return c.ghc.Repositories.GetCombinedStatus(ctx, owner, repo, ref, opts)
	})
}

func (c *client) ListCheckRunsForRef(ctx context.Context, owner, repo, ref string, opts *ListCheckRunsOptions) (*ListCheckRunsResults, *Response, error) {
	return withRetry(ctx, defaultMaxRetries, defaultRetryDelay, func() (*ListCheckRunsResults, *Response, error) {
		return c.ghc.Checks.ListCheckRunsForRef(ctx, owner, repo, ref, opts)
	})
}

func (c *client) ListRepositoryWorkflowRuns(ctx context.Context, owner, repo string, opts *ListWorkflowRunsOptions) (*WorkflowRuns, *Response, error) {
	return withRetry(ctx, defaultMaxRetries, defaultRetryDelay, func() (*WorkflowRuns, *Response, error) {
		return c.ghc.Actions.ListRepositoryWorkflowRuns(ctx, owner, repo, opts)
	})
}

func (c *client) ListWorkflowJobs(ctx context.Context, owner, repo string, runID int64, opts *ListWorkflowJobsOptions) (*Jobs, *Response, error) {
	return withRetry(ctx, defaultMaxRetries, defaultRetryDelay, func() (*Jobs, *Response, error) {
		return c.ghc.Actions.ListWorkflowJobs(ctx, owner, repo, runID, opts)
	})
}

// withRetry runs fn and retries on 5xx and rate-limit responses with
// exponential backoff (or the server-requested Retry-After delay, whichever
// is longer). It does not retry on context cancellation or other 4xx errors.
func withRetry[T any](ctx context.Context, maxRetries int, initialDelay time.Duration, fn func() (T, *Response, error)) (T, *Response, error) {
	var zero T
	var lastResp *Response
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		v, resp, err := fn()
		if err == nil {
			return v, resp, nil
		}
		lastResp = resp
		lastErr = err

		if ctx.Err() != nil {
			return zero, lastResp, fmt.Errorf("context error while retrying: %w", ctx.Err())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return zero, lastResp, err
		}
		if resp == nil {
			return zero, lastResp, err
		}
		if !isRetryableStatus(resp) {
			return zero, lastResp, err
		}
		if attempt == maxRetries-1 {
			break
		}
		backoff := initialDelay * time.Duration(1<<uint(attempt))
		// Secondary rate limits announce when to come back; honor it when it
		// exceeds our own backoff. The select on ctx below still bounds the
		// wait by the gatekeeper's deadline.
		if ra := retryAfter(resp); ra > backoff {
			backoff = ra
		}
		select {
		case <-ctx.Done():
			return zero, lastResp, ctx.Err()
		case <-time.After(backoff):
			// retry
		}
	}
	return zero, lastResp, lastErr
}

// isRetryableStatus reports whether the response is a transient condition
// worth retrying: any 5xx, a primary rate limit (403 with the quota
// exhausted), or a secondary rate limit (429, or 403 carrying Retry-After
// while the primary quota is still positive — GitHub returns both shapes for
// secondary limits, which bursty polling across many gatekeepers triggers).
func isRetryableStatus(resp *Response) bool {
	if resp.StatusCode >= 500 && resp.StatusCode <= 599 {
		return true
	}
	if resp.StatusCode == 429 {
		return true
	}
	if resp.StatusCode == 403 {
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return true
		}
		if resp.Header.Get("Retry-After") != "" {
			return true
		}
	}
	return false
}

// retryAfter parses the Retry-After header (0 when absent or unparseable).
func retryAfter(resp *Response) time.Duration {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	seconds, err := strconv.Atoi(v)
	if err != nil || seconds < 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
