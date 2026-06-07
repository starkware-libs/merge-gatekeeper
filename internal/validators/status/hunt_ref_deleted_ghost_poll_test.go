package status

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
	"github.com/starkware-libs/merge-gatekeeper/internal/validators"
)

// Test_Hunt_RefDeletedGhostPoll reproduces the 2026-06-07 production tail of
// sequencer run 27088546998 (observed live, first hour of the v1.3.0
// deploy): the merge queue merged the batch and deleted the
// gh-readonly-queue branch while the gatekeeper was mid-confirmation. Every
// subsequent poll 404'd ("Ref not found") and was classified transient, so
// the gatekeeper ghost-polled a deleted ref for 21 minutes until its
// deadline.
//
// Contract asserted: a ref that RESOLVED earlier in the run and then returns
// "Ref not found" on several consecutive polls was deleted — that is
// terminal, not transient (for a merge-queue ref it means the batch already
// merged or was dequeued). The gatekeeper must fail fast with an error that
// names the ref and is NOT transient, instead of burning the remaining
// timeout. The first ref-gone polls stay transient (a fresh 404 can be
// replication lag), and a 404 on a ref that never resolved keeps the old
// keep-polling behavior.
func Test_Hunt_RefDeletedGhostPoll(t *testing.T) {
	refGone := &github.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusNotFound},
		Message:  "Ref not found",
	}

	polls := 0
	client := &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			polls++
			if polls == 1 {
				return &github.CombinedStatus{}, nil, nil
			}
			// The branch was deleted after the first poll.
			return nil, nil, refGone
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			one := 1
			status := "completed"
			conclusion := checkRunSuccessConclusion
			return &github.ListCheckRunsResults{Total: &one, CheckRuns: []*github.CheckRun{{
				ID:     int64Ptr(1),
				Name:   stringPtr("build"),
				Status: &status, Conclusion: &conclusion,
				App: &github.App{ID: int64Ptr(77), Slug: stringPtr("third-party-ci")},
			}}}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			zero := 0
			return &github.WorkflowRuns{TotalCount: &zero}, nil, nil
		},
	}

	sv := &statusValidator{
		client:      client,
		owner:       "test-owner",
		repo:        "test-repo",
		ref:         fullSHARef, // resolves on poll 1, setting lastResolvedHeadSHA
		selfJobName: "self-job",
	}

	// Poll 1: the ref resolves and the world is green.
	if _, err := sv.Validate(context.Background()); err != nil {
		t.Fatalf("Validate() poll 1 returned unexpected error: %v", err)
	}

	// Polls 2 and 3: first ref-gone responses — still transient (could be
	// replication lag), the loop keeps polling.
	for poll := 2; poll <= 3; poll++ {
		_, err := sv.Validate(context.Background())
		if err == nil {
			t.Fatalf("Validate() poll %d should surface the listing error", poll)
		}
		if !validators.IsTransient(err) {
			t.Fatalf("Validate() poll %d: the first ref-gone polls must stay transient; got terminal error: %v", poll, err)
		}
	}

	// Poll 4: the third consecutive ref-gone poll on a previously-resolving
	// ref — the ref was deleted; this must be terminal and actionable.
	_, err := sv.Validate(context.Background())
	if err == nil {
		t.Fatal("Validate() poll 4 should fail once the ref is conclusively gone")
	}
	if validators.IsTransient(err) {
		t.Errorf("a previously-resolving ref missing for 3 consecutive polls is deleted, not transient; the gatekeeper must fail fast instead of ghost-polling to its deadline. got transient: %v", err)
	}
	if !strings.Contains(err.Error(), fullSHARef) {
		t.Errorf("the terminal error must name the ref so the failure is actionable; got: %v", err)
	}
}
