package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/ticker"
	"github.com/starkware-libs/merge-gatekeeper/internal/validators"
	"github.com/starkware-libs/merge-gatekeeper/internal/validators/status"
)

const defaultSelfJobName = "merge-gatekeeper"

// These variables will be set by command line flags.
var (
	ghRepo              string // e.g. owner/repo
	ghRef               string
	timeoutSecond       uint
	validateIntervalSeconds uint
	selfJobName         string
	ignoredJobs         string
	confirmPolls        uint
	emptyGraceSeconds   uint
)

func validateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate other github actions job",
		PreRun: func(cmd *cobra.Command, args []string) {
			// GITHUB_REPOSITORY only fills the gap when --repo is not passed:
			// inside GitHub Actions the env var is always set, and letting it
			// override an explicit flag would silently point the gatekeeper at
			// the wrong repository.
			if cmd.Flags().Changed("repo") {
				return
			}
			str := os.Getenv("GITHUB_REPOSITORY")
			if len(str) != 0 {
				ghRepo = str
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			token := ghToken
			if token == "" {
				token = os.Getenv("GITHUB_TOKEN")
			}
			if token == "" {
				return fmt.Errorf("github token is required: set --token or GITHUB_TOKEN environment variable")
			}

			owner, repo := ownerAndRepository(ghRepo)
			if len(owner) == 0 || len(repo) == 0 {
				return fmt.Errorf("github owner or repository is empty. owner: %s, repository: %s", owner, repo)
			}

			opts := []status.Option{
				status.WithSelfJob(selfJobName),
				status.WithGitHubOwnerAndRepo(owner, repo),
				status.WithGitHubRef(ghRef),
				status.WithIgnoredJobs(ignoredJobs),
			}
			if isDebugEnabled() {
				opts = append(opts, status.WithDebugLog(func(format string, args ...interface{}) {
					// Log with visible prefix so output shows when user passes debug: true. GitHub only shows
					// ::debug:: when ACTIONS_STEP_DEBUG is set (repo secret), so we use a normal prefix.
					fmt.Fprintf(os.Stderr, "[merge-gatekeeper debug] "+format, args...)
				}))
			}

			statusValidator, err := status.CreateValidator(github.NewClient(ctx, token), opts...)
			if err != nil {
				return fmt.Errorf("failed to create validator: %w", err)
			}

			cmd.SilenceUsage = true
			return doValidateCmd(ctx, cmd, statusValidator)
		},
	}

	cmd.PersistentFlags().StringVarP(&selfJobName, "self", "s", defaultSelfJobName, "set self job name")

	cmd.PersistentFlags().StringVarP(&ghRepo, "repo", "r", "", "set github repository")

	cmd.PersistentFlags().StringVar(&ghRef, "ref", "", "set ref of github repository. the ref can be a SHA, a branch name, or tag name")
	cmd.MarkPersistentFlagRequired("ref")

	cmd.PersistentFlags().UintVar(&timeoutSecond, "timeout", 600, "set validate timeout second")
	cmd.PersistentFlags().UintVar(&validateIntervalSeconds, "interval", 5, "set validate interval second")

	cmd.PersistentFlags().StringVarP(&ignoredJobs, "ignored", "i", "", "set ignored jobs (comma-separated list)")

	cmd.PersistentFlags().UintVar(&confirmPolls, "confirm-polls", 3, "consecutive green polls observing the same jobs required before reporting success")
	cmd.PersistentFlags().UintVar(&emptyGraceSeconds, "empty-grace", 30, "when no jobs are discovered, require the empty result to hold for this many seconds before reporting success")

	return cmd
}

// isMergeQueueRef reports whether ref names a GitHub merge-queue branch
// (gh-readonly-queue/...), with or without the refs/heads/ prefix. Merge-
// queue branches are deleted by GitHub the moment their batch merges or is
// dequeued — while ordinary branches disappearing mid-run is an anomaly.
func isMergeQueueRef(ref string) bool {
	return strings.HasPrefix(strings.TrimPrefix(ref, "refs/heads/"), "gh-readonly-queue/")
}

func ownerAndRepository(fullName string) (owner string, repo string) {
	sp := strings.Split(fullName, "/")
	switch len(sp) {
	case 1:
		return sp[0], ""
	case 2:
		return sp[0], sp[1]
	default:
		return sp[0], strings.Join(sp[1:], "/")
	}
}

func debug(logger logger, name string) func() {
	logger.Printf("Start processing %s....\n", name)
	return func() {
		logger.Printf("Finish %s processing.\n", name)
	}
}

