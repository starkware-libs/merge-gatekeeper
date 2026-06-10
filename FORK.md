# Fork changes

This repository (`starkware-libs/merge-gatekeeper`) is a fork of
[`upsidr/merge-gatekeeper`](https://github.com/upsidr/merge-gatekeeper). It diverged at
upstream commit `12e1af4` and has since added **~7,700 lines** across 65+ commits, almost
all hardening the validation core.

Upstream was a straightforward check-poller: list all checks at the PR head, fail if any are
red, succeed once they're all green. The fork rebuilt that core (`validator.go`, `status.go`)
and wrapped it in a confirmation/retry/grace layer so it stops producing **false greens** and
**false reds** under GitHub's eventually-consistent API, matrix jobs, re-runs, and merge
queues.

## New configuration

Upstream exposed only `self`, `repo`, `ref`, `timeout`, `ignored`, `interval`. The fork adds:

| Flag / input     | Default | Purpose                                                                 |
| ---------------- | ------- | ----------------------------------------------------------------------- |
| `--confirm-polls`| `3`     | Require N consecutive green polls observing the *same* world before success. |
| `--empty-grace`  | `30` (s)| When zero jobs are discovered, keep polling this long before trusting the empty set. |
| `--debug`        | `false` | Verbose logging; also auto-enabled by `ACTIONS_STEP_DEBUG` / `ACTIONS_RUNNER_DEBUG`. |

Also: an explicit `--repo` now beats the `GITHUB_REPOSITORY` env var, and `interval=0` /
`timeout=0` are rejected with clear errors.

## Eventual-consistency hardening (the core problem)

GitHub's check/status listings lag reality, so a single green snapshot could merge a PR with
CI still pending. The fork addresses this with:

- **Quiescence / fingerprinting.** Success requires `confirm-polls` consecutive green polls
  over the *same* world. The fingerprint = resolved head SHA + sorted set of gated job names
  (NUL-separated); any change (mid-run push, newly-materialized job) resets the streak.
- **Empty-set grace.** A freshly-triggered PR whose CI hasn't appeared in the API yet keeps
  polling for `--empty-grace` rather than being declared green immediately.
- **Transient-error handling.** New `internal/validators/transient.go` (`TransientError`)
  plus a retry/backoff wrapper in `internal/github/github.go`, including secondary rate
  limits (429 / 403 + `Retry-After`). API hiccups keep polling instead of going red.

## Matrix jobs, re-runs, and superseded suites

A large cluster of fixes for structures the original mishandled:

- **Matrix-parent heuristic.** Ignore the phantom parent check (e.g. `test` alongside
  `test (a, x)`) that can hang in `pending`/`queued` after cancellation — while *not*
  dropping a real job that merely shares that name (cross-referenced against the workflow's
  job listing). Scoped to a single workflow.
- **Stale / superseded suites.** Treat successes from a superseded suite as pending while
  the superseding re-run is in flight; drop check runs from old, cancelled suites.
- **Duplicate-named-jobs guard.** Fail loudly when one workflow has two jobs with the same
  display name (a config error that would otherwise let one job's success mask another's
  failure). `self` / `ignored` names are exempt.
- **Name disambiguation.** Keep same-named checks from different workflows, check apps, and
  unknown suites independent; render cross-workflow collisions as `"Job [WorkflowName]"`.
- **Failures without check runs.** Surface workflow runs that fail or get approval-blocked
  (`action_required`) without ever producing a check run.

## Ref deletion and merge queues (most recent work)

GitHub merge queues run CI on a temporary `gh-readonly-queue/<base>/<sha>` branch and delete
it the instant the batch merges. The fork handles the resulting race with two complementary
rules:

- **Fail fast on a deleted ref** (`internal/validators/refgone.go`, `RefGoneError`). A ref
  that resolved earlier and then 404s for several consecutive polls is terminal — never
  transient — so the gatekeeper doesn't ghost-poll the remaining timeout. (`RefGoneError`
  deliberately has no `Unwrap()`: its inner error is a `TransientError` carried for the
  message only; exposing it would make the loop retry a terminal condition.)
- **Report success when a merge-queue ref disappears after going green.** In the loop's
  error path, a `RefGoneError` is treated as success **only** when all three hold: the ref is
  a `gh-readonly-queue/...` branch (`isMergeQueueRef`) **and** the world was ever observed
  green (the sticky `sawGreen` latch). That means the batch merged out from under the
  confirmation window after the world was seen green — failing it would red a PR that already
  merged. A non-queue ref vanishing, or a queue ref that never saw green, stays red.
  (`sawGreen` rather than the confirmation streak: the transient 404 polls that accumulate
  into the terminal `RefGoneError` reset the streak to 0 first, so a streak-based guard would
  never fire on the real path.)

## Miscellaneous

- New `.github/workflows/matrix-test.yaml` to exercise matrix behavior in CI.
- `go.mod` dependency bumps; generic `withRetry`; dedup check runs by check-suite ID;
  pagination fixes (stop on empty page); identifier cleanups.
- Project ownership / module path moved to `github.com/starkware-libs/merge-gatekeeper`.
