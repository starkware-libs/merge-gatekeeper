package github

import (
	"context"
	"errors"
	"fmt"
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
	CheckRun             = github.CheckRun
	CheckSuite           = github.CheckSuite
	ListCheckRunsOptions = github.ListCheckRunsOptions
	ListCheckRunsResults = github.ListCheckRunsResults
)

type Client interface {
	GetCombinedStatus(ctx context.Context, owner, repo, ref string, opts *ListOptions) (*CombinedStatus, *Response, error)
	ListCheckRunsForRef(ctx context.Context, owner, repo, ref string, opts *ListCheckRunsOptions) (*ListCheckRunsResults, *Response, error)
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

// withRetry runs fn and retries on 5xx with exponential backoff. It does not retry on context
// cancellation or 4xx errors.
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
		if resp == nil || resp.StatusCode < 500 || resp.StatusCode > 599 {
			return zero, lastResp, err
		}
		if attempt == maxRetries-1 {
			break
		}
		backoff := initialDelay * time.Duration(1<<uint(attempt))
		select {
		case <-ctx.Done():
			return zero, lastResp, ctx.Err()
		case <-time.After(backoff):
			// retry
		}
	}
	return zero, lastResp, lastErr
}