func doValidateCmd(ctx context.Context, logger logger, vs ...validators.Validator) error {
	// Reject nonsensical values up front: interval=0 would panic in
	// time.NewTicker and timeout=0 would die with a bare "context deadline
	// exceeded" — both one action-input typo away.
	if validateIntervalSeconds == 0 {
		return errors.New("invalid configuration: interval must be at least 1 second")
	}
	if timeoutSecond == 0 {
		return errors.New("invalid configuration: timeout must be at least 1 second")
	}
	if confirmPolls == 0 {
		return errors.New("invalid configuration: confirm-polls must be at least 1")
	}
	// Conservative startup sanity bound: a healthy success path needs streak
	// room ((confirmPolls-1) intervals) and, for an empty world, the grace —
	// a timeout that cannot fit their sum is a config that mostly expires.
	minBudget := (confirmPolls-1)*validateIntervalSeconds + emptyGraceSeconds
	if minBudget >= timeoutSecond {
		return fmt.Errorf(
			"invalid configuration: confirm-polls=%d at interval=%ds plus empty-grace=%ds needs at least %ds, but timeout is %ds",
			confirmPolls, validateIntervalSeconds, emptyGraceSeconds, minBudget, timeoutSecond)
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecond)*time.Second)
	defer cancel()

	intervalTicker := ticker.NewInstantTicker(time.Duration(validateIntervalSeconds) * time.Second)
	defer intervalTicker.Stop()

	// Quiescence state: GitHub's listings are eventually consistent, so a
	// single all-green poll can predate work that simply has not indexed yet
	// (a run with no check runs, a partially materialized suite, a stale
	// re-run attempt). Success therefore requires confirmPolls consecutive
	// green polls that observed the SAME world — fingerprints carry the
	// resolved head SHA and the gated job set, so a branch advancing mid-run
	// or CI materializing late resets the streak automatically. The empty-set
	// grace is anchored at the CURRENT streak's start, not at process start:
	// a mid-run push is a new trigger, and the empty world it briefly exposes
	// deserves the same grace the original trigger got. Red stays fail-fast:
	// validation failures abort on the poll that sees them.
	var streak uint
	var streakStart time.Time
	var lastFingerprint string
	var haveFingerprint bool
	// lastObservedAllGreen records whether the most recent poll that actually
	// observed the world saw it all-green. Unlike streak, it is NOT reset by a
	// transient poll (one that observed nothing) — only by a real observation
	// that was not green. The merge-queue ref-gone success path keys on this:
	// a deleted ref is reached through refGoneTerminalPolls transient 404s that
	// would zero any streak, so "did we last see green" is the only durable
	// signal that the batch merged after our verdict was green.
	var lastObservedAllGreen bool

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-intervalTicker.C():
			allGreen := true
			observedNothing := false
			totalTracked := 0
			fingerprints := make([]string, 0, len(vs))
			for _, v := range vs {
				st, err := validate(ctx, v, logger)
				if err != nil {
					// A merge-queue ref deleted after we last saw an all-green
					// world means the batch merged out from under the
					// confirmation window: the verdict can no longer gate
					// anything, so report success rather than failing a merge
					// that already happened. Strictly scoped: a gate whose last
					// real observation was not green stays red (the queue merged
					// something this gate hadn't confirmed), and a deleted
					// NON-queue ref stays red.
					var refGone *validators.RefGoneError
					if errors.As(err, &refGone) && isMergeQueueRef(ghRef) && lastObservedAllGreen {
						logger.Println("The merge queue deleted the ref after a green poll — the batch merged; reporting success.")
						return nil
					}
					return err
				}
				// A transient API error yields no status: this poll observed
				// nothing, so it cannot extend the quiescence streak — but it
				// must also not erase the memory of the last world we DID
				// observe (the ref-gone decision above depends on it; a deleted
				// ref is preceded by exactly such transient 404s).
				if st == nil {
					observedNothing = true
					allGreen = false
					break
				}
				if !st.IsSuccess() {
					allGreen = false
					break
				}
				totalTracked += st.TrackedJobs()
				fingerprints = append(fingerprints, st.Fingerprint())
			}
			// Update the durable "last observed green" memory: set true on a
			// fully-green poll, false on a poll that observed a non-green world,
			// unchanged on a poll that observed nothing (transient).
			if allGreen {
				lastObservedAllGreen = true
			} else if !observedNothing {
				lastObservedAllGreen = false
			}
			if !allGreen {
				streak = 0
				haveFingerprint = false
				logger.PrintErrln("")
				logger.PrintErrln("  WARNING: Validation is yet to be completed. This is most likely due to some other jobs still running.")
				logger.PrintErrf("           Waiting for %d seconds before retrying.\n\n", validateIntervalSeconds)
				break
			}

			fingerprint := strings.Join(fingerprints, "\x01")
			if haveFingerprint && fingerprint == lastFingerprint {
				streak++
			} else {
				streak = 1
				streakStart = time.Now()
			}
			lastFingerprint = fingerprint
			haveFingerprint = true

			// An empty gated set right after the trigger usually means CI has
			// not materialized into the listings yet — hold the gate for the
			// grace period before trusting it. Measured from the streak start,
			// so the grace re-arms whenever the observed world changes.
			graceMet := totalTracked > 0 ||
				time.Since(streakStart) >= time.Duration(emptyGraceSeconds)*time.Second

			if streak >= confirmPolls && graceMet {
				logger.Println("All validations were successful!")
				return nil
			}

			if !graceMet {
				logger.PrintErrf("  green poll %d/%d, but no jobs were discovered yet; holding for the %ds empty-set grace (%.0fs elapsed)\n\n",
					streak, confirmPolls, emptyGraceSeconds, time.Since(streakStart).Seconds())
			} else {
				logger.PrintErrf("  green poll %d/%d; confirming the result holds before reporting success\n\n",
					streak, confirmPolls)
			}
		}
	}
}

func validate(ctx context.Context, v validators.Validator, logger logger) (validators.Status, error) {
	defer debug(logger, "validator: "+v.Name())()

	st, err := v.Validate(ctx)
	if err != nil {
		// Infrastructure hiccups (GitHub API failures) must not turn the
		// gatekeeper red while it still has timeout budget — warn and let the
		// loop poll again. Real validation outcomes (failed/cancelled jobs,
		// invalid configuration) abort immediately as before.
		if validators.IsTransient(err) {
			logger.PrintErrf("  WARNING: transient GitHub API error, will retry on the next poll: %v\n", err)
			return nil, nil
		}
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	logger.Println(st.Detail())

	return st, nil
}
