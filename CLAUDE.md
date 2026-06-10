# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Merge Gatekeeper is a GitHub Action (shipped as a CLI binary run in a Docker container) that
gates PR merges on CI status. It runs as one job in a PR, polls the status of *all other*
triggered CI jobs at the PR head, and fails if any of them fail. This lets a monorepo use a
single branch-protection rule instead of enumerating conditionally-run jobs. Module path:
`github.com/starkware-libs/merge-gatekeeper`.

## Commands

```bash
make go-build        # CGO_ENABLED=0 go build .  -> ./merge-gatekeeper
make test            # go test ./...
go build ./...       # build all packages
go test ./...        # run all tests

# Run a single package / single test
go test ./internal/validators/status/ -run TestName -v

# Run the validator locally (needs a token with repo read scope)
GITHUB_TOKEN=... make go-run REF=main REPO=starkware-libs/merge-gatekeeper IGNORED=""
# equivalently:
./merge-gatekeeper validate --token=$GITHUB_TOKEN --ref main --repo owner/repo

make docker-build    # docker build -t merge-gatekeeper:latest .
make docker-run      # build + run the action in a container
```

There is no linter configured (no `.golangci.*`). CI (`.github/workflows/build-ci.yaml`,
Go 1.25.x) only runs `go build ./...` then `go test ./...`. The action version lives in
`version.txt` (embedded into the binary at compile time via `//go:embed` in `main.go`).

## Architecture

Entry point `main.go` -> `internal/cli`. Cobra root command (`cli.go`) wires global
`--token`/`--debug` flags and registers the single `validate` subcommand (`validate.go`).
`action.yml` maps the GitHub Action inputs to these same CLI flags; keep flag names,
defaults, and `action.yml` in sync when changing either.

Layered structure:

- `internal/cli/validate.go` — owns the **polling loop**. On each tick it runs the
  validator(s), fails fast on a red status, and on all-green requires the world to stay
  stable across `--confirm-polls` consecutive polls before declaring success.
- `internal/validators/status/` — the core. `validator.go` fetches and reconciles CI state
  from GitHub; `status.go` categorizes jobs and computes the **fingerprint** used for
  quiescence; `option.go` holds config.
- `internal/github/github.go` — thin wrapper over `go-github/v84` exposing the ~5 endpoints
  used (combined status, check runs, workflow runs, workflow jobs, resolve-ref-to-SHA) with
  a shared retry/backoff wrapper.
- `internal/validators/transient.go` (`TransientError`) and `refgone.go` (`RefGoneError`) —
  error *types* that steer the loop: transient = keep polling, refgone = terminal.
- `internal/ticker`, `internal/multierror` — small helpers (instant-first-tick ticker;
  error aggregation).

### Key concepts (the non-obvious parts)

- **Quiescence / fingerprint.** GitHub's API is eventually consistent, so one green poll can
  predate jobs that haven't appeared yet. Success requires `confirm-polls` (default 3)
  consecutive green polls observing the *same* world. The fingerprint = resolved head SHA +
  sorted set of gated job names (NUL-separated). A mid-run push or newly-materialized job
  changes the fingerprint and **resets the streak**.
- **Empty grace.** If *no* jobs are discovered, the loop keeps polling for `--empty-grace`
  (default 30s) before trusting the empty set as success — CI may simply not have shown up yet.
- **Merge-queue refs** (`gh-readonly-queue/...`). These are deleted the instant the batch
  merges. If the ref disappears *after* the world was ever observed green on such a ref,
  that's treated as **success** (the merge preempted confirmation), not failure. This is
  gated on the sticky `sawGreen` latch in `validate.go`, **not** on the confirmation streak:
  the terminal `RefGoneError` is always preceded by transient 404 polls that reset the
  streak to 0, so a streak-based guard would never fire on the real path.
- **Ref deletion** elsewhere: tracked across consecutive 404s and becomes a terminal
  `RefGoneError` (fail fast) rather than ghost-polling forever.
- **`self`** (default `merge-gatekeeper`) and **`ignored`** jobs are excluded from gating —
  `self` to avoid waiting on itself, `ignored` for informational/optional jobs.
- **Stale-run filtering, matrix-parent heuristic, duplicate-name guard, cross-workflow name
  collisions.** `validator.go` cross-references workflow runs to drop check-runs from
  superseded suites, ignore stuck matrix *parent* jobs, fail loudly on duplicate display
  names within a workflow, and disambiguate same-named jobs across workflows
  (`"Job [WorkflowName]"`). These exist to avoid false "still pending"/false failures — be
  careful preserving real terminal (success/failure) signals when touching this logic.

### Testing notes

Tests are heaviest under `internal/validators/status/` and `internal/cli/` and lean
adversarial (edge cases around the behaviors above). GitHub interactions are exercised
through mocks in `internal/github/mock/` and `internal/validators/.../mock/`. When changing
validation behavior, add/adjust table cases there rather than hitting the real API.
