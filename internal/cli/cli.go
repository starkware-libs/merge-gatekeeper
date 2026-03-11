package cli

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

// These variables will be set by command line flags.
var (
	ghToken    string
	flagDebug  bool
)

// isDebugEnabled returns true if debug logging should be enabled (--debug or GitHub Actions debug env).
func isDebugEnabled() bool {
	if flagDebug {
		return true
	}
	s := os.Getenv("ACTIONS_STEP_DEBUG")
	if strings.EqualFold(s, "true") || s == "1" {
		return true
	}
	s = os.Getenv("ACTIONS_RUNNER_DEBUG")
	return strings.EqualFold(s, "true") || s == "1"
}

func Run(version string, args ...string) error {
	cmd := &cobra.Command{
		Use:     "merge-gatekeeper",
		Short:   "Get more refined merge control",
		Version: version,
	}
	cmd.PersistentFlags().StringVarP(&ghToken, "token", "t", "", "GitHub token (or set GITHUB_TOKEN env)")
	cmd.PersistentFlags().BoolVar(&flagDebug, "debug", false, "enable debug logging (also enabled when ACTIONS_STEP_DEBUG or ACTIONS_RUNNER_DEBUG is true)")

	cmd.AddCommand(validateCmd())

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
	)
	defer cancel()

	if err := cmd.ExecuteContext(ctx); err != nil {
		return err
	}
	return nil
}
