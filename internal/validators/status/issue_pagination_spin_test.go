package status

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// Theory: the pagination loops in getCombinedStatus and listCheckRunsForRef
// terminate only via `total_count <= len(collected)`. If GitHub ever reports
// a total_count higher than the number of items it actually returns (the
// same eventual-consistency family as the May 26 orphan-suite incident, or
// simply items deleted between page fetches), the loop requests page after
// page of empty results forever — hammering the API at full speed, burning
// the repository's shared rate limit, until the gatekeeper's outer timeout
// finally kills the context.
//
// The codebase already knows about this failure mode: listWorkflowRunsForRef
// guards with `if len(wr.WorkflowRuns) == 0 { break }`. The other two loops
// were never given the same guard.
//
// Expected correct behavior: a page that returns zero items terminates
// pagination, as in listWorkflowRunsForRef.
func Test_Issue_PaginationSpinsOnInconsistentTotalCount(t *testing.T) {
	newValidator := func(c *mock.Client) *statusValidator {
		return &statusValidator{
			client:      c,
			owner:       "test-owner",
			repo:        "test-repo",
			ref:         "main",
			selfJobName: "self-job",
		}
	}

	t.Run("getCombinedStatus", func(t *testing.T) {
		var calls int64
		client := &mock.Client{
			GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
				atomic.AddInt64(&calls, 1)
				// total_count says 3, but no statuses are ever returned —
				// an inconsistent listing must not hang the gatekeeper.
				three := 3
				return &github.CombinedStatus{TotalCount: &three}, nil, nil
			},
		}
		sv := newValidator(client)

		done := make(chan struct{})
		go func() {
			_, _ = sv.getCombinedStatus(context.Background())
			close(done)
		}()
		select {
		case <-done:
			// Terminated — pagination is guarded.
		case <-time.After(500 * time.Millisecond):
			t.Errorf("getCombinedStatus() is stuck paginating an inconsistent listing "+
				"(total_count=3, zero items): %d API calls in 500ms and counting",
				atomic.LoadInt64(&calls))
		}
	})

	t.Run("listCheckRunsForRef", func(t *testing.T) {
		var calls int64
		client := &mock.Client{
			ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
				atomic.AddInt64(&calls, 1)
				three := 3
				return &github.ListCheckRunsResults{Total: &three}, nil, nil
			},
		}
		sv := newValidator(client)

		done := make(chan struct{})
		go func() {
			_, _ = sv.listCheckRunsForRef(context.Background())
			close(done)
		}()
		select {
		case <-done:
			// Terminated — pagination is guarded.
		case <-time.After(500 * time.Millisecond):
			t.Errorf("listCheckRunsForRef() is stuck paginating an inconsistent listing "+
				"(total=3, zero items): %d API calls in 500ms and counting",
				atomic.LoadInt64(&calls))
		}
	})
}
