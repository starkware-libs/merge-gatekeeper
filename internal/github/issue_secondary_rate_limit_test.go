package github

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// Theory: withRetry handles primary rate limiting (403 with
// X-RateLimit-Remaining: 0) but not GitHub's *secondary* rate limits, which
// are returned as 429 Too Many Requests, or as 403 with a Retry-After header
// while X-RateLimit-Remaining is still positive. Secondary limits are
// triggered by concurrent/bursty request patterns — exactly what a fleet of
// gatekeepers polling 4+ endpoints every few seconds across many open PRs
// produces (all sharing the repository's GITHUB_TOKEN budget).
//
// Current behavior: 429 falls through `resp.StatusCode < 500` and a
// Retry-After 403 fails the `isRateLimit` check, so the first secondary-limit
// response aborts the API call, which fails Validate, which kills the
// gatekeeper run red — even though GitHub explicitly tells us when to retry.
//
// Expected correct behavior: secondary rate-limit responses are transient and
// announced as retryable by GitHub; withRetry must retry them.
func Test_Issue_WithRetry_SecondaryRateLimit429NotRetried(t *testing.T) {
	ctx := context.Background()
	calls := 0
	v, _, err := withRetry(ctx, 3, time.Millisecond, func() (interface{}, *Response, error) {
		calls++
		if calls < 2 {
			return nil, &Response{Response: &http.Response{
				StatusCode: 429,
				Header:     http.Header{"Retry-After": []string{"1"}},
			}}, errors.New("429 you have exceeded a secondary rate limit")
		}
		return "ok", &Response{Response: &http.Response{StatusCode: 200}}, nil
	})
	if err != nil {
		t.Fatalf("withRetry must retry a 429 secondary rate limit; instead it aborted "+
			"after %d call(s) with: %v", calls, err)
	}
	if v != "ok" {
		t.Errorf("got v=%v, want ok", v)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (one retry), got %d", calls)
	}
}

func Test_Issue_WithRetry_SecondaryRateLimit403RetryAfterNotRetried(t *testing.T) {
	ctx := context.Background()
	calls := 0
	v, _, err := withRetry(ctx, 3, time.Millisecond, func() (interface{}, *Response, error) {
		calls++
		if calls < 2 {
			return nil, &Response{Response: &http.Response{
				StatusCode: 403,
				Header: http.Header{
					// Secondary limit: the primary quota is NOT exhausted...
					"X-Ratelimit-Remaining": []string{"4321"},
					// ...but GitHub asks us to back off and retry.
					"Retry-After": []string{"1"},
				},
			}}, errors.New("403 you have exceeded a secondary rate limit")
		}
		return "ok", &Response{Response: &http.Response{StatusCode: 200}}, nil
	})
	if err != nil {
		t.Fatalf("withRetry must retry a 403 secondary rate limit (Retry-After present); "+
			"instead it aborted after %d call(s) with: %v", calls, err)
	}
	if v != "ok" {
		t.Errorf("got v=%v, want ok", v)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (one retry), got %d", calls)
	}
}
