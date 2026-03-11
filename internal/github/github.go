package github

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/go-github/v38/github"
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
	v, resp, err := withRetry(ctx, defaultMaxRetries, defaultRetryDelay, func() (interface{}, *Response, error) {
		return c.ghc.Repositories.GetCombinedStatus(ctx, owner, repo, ref, opts)
	})
	if err != nil {
		return nil, resp, err
	}
	return v.(*CombinedStatus), resp, nil
}

func (c *client) ListCheckRunsForRef(ctx context.Context, owner, repo, ref string, opts *ListCheckRunsOptions) (*ListCheckRunsResults, *Response, error) {
	v, resp, err := withRetry(ctx, defaultMaxRetries, defaultRetryDelay, func() (interface{}, *Response, error) {
		return c.ghc.Checks.ListCheckRunsForRef(ctx, owner, repo, ref, opts)
	})
	if err != nil {
		return nil, resp, err
	}
	return v.(*ListCheckRunsResults), resp, nil
}

// withRetry runs fn and retries on 5xx with exponential backoff. It does not retry on context
// cancellation or 4xx errors.
func withRetry(ctx context.Context, maxRetries int, initialDelay time.Duration, fn func() (interface{}, *Response, error)) (interface{}, *Response, error) {
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
			return nil, lastResp, fmt.Errorf("context error while retrying: %w", ctx.Err())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, lastResp, err
		}
		if resp == nil || resp.StatusCode < 500 || resp.StatusCode > 599 {
			return nil, lastResp, err
		}
		if attempt == maxRetries-1 {
			break
		}
		backoff := initialDelay * time.Duration(1<<uint(attempt))
		select {
		case <-ctx.Done():
			return nil, lastResp, ctx.Err()
		case <-time.After(backoff):
			// retry
		}
	}
	return nil, lastResp, lastErr
}
