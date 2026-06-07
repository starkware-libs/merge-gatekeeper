package cli

import (
	"context"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/starkware-libs/merge-gatekeeper/internal/validators"
	"github.com/starkware-libs/merge-gatekeeper/internal/validators/mock"
)

// Theory under test: the empty-set grace is anchored at process start, so a
// branch --ref advancing mid-run to a SHA whose CI has not materialized yet
// (an empty, vacuously-green world) gets NO fresh grace — the quiescence
// streak re-arms on the fingerprint change, but the grace clock is already
// spent. The gate would then trust the empty world after only
// (confirm-polls-1) intervals, well inside the eventual-consistency window
// the grace was designed to cover ("right after a trigger, CI may simply not
// have materialized in the API yet" — and a mid-run push IS a new trigger).
//
// Contract asserted: success over an empty world must come no sooner than
// empty-grace seconds after that empty world was FIRST observed, regardless
// of how long the gatekeeper had already been running.
func Test_doValidateCmd_EmptyGraceReArmsWhenWorldChangesMidRun(t *testing.T) {
	const graceSeconds = 2
	setLoopConfig(t, 1, 20, 2, graceSeconds)

	// Poll script:
	//   p1: pending — the gate is held; the grace-from-start clock keeps
	//       running. (Also keeps the empty phase off the grace/interval
	//       boundary so the buggy and correct behaviors are well separated.)
	//   p2: green world A (one tracked job) — starts a streak on world A.
	//   p3+: the branch advanced; green EMPTY world B — the streak restarts,
	//        and by then time-since-start already exceeds the grace.
	var firstEmptyPoll time.Time
	polls := 0
	v := &mock.Validator{
		NameFunc: func() string { return "merge-gatekeeper" },
		ValidateFunc: func(ctx context.Context) (validators.Status, error) {
			polls++
			switch {
			case polls == 1:
				st := greenStatus("sha1\x00build", 1)
				st.IsSuccessFunc = func() bool { return false }
				return st, nil
			case polls == 2:
				return greenStatus("sha1\x00build", 1), nil
			default:
				if firstEmptyPoll.IsZero() {
					firstEmptyPoll = time.Now()
				}
				return greenStatus("sha2\x00", 0), nil
			}
		},
	}

	err := doValidateCmd(context.Background(), &cobra.Command{}, v)
	if err != nil {
		t.Fatalf("doValidateCmd() must still succeed once the re-armed grace elapses; got error: %v", err)
	}
	if firstEmptyPoll.IsZero() {
		t.Fatal("the empty world was never polled; the test scenario did not run as scripted")
	}

	observedEmpty := time.Since(firstEmptyPoll)
	if observedEmpty < graceSeconds*time.Second {
		t.Errorf("the empty world after a mid-run advance was trusted after only %v; "+
			"the %ds empty-set grace must re-arm when the observed world changes (a mid-run push is a new trigger)",
			observedEmpty, graceSeconds)
	}
}
