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
