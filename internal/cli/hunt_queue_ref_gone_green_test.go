package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"

	"github.com/starkware-libs/merge-gatekeeper/internal/validators"
	"github.com/starkware-libs/merge-gatekeeper/internal/validators/mock"
)

// Contract under test: a merge-queue ref (gh-readonly-queue/*) deleted right
// after a green poll means the batch merged out from under the confirmation
// window — the verdict can no longer gate anything, and the last observed
// world was green, so the gatekeeper must report SUCCESS instead of failing
// a merge that already happened (observed live: every merged sequencer batch
// ended with a red gatekeeper-new, e.g. runs 27091764167/27091750873).
//
// The leniency is strictly scoped: a gate that never saw green stays red
// (the queue merged something this gate hadn't finished checking — real
// signal), and a deleted NON-queue ref stays red (a vanished feature branch
// must not get a green stamp).
func Test_doValidateCmd_QueueRefGoneAfterGreenPoll(t *testing.T) {
	refGone := func(ref string) error {
		return &validators.RefGoneError{Ref: ref, Polls: 3, Err: errors.New("404 Ref not found")}
	}

	tests := map[string]struct {
		ref        string
		firstGreen bool // whether poll 1 observes an all-green world
		wantErr    bool
	}{
		"queue ref deleted after a green poll reports success": {
			ref:        "refs/heads/gh-readonly-queue/main/pr-1-abc",
			firstGreen: true,
			wantErr:    false,
		},
		"queue ref deleted while never green stays red": {
			ref:        "refs/heads/gh-readonly-queue/main/pr-1-abc",
			firstGreen: false,
			wantErr:    true,
		},
		"non-queue ref deleted after a green poll stays red": {
			ref:        "refs/heads/feature",
			firstGreen: true,
			wantErr:    true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			setLoopConfig(t, 1, 10, 3, 0)
			oldRef := ghRef
			t.Cleanup(func() { ghRef = oldRef })
			ghRef = tt.ref

			polls := 0
			v := &mock.Validator{
				NameFunc: func() string { return "merge-gatekeeper" },
				ValidateFunc: func(ctx context.Context) (validators.Status, error) {
					polls++
					if polls == 1 {
						st := greenStatus("sha\x00build", 1)
						if !tt.firstGreen {
							st.IsSuccessFunc = func() bool { return false }
						}
						return st, nil
					}
					// The queue merged the batch and deleted the branch.
					return nil, refGone(tt.ref)
				},
			}

			err := doValidateCmd(context.Background(), &cobra.Command{}, v)
			if tt.wantErr {
				if err == nil {
					t.Fatal("doValidateCmd() must stay red: the ref-gone leniency applies only to merge-queue refs whose last poll was green")
				}
				return
			}
			if err != nil {
				t.Fatalf("doValidateCmd() must report success when a merge-queue ref is deleted right after a green poll (the batch merged); got: %v", err)
			}
			if polls != 2 {
				t.Errorf("success must come from the ref-gone poll itself; polls = %d", polls)
			}
		})
	}
}

// Regression for the production sequence the test above skips: a terminal
// RefGoneError is never raised on the poll right after the green one. It is
// raised only once the watched ref has 404'd for several CONSECUTIVE polls,
// and every one of those leading 404s comes back as a transient error first.
// A transient poll yields no status, which the loop treats as "not green" and
// so resets the confirmation streak to 0. By the time the RefGoneError fires,
// streak is therefore always 0 — making the `streak >= 1` carve-out
// unreachable in practice and turning a merged batch red.
//
// The carve-out's real question is "did we EVER see this ref green before it
// vanished?", not "was the immediately preceding poll green?". Observed live:
// sequencer run 27264098063 — a merge_group batch that polled green, then
// 404'd as the batch merged, and the gatekeeper-new job went red while the
// upstream gatekeeper passed.
func Test_doValidateCmd_QueueRefGoneAfterGreenThenTransientPolls(t *testing.T) {
	// confirmPolls=3 so the single green poll can never satisfy quiescence on
	// its own: success here must come from the merge-queue ref-gone carve-out.
	setLoopConfig(t, 1, 10, 3, 0)

	ref := "refs/heads/gh-readonly-queue/main/pr-1-abc"
	oldRef := ghRef
	t.Cleanup(func() { ghRef = oldRef })
	ghRef = ref

	transient := &validators.TransientError{Err: errors.New("404 Ref not found")}
	refGone := &validators.RefGoneError{Ref: ref, Polls: 3, Err: errors.New("404 Ref not found")}

	polls := 0
	v := &mock.Validator{
		NameFunc: func() string { return "merge-gatekeeper" },
		ValidateFunc: func(ctx context.Context) (validators.Status, error) {
			polls++
			switch polls {
			case 1:
				return greenStatus("sha\x00build", 1), nil // streak -> 1
			case 2, 3:
				return nil, transient // 404s on the way to ref-gone; reset streak to 0
			default:
				return nil, refGone // batch merged, branch deleted
			}
		},
	}

	if err := doValidateCmd(context.Background(), &cobra.Command{}, v); err != nil {
		t.Fatalf("a merge-queue ref deleted after a green poll must report success even when the usual transient 404s precede the terminal RefGoneError; got: %v", err)
	}
}
