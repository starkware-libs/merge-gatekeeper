package status

import (
	"context"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
	"github.com/starkware-libs/merge-gatekeeper/internal/validators"
)

// Theory: a malformed item in a GitHub listing — a check run with no status,
// or a combined-status entry with no context — makes listGhaStatuses return
// ErrInvalidCheckRunResponse/ErrInvalidCombinedStatusResponse as a plain
// error. The polling loop (commit 5307b86) distinguishes transient
// infrastructure failures, which warn and retry until the deadline, from real
// validation outcomes, which abort: an API response the gatekeeper cannot
// interpret is the former — one half-synced or garbled poll must not paint
// the gate red while timeout budget remains, any more than a failed API call
// would.
//
// Expected correct behavior: Validate surfaces malformed responses as
// validators.TransientError so the next poll retries.
func Test_Issue_MalformedListingAbortsInsteadOfRetrying(t *testing.T) {
	t.Run("check run with nil status", func(t *testing.T) {
		client := &mock.Client{
			GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
				return &github.CombinedStatus{}, nil, nil
			},
			ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
				return &github.ListCheckRunsResults{
					CheckRuns: []*github.CheckRun{
						{
							ID:   int64Ptr(1),
							Name: stringPtr("build"),
							// Status missing — uninterpretable item.
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

		_, err := sv.Validate(context.Background())
		if err == nil {
			t.Fatal("Validate() should return an error for a check run with no status")
		}
		if !validators.IsTransient(err) {
			t.Errorf("FALSE RED: a malformed check-run listing aborts the gatekeeper instead of "+
				"retrying on the next poll — error is not transient: %v", err)
		}
	})

	t.Run("combined status with nil context", func(t *testing.T) {
		total := 1
		client := &mock.Client{
			GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
				return &github.CombinedStatus{
					TotalCount: &total,
					Statuses: []*github.RepoStatus{
						{
							// Context and state missing — uninterpretable item.
							ID: int64Ptr(7),
						},
					},
				}, nil, nil
			},
			ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
				return &github.ListCheckRunsResults{}, nil, nil
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
			t.Fatal("Validate() should return an error for a combined status with no context")
		}
		if !validators.IsTransient(err) {
			t.Errorf("FALSE RED: a malformed combined-status listing aborts the gatekeeper instead "+
				"of retrying on the next poll — error is not transient: %v", err)
		}
	})
}
