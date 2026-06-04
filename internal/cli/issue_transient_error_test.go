package cli

import (
	"context"
	"fmt"
	"testing"

	"github.com/spf13/cobra"

	"github.com/starkware-libs/merge-gatekeeper/internal/validators"
	"github.com/starkware-libs/merge-gatekeeper/internal/validators/mock"
)

// Regression test: the polling loop in doValidateCmd exited on the FIRST
// error returned by Validate, no matter what kind of error it was. Validate
// returns errors both for real CI failures (failed/cancelled jobs — where
// fast exit is correct) and for transient infrastructure problems: a 5xx
// burst that outlives withRetry's backoff, a rate-limit response, a DNS
// hiccup, or the transiently-inconsistent listings the May 26 fixes were
// about. For the latter class the gatekeeper still had most of its timeout
// budget left, and one flaky API call turned a healthy CI run red.
//
// The code itself documented the opposite intent — detectDuplicateNamedJobs
// says: "If any API call fails, we propagate the error and rely on the
// caller to retry on the next poll." No caller retried.
//
// Expected correct behavior: transient API errors (validators.TransientError,
// which the status validator wraps around every GitHub API call failure)
// surface as a warning and the loop keeps polling until the deadline; only
// genuine validation failures abort the run early.
func Test_Issue_TransientAPIErrorKillsPollingLoop(t *testing.T) {
	polls := 0
	v := &mock.Validator{
		NameFunc: func() string { return "merge-gatekeeper" },
		ValidateFunc: func(ctx context.Context) (validators.Status, error) {
			polls++
			if polls == 1 {
				// First poll: the GitHub API hiccups (502 after retries).
				// This is NOT a CI failure — no job failed.
				return nil, fmt.Errorf("failed to fetch workflow runs (actions: read permission required): %w",
					&validators.TransientError{Err: fmt.Errorf("GET https://api.github.com/repos/o/r/actions/runs: 502 Bad Gateway")})
			}
			// Next poll: the API recovered and CI is green.
			return &mock.Status{
				DetailFunc:    func() string { return "all jobs passed" },
				IsSuccessFunc: func() bool { return true },
			}, nil
		},
	}

	err := doValidateCmd(context.Background(), &cobra.Command{}, v)
	if err != nil {
		t.Errorf("doValidateCmd() must survive a transient API error and keep polling "+
			"(per the documented intent in detectDuplicateNamedJobs); instead the first "+
			"hiccup killed the gatekeeper after %d poll(s): %v", polls, err)
	}
	if polls < 2 {
		t.Errorf("expected the loop to poll again after the transient error; polls = %d", polls)
	}
}
