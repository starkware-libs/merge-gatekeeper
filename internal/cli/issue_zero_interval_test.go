package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/starkware-libs/merge-gatekeeper/internal/validators"
	"github.com/starkware-libs/merge-gatekeeper/internal/validators/mock"
)

// Theory: doValidateCmd feeds the configured interval straight into
// time.NewTicker, which panics on a non-positive duration. The action input
// is a free-form string ('interval: "0"' is one typo away), and the result
// is a runtime panic with a goroutine dump instead of a clear configuration
// error. A zero timeout similarly produces an instantly-expired context and
// a confusing "context deadline exceeded" failure.
//
// Expected correct behavior: invalid interval/timeout values are rejected
// with an explicit error before any polling starts.
func Test_Issue_ZeroIntervalPanicsInsteadOfCleanError(t *testing.T) {
	oldInterval := validateIntervalSeconds
	defer func() { validateIntervalSeconds = oldInterval }()
	validateIntervalSeconds = 0

	v := &mock.Validator{
		NameFunc: func() string { return "merge-gatekeeper" },
		ValidateFunc: func(ctx context.Context) (validators.Status, error) {
			return &mock.Status{
				DetailFunc:    func() string { return "all jobs passed" },
				IsSuccessFunc: func() bool { return true },
			}, nil
		},
	}

	err := doValidateCmd(context.Background(), &cobra.Command{}, v)
	if err == nil {
		t.Fatal("doValidateCmd() must reject interval=0 with a clear error (pre-fix it panics in time.NewTicker)")
	}
	if !strings.Contains(err.Error(), "interval") {
		t.Errorf("error should name the offending option 'interval'; got: %v", err)
	}
}

func Test_Issue_ZeroTimeoutConfusingError(t *testing.T) {
	oldTimeout := timeoutSecond
	defer func() { timeoutSecond = oldTimeout }()
	timeoutSecond = 0

	v := &mock.Validator{
		NameFunc: func() string { return "merge-gatekeeper" },
		ValidateFunc: func(ctx context.Context) (validators.Status, error) {
			return &mock.Status{
				DetailFunc:    func() string { return "all jobs passed" },
				IsSuccessFunc: func() bool { return true },
			}, nil
		},
	}

	err := doValidateCmd(context.Background(), &cobra.Command{}, v)
	if err == nil {
		t.Fatal("doValidateCmd() must reject timeout=0 with a clear error (pre-fix it reports a bare 'context deadline exceeded')")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error should name the offending option 'timeout'; got: %v", err)
	}
}
