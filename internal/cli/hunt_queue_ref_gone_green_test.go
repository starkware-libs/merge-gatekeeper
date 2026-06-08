package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"

	"github.com/starkware-libs/merge-gatekeeper/internal/validators"
	"github.com/starkware-libs/merge-gatekeeper/internal/validators/mock"
)

// Contract under test: a merge-queue ref (gh-readonly-queue/*) deleted after
// the gatekeeper last saw an all-green world means the batch merged out from
// under the confirmation window — the verdict can no longer gate anything,
// so report SUCCESS instead of failing a merge that already happened
// (observed live on sequencer queue run 27126503199, ref
// gh-readonly-queue/main-v0.14.3/pr-14403-...).
//
// CRITICAL faithful detail: the validator needs refGoneTerminalPolls (3)
// consecutive "Ref not found" polls before it returns the terminal
// RefGoneError. The first two surface as TRANSIENT errors — which the loop
// treats as "observed nothing" and which reset the quiescence streak to 0.
// So by the time RefGoneError arrives the streak is always 0; the success
// decision must hinge on the last OBSERVED world being green, not on a live
// streak (the bug in the first cut of this fix: a `streak >= 1` guard that
// was structurally always false).
//
// Leniency stays strictly scoped: a gate whose last real observation was NOT
// green stays red (the queue merged something this gate hadn't confirmed),
// and a deleted NON-queue ref stays red.
func Test_doValidateCmd_QueueRefGoneAfterGreenPoll(t *testing.T) {
	type outcome int
	const (
		green outcome = iota
		pending
		transient // a ref-gone 404 before the terminal threshold (or any API hiccup)
		refgone   // the terminal RefGoneError
	)

	mkValidator := func(ref string, script []outcome) *mock.Validator {
		i := 0
		return &mock.Validator{
			NameFunc: func() string { return "merge-gatekeeper" },
			ValidateFunc: func(ctx context.Context) (validators.Status, error) {
				o := script[min(i, len(script)-1)]
				i++
				switch o {
				case green:
					return greenStatus("sha\x00build", 1), nil
				case pending:
					st := greenStatus("sha\x00build", 1)
					st.IsSuccessFunc = func() bool { return false }
					return st, nil
				case transient:
					return nil, &validators.TransientError{Err: errors.New("404 Ref not found")}
				default: // refgone
					return nil, &validators.RefGoneError{Ref: ref, Polls: 3, Err: errors.New("404 Ref not found")}
				}
			},
		}
	}

	// The faithful production sequence: one green observation, then the two
	// transient ref-gone polls that reset the streak, then the terminal error.
	greenThenGone := []outcome{green, transient, transient, refgone}

	tests := map[string]struct {
		ref     string
		script  []outcome
		wantErr bool
	}{
		"queue ref deleted after a green poll reports success": {
			ref:     "refs/heads/gh-readonly-queue/main/pr-1-abc",
			script:  greenThenGone,
			wantErr: false,
		},
		"queue ref with the real nested branch name reports success": {
			ref:     "refs/heads/gh-readonly-queue/main-v0.14.3/pr-14403-abc",
			script:  greenThenGone,
			wantErr: false,
		},
		"queue ref never green stays red": {
			ref:     "refs/heads/gh-readonly-queue/main/pr-1-abc",
			script:  []outcome{pending, transient, transient, refgone},
			wantErr: true,
		},
		"queue ref green then a re-run went pending stays red": {
			ref:     "refs/heads/gh-readonly-queue/main/pr-1-abc",
			script:  []outcome{green, pending, transient, transient, refgone},
			wantErr: true,
		},
		"non-queue ref deleted after a green poll stays red": {
			ref:     "refs/heads/feature",
			script:  greenThenGone,
			wantErr: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			setLoopConfig(t, 1, 30, 3, 0)
			oldRef := ghRef
			t.Cleanup(func() { ghRef = oldRef })
			ghRef = tt.ref

			err := doValidateCmd(context.Background(), &cobra.Command{}, mkValidator(tt.ref, tt.script))
			if tt.wantErr {
				if err == nil {
					t.Fatal("doValidateCmd() must stay red: the ref-gone leniency applies only to merge-queue refs whose last observed world was green")
				}
				return
			}
			if err != nil {
				t.Fatalf("doValidateCmd() must report success when a merge-queue ref is deleted after a green poll (the batch merged); got: %v", err)
			}
		})
	}
}
