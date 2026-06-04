package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/starkware-libs/merge-gatekeeper/internal/validators"
	"github.com/starkware-libs/merge-gatekeeper/internal/validators/mock"
)

// setLoopConfig overrides the loop's package-level configuration for one test
// and restores the previous values on cleanup (same idiom as the zero-interval
// tests).
func setLoopConfig(t *testing.T, interval, timeout, confirm, grace uint) {
	t.Helper()
	oldInterval, oldTimeout := validateIntervalSeconds, timeoutSecond
	oldConfirm, oldGrace := confirmPolls, emptyGraceSeconds
	t.Cleanup(func() {
		validateIntervalSeconds, timeoutSecond = oldInterval, oldTimeout
		confirmPolls, emptyGraceSeconds = oldConfirm, oldGrace
	})
	validateIntervalSeconds, timeoutSecond = interval, timeout
	confirmPolls, emptyGraceSeconds = confirm, grace
}

// greenStatus returns a green mock status with the given fingerprint and
// tracked-job count.
func greenStatus(fingerprint string, tracked int) *mock.Status {
	return &mock.Status{
		DetailFunc:      func() string { return "all jobs passed" },
		IsSuccessFunc:   func() bool { return true },
		FingerprintFunc: func() string { return fingerprint },
		TrackedJobsFunc: func() int { return tracked },
	}
}

func Test_doValidateCmd_QuiescenceRequiresConsecutiveGreens(t *testing.T) {
	setLoopConfig(t, 1, 10, 2, 0)

	polls := 0
	v := &mock.Validator{
		NameFunc: func() string { return "merge-gatekeeper" },
		ValidateFunc: func(ctx context.Context) (validators.Status, error) {
			polls++
			return greenStatus("sha\x00build", 1), nil
		},
	}

	if err := doValidateCmd(context.Background(), &cobra.Command{}, v); err != nil {
		t.Fatalf("doValidateCmd() returned unexpected error: %v", err)
	}
	if polls != 2 {
		t.Errorf("confirm-polls=2 with a stable green world must succeed on exactly the 2nd poll; polls = %d", polls)
	}
}

func Test_doValidateCmd_QuiescenceStreakResetsOnFingerprintChange(t *testing.T) {
	setLoopConfig(t, 1, 10, 2, 0)

	// Models a branch --ref advancing between polls (the fingerprint carries
	// the resolved head SHA): green throughout, but the world changed after
	// the first poll, so the streak must restart at the new world.
	fingerprints := []string{"sha1\x00build", "sha2\x00build", "sha2\x00build", "sha2\x00build"}
	polls := 0
	v := &mock.Validator{
		NameFunc: func() string { return "merge-gatekeeper" },
		ValidateFunc: func(ctx context.Context) (validators.Status, error) {
			fp := fingerprints[min(polls, len(fingerprints)-1)]
			polls++
			return greenStatus(fp, 1), nil
		},
	}

	if err := doValidateCmd(context.Background(), &cobra.Command{}, v); err != nil {
		t.Fatalf("doValidateCmd() returned unexpected error: %v", err)
	}
	if polls != 3 {
		t.Errorf("a fingerprint change on poll 2 must reset the streak: success expected on poll 3 (1 mismatched + 2 stable); polls = %d", polls)
	}
}

func Test_doValidateCmd_QuiescenceStreakResetsOnTransientError(t *testing.T) {
	setLoopConfig(t, 1, 10, 2, 0)

	polls := 0
	v := &mock.Validator{
		NameFunc: func() string { return "merge-gatekeeper" },
		ValidateFunc: func(ctx context.Context) (validators.Status, error) {
			polls++
			if polls == 2 {
				// The API hiccuped: this poll observed nothing, so it cannot
				// extend the quiescence streak.
				return nil, &validators.TransientError{Err: errors.New("502 Bad Gateway")}
			}
			return greenStatus("sha\x00build", 1), nil
		},
	}

	if err := doValidateCmd(context.Background(), &cobra.Command{}, v); err != nil {
		t.Fatalf("doValidateCmd() returned unexpected error: %v", err)
	}
	if polls != 4 {
		t.Errorf("a transient error must reset the streak: green, transient, green, green => success on poll 4; polls = %d", polls)
	}
}

