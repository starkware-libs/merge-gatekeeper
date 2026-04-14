package github

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestWithRetry_SuccessOnFirstCall(t *testing.T) {
	ctx := context.Background()
	calls := 0
	v, resp, err := withRetry(ctx, 3, time.Millisecond, func() (interface{}, *Response, error) {
		calls++
		return "ok", &Response{Response: &http.Response{StatusCode: 200}}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "ok" || resp == nil || resp.StatusCode != 200 {
		t.Errorf("got v=%v resp=%v", v, resp)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestWithRetry_RetryThenSucceed(t *testing.T) {
	ctx := context.Background()
	calls := 0
	v, resp, err := withRetry(ctx, 3, 1*time.Millisecond, func() (interface{}, *Response, error) {
		calls++
		if calls < 2 {
			return nil, &Response{Response: &http.Response{StatusCode: 500}}, errors.New("500")
		}
		return "ok", &Response{Response: &http.Response{StatusCode: 200}}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "ok" {
		t.Errorf("got v=%v", v)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
	_ = resp
}

func TestWithRetry_FailAfterMaxRetries(t *testing.T) {
	ctx := context.Background()
	calls := 0
	_, resp, err := withRetry(ctx, 3, 1*time.Millisecond, func() (interface{}, *Response, error) {
		calls++
		return nil, &Response{Response: &http.Response{StatusCode: 500}}, errors.New("500")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if resp == nil || resp.StatusCode != 500 {
		t.Errorf("expected 500 response, got %v", resp)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestWithRetry_NoRetryOn4xx(t *testing.T) {
	ctx := context.Background()
	calls := 0
	_, resp, err := withRetry(ctx, 3, time.Millisecond, func() (interface{}, *Response, error) {
		calls++
		return nil, &Response{Response: &http.Response{StatusCode: 404}}, errors.New("not found")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry on 4xx), got %d", calls)
	}
	_ = resp
}

func TestWithRetry_RetryOnRateLimit(t *testing.T) {
	ctx := context.Background()
	calls := 0
	v, resp, err := withRetry(ctx, 3, 1*time.Millisecond, func() (interface{}, *Response, error) {
		calls++
		if calls < 2 {
			return nil, &Response{Response: &http.Response{
				StatusCode: 403,
				Header:     http.Header{"X-Ratelimit-Remaining": []string{"0"}},
			}}, errors.New("rate limited")
		}
		return "ok", &Response{Response: &http.Response{StatusCode: 200}}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "ok" {
		t.Errorf("got v=%v", v)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls, got %d", calls)
	}
	_ = resp
}

func TestWithRetry_NoRetryOn403NonRateLimit(t *testing.T) {
	ctx := context.Background()
	calls := 0
	_, resp, err := withRetry(ctx, 3, time.Millisecond, func() (interface{}, *Response, error) {
		calls++
		return nil, &Response{Response: &http.Response{
			StatusCode: 403,
			Header:     http.Header{},
		}}, errors.New("forbidden")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected 1 call (no retry on non-rate-limit 403), got %d", calls)
	}
	_ = resp
}

func TestWithRetry_NoRetryOnContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, _, err := withRetry(ctx, 3, time.Millisecond, func() (interface{}, *Response, error) {
		calls++
		return nil, &Response{Response: &http.Response{StatusCode: 500}}, errors.New("500")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}
