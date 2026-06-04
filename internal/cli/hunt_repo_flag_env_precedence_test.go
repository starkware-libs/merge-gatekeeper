package cli

import (
	"testing"
)

// snapshotFlagGlobals saves the package-level flag variables that
// validateCmd() resets as a side effect of re-registering its flags (UintVar
// and friends write the default into the bound variable immediately), and
// returns a restore func. Without this, constructing the command mid-suite
// silently bumps timeoutSecond back to 600 for every later test.
func snapshotFlagGlobals() func() {
	oldRepo, oldRef, oldSelf, oldIgnored := ghRepo, ghRef, selfJobName, ignoredJobs
	oldTimeout, oldInterval := timeoutSecond, validateIntervalSeconds
	oldConfirm, oldGrace := confirmPolls, emptyGraceSeconds
	return func() {
		ghRepo, ghRef, selfJobName, ignoredJobs = oldRepo, oldRef, oldSelf, oldIgnored
		timeoutSecond, validateIntervalSeconds = oldTimeout, oldInterval
		confirmPolls, emptyGraceSeconds = oldConfirm, oldGrace
	}
}

// Theory: validateCmd's PreRun unconditionally overwrites ghRepo with the
// GITHUB_REPOSITORY environment variable. Inside GitHub Actions that variable
// is ALWAYS set, so an explicit `--repo other-owner/other-repo` (the
// documented flag for cross-repo gatekeeping, also used by `make go-run`) is
// silently ignored and the gatekeeper validates the wrong repository — where
// the target ref's checks don't exist, yielding empty listings and a false
// green.
//
// Expected correct behavior: standard precedence — an explicitly passed flag
// beats the ambient environment variable; the env var only fills the gap when
// no flag is given.
func Test_Hunt_RepoFlagBeatsEnv(t *testing.T) {
	defer snapshotFlagGlobals()()
	t.Setenv("GITHUB_REPOSITORY", "env-owner/env-repo")

	cmd := validateCmd()
	if err := cmd.ParseFlags([]string{"--repo", "flag-owner/flag-repo", "--ref", "deadbeef"}); err != nil {
		t.Fatalf("ParseFlags() returned an unexpected error: %v", err)
	}
	cmd.PreRun(cmd, nil)

	if ghRepo != "flag-owner/flag-repo" {
		t.Errorf("explicit --repo flag must beat the ambient GITHUB_REPOSITORY env var; "+
			"got repo %q (the gatekeeper would silently validate the wrong repository)", ghRepo)
	}
}

// The env var must still apply when the flag is absent (the normal in-Action
// path, where action.yml never passes --repo).
func Test_Hunt_RepoEnvFillsGapWithoutFlag(t *testing.T) {
	defer snapshotFlagGlobals()()
	ghRepo = ""
	t.Setenv("GITHUB_REPOSITORY", "env-owner/env-repo")

	cmd := validateCmd()
	if err := cmd.ParseFlags([]string{"--ref", "deadbeef"}); err != nil {
		t.Fatalf("ParseFlags() returned an unexpected error: %v", err)
	}
	cmd.PreRun(cmd, nil)

	if ghRepo != "env-owner/env-repo" {
		t.Errorf("GITHUB_REPOSITORY must be used when --repo is not passed; got repo %q", ghRepo)
	}
}