func Test_doValidateCmd_QuiescenceStreakResetsOnNonGreen(t *testing.T) {
	setLoopConfig(t, 1, 10, 2, 0)

	polls := 0
	v := &mock.Validator{
		NameFunc: func() string { return "merge-gatekeeper" },
		ValidateFunc: func(ctx context.Context) (validators.Status, error) {
			polls++
			if polls == 2 {
				// A job went back to pending (e.g. a re-run started).
				st := greenStatus("sha\x00build", 1)
				st.IsSuccessFunc = func() bool { return false }
				return st, nil
			}
			return greenStatus("sha\x00build", 1), nil
		},
	}

	if err := doValidateCmd(context.Background(), &cobra.Command{}, v); err != nil {
		t.Fatalf("doValidateCmd() returned unexpected error: %v", err)
	}
	if polls != 4 {
		t.Errorf("a non-green poll must reset the streak: green, pending, green, green => success on poll 4; polls = %d", polls)
	}
}

func Test_doValidateCmd_QuiescenceKeepsRedFailFast(t *testing.T) {
	setLoopConfig(t, 1, 10, 3, 0)

	polls := 0
	v := &mock.Validator{
		NameFunc: func() string { return "merge-gatekeeper" },
		ValidateFunc: func(ctx context.Context) (validators.Status, error) {
			polls++
			if polls == 2 {
				// A genuine CI failure mid-streak must abort immediately —
				// quiescence applies to green only.
				return nil, errors.New("1 job failed: build")
			}
			return greenStatus("sha\x00build", 1), nil
		},
	}

	err := doValidateCmd(context.Background(), &cobra.Command{}, v)
	if err == nil {
		t.Fatal("doValidateCmd() must fail fast on a genuine validation failure even mid-streak")
	}
	if polls != 2 {
		t.Errorf("red must abort on the poll that sees it; polls = %d", polls)
	}
}

func Test_doValidateCmd_EmptySetWaitsForGrace(t *testing.T) {
	setLoopConfig(t, 1, 10, 1, 1)

	// Zero discovered jobs: even a green poll must wait out the empty-set
	// grace (1s here), so success cannot come from the instant first poll.
	polls := 0
	v := &mock.Validator{
		NameFunc: func() string { return "merge-gatekeeper" },
		ValidateFunc: func(ctx context.Context) (validators.Status, error) {
			polls++
			return greenStatus("sha\x00", 0), nil
		},
	}
	if err := doValidateCmd(context.Background(), &cobra.Command{}, v); err != nil {
		t.Fatalf("doValidateCmd() returned unexpected error: %v", err)
	}
	if polls < 2 {
		t.Errorf("an empty job set must hold the gate for the grace period; succeeded after %d poll(s)", polls)
	}

	// Control: the same configuration with a non-empty set succeeds on the
	// first poll — the grace is specific to the empty case.
	polls = 0
	v.ValidateFunc = func(ctx context.Context) (validators.Status, error) {
		polls++
		return greenStatus("sha\x00build", 1), nil
	}
	if err := doValidateCmd(context.Background(), &cobra.Command{}, v); err != nil {
		t.Fatalf("doValidateCmd() returned unexpected error on the non-empty control: %v", err)
	}
	if polls != 1 {
		t.Errorf("a non-empty green set with confirm-polls=1 must succeed on the first poll; polls = %d", polls)
	}
}

func Test_doValidateCmd_RejectsConfirmPollsZero(t *testing.T) {
	setLoopConfig(t, 1, 2, 0, 0)

	v := &mock.Validator{
		NameFunc: func() string { return "merge-gatekeeper" },
		ValidateFunc: func(ctx context.Context) (validators.Status, error) {
			return greenStatus("sha\x00build", 1), nil
		},
	}

	err := doValidateCmd(context.Background(), &cobra.Command{}, v)
	if err == nil {
		t.Fatal("doValidateCmd() must reject confirm-polls=0 with a clear error")
	}
	if !strings.Contains(err.Error(), "confirm-polls") {
		t.Errorf("error should name the offending option 'confirm-polls'; got: %v", err)
	}
}

func Test_doValidateCmd_RejectsImpossibleQuiescenceBudget(t *testing.T) {
	// (confirm-polls-1)*interval + empty-grace = 2*5 + 30 = 40 >= timeout 10:
	// the success path can never complete before the deadline.
	setLoopConfig(t, 5, 10, 3, 30)

	v := &mock.Validator{
		NameFunc: func() string { return "merge-gatekeeper" },
		ValidateFunc: func(ctx context.Context) (validators.Status, error) {
			return greenStatus("sha\x00build", 1), nil
		},
	}

	err := doValidateCmd(context.Background(), &cobra.Command{}, v)
	if err == nil {
		t.Fatal("doValidateCmd() must reject a quiescence budget that cannot fit in the timeout")
	}
	if !strings.Contains(err.Error(), "confirm-polls") || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error should name 'confirm-polls' and 'timeout' so the config is actionable; got: %v", err)
	}
}
