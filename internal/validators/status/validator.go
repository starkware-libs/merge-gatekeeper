package status

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/multierror"
	"github.com/starkware-libs/merge-gatekeeper/internal/validators"
)

type jobState string

const (
	successState   jobState = "success"
	errorState     jobState = "error"
	failureState   jobState = "failure"
	pendingState   jobState = "pending"
	cancelledState jobState = "cancelled"
)

// NOTE: https://docs.github.com/en/rest/reference/checks
const (
	checkRunCompletedStatus = "completed"
)
const (
	checkRunNeutralConclusion = "neutral"
	checkRunSuccessConclusion = "success"
	checkRunSkipConclusion    = "skipped"
	checkRunCancelledConclusion = "cancelled"
)

const (
	maxStatusesPerPage  = 100
	maxCheckRunsPerPage = 100
)

var (
	ErrInvalidCombinedStatusResponse = errors.New("github combined status response is invalid")
	ErrInvalidCheckRunResponse       = errors.New("github checkRun response is invalid")
)

type ghaStatus struct {
	Job   string
	State jobState
}

type statusValidator struct {
	repo        string
	owner       string
	ref         string
	selfJobName string
	ignoredJobs []string
	client      github.Client
	debugLog    DebugLog

	// duplicateCheckCached is set after detectDuplicateNamedJobs has run
	// successfully for this ref. The result is invariant per SHA, so we run
	// the check at most once per validator instance instead of every poll.
	duplicateCheckCached bool
}

func CreateValidator(c github.Client, opts ...Option) (validators.Validator, error) {
	sv := &statusValidator{
		client: c,
	}
	for _, opt := range opts {
		opt(sv)
	}
	if err := sv.validateFields(); err != nil {
		return nil, err
	}
	return sv, nil
}

func (sv *statusValidator) Name() string {
	return sv.selfJobName
}

func (sv *statusValidator) validateFields() error {
	errs := make(multierror.Errors, 0, 6)

	if len(sv.repo) == 0 {
		errs = append(errs, errors.New("repository name is empty"))
	}
	if len(sv.owner) == 0 {
		errs = append(errs, errors.New("repository owner is empty"))
	}
	if len(sv.ref) == 0 {
		errs = append(errs, errors.New("reference of repository is empty"))
	}
	if len(sv.selfJobName) == 0 {
		errs = append(errs, errors.New("self job name is empty"))
	}
	if sv.client == nil {
		errs = append(errs, errors.New("github client is empty"))
	}

	if len(errs) != 0 {
		return errs
	}

	return nil
}

func (sv *statusValidator) Validate(ctx context.Context) (validators.Status, error) {
	// One-time precondition: fail loud if any workflow has two YAML jobs that
	// share a display name. Same-suite same-name check runs are indistinguishable
	// from re-runs of one job via the API, so the dedup logic in listGhaStatuses
	// would silently drop one — masking CI signal. The YAML structure is fixed
	// for this ref, so cache the result after the first successful run.
	if !sv.duplicateCheckCached {
		if err := sv.detectDuplicateNamedJobs(ctx); err != nil {
			return nil, err
		}
		sv.duplicateCheckCached = true
	}

	ghaStatuses, err := sv.listGhaStatuses(ctx)
	if err != nil {
		return nil, err
	}

	st := &status{
		totalJobs:      make([]string, 0, len(ghaStatuses)),
		completeJobs:   make([]string, 0, len(ghaStatuses)),
		failedJobs:        make([]string, 0, len(ghaStatuses)/2),
		cancelledJobs:  make([]string, 0),
		ignoredJobs:    make([]string, 0, len(ghaStatuses)),
		succeeded:      true,
	}

	st.ignoredJobs = append(st.ignoredJobs, sv.ignoredJobs...)

	var successCnt int
	for _, ghaStatus := range ghaStatuses {
		var toIgnore bool
		for _, ignored := range sv.ignoredJobs {
			if ghaStatus.Job == ignored {
				toIgnore = true
				break
			}
		}

		// Ignored jobs and this job itself should be considered as success regardless of their statuses.
		if toIgnore || ghaStatus.Job == sv.selfJobName {
			successCnt++
			continue
		}

		st.totalJobs = append(st.totalJobs, ghaStatus.Job)

		switch ghaStatus.State {
		case successState:
			st.completeJobs = append(st.completeJobs, ghaStatus.Job)
			successCnt++
		case errorState, failureState:
			st.failedJobs = append(st.failedJobs, ghaStatus.Job)
		case cancelledState:
			st.cancelledJobs = append(st.cancelledJobs, ghaStatus.Job)
		}
	}
	if len(st.failedJobs) != 0 || len(st.cancelledJobs) != 0 {
		return nil, errors.New(st.Detail())
	}

	if len(ghaStatuses) != successCnt {
		st.succeeded = false
		return st, nil
	}

	return st, nil
}

func (sv *statusValidator) getCombinedStatus(ctx context.Context) ([]*github.RepoStatus, error) {
	var combined []*github.RepoStatus
	page := 1
	for {
		c, _, err := sv.client.GetCombinedStatus(ctx, sv.owner, sv.repo, sv.ref, &github.ListOptions{PerPage: maxStatusesPerPage, Page: page})
		if err != nil {
			return nil, err
		}
		// Guard against an inconsistent listing (total_count above the number
		// of items actually returned): an empty page ends pagination, as in
		// listWorkflowRunsForRef — otherwise this loop would hammer the API
		// until the gatekeeper's timeout.
		if len(c.Statuses) == 0 {
			break
		}
		combined = append(combined, c.Statuses...)
		if c.GetTotalCount() <= len(combined) {
			break
		}
		page++
	}
	return combined, nil
}

func (sv *statusValidator) listCheckRunsForRef(ctx context.Context) ([]*github.CheckRun, error) {
	var runResults []*github.CheckRun
	page := 1
	filterAll := "all"
	for {
		cr, _, err := sv.client.ListCheckRunsForRef(ctx, sv.owner, sv.repo, sv.ref, &github.ListCheckRunsOptions{
			ListOptions: github.ListOptions{
				Page:    page,
				PerPage: maxCheckRunsPerPage,
			},
			Filter: &filterAll,
		})
		if err != nil {
			return nil, err
		}
		// Empty page ends pagination — see the matching guard in
		// getCombinedStatus and listWorkflowRunsForRef.
		if len(cr.CheckRuns) == 0 {
			break
		}
		runResults = append(runResults, cr.CheckRuns...)
		if cr.GetTotal() <= len(runResults) {
			break
		}
		page++
	}
	return runResults, nil
}

const maxWorkflowRunsPerPage = 100

func (sv *statusValidator) listWorkflowRunsForRef(ctx context.Context) ([]*github.WorkflowRun, error) {
	var runs []*github.WorkflowRun
	page := 1
	for {
		wr, _, err := sv.client.ListRepositoryWorkflowRuns(ctx, sv.owner, sv.repo, &github.ListWorkflowRunsOptions{
			HeadSHA:     sv.ref,
			ListOptions: github.ListOptions{Page: page, PerPage: maxWorkflowRunsPerPage},
		})
		if err != nil {
			return nil, err
		}
		if len(wr.WorkflowRuns) == 0 {
			break
		}
		runs = append(runs, wr.WorkflowRuns...)
		if wr.GetTotalCount() <= len(runs) {
			break
		}
		page++
	}
	return runs, nil
}

const maxWorkflowJobsPerPage = 100

// listAllWorkflowJobs paginates through all jobs of a workflow run.
// filter="latest" returns one entry per YAML job from the latest attempt,
// which is what duplicate-name detection wants — same-named entries in this
// list mean the YAML literally defines two jobs with that display name.
func (sv *statusValidator) listAllWorkflowJobs(ctx context.Context, runID int64, filter string) ([]*github.WorkflowJob, error) {
	var jobs []*github.WorkflowJob
	page := 1
	for {
		result, _, err := sv.client.ListWorkflowJobs(ctx, sv.owner, sv.repo, runID, &github.ListWorkflowJobsOptions{
			Filter: filter,
			ListOptions: github.ListOptions{
				Page:    page,
				PerPage: maxWorkflowJobsPerPage,
			},
		})
		if err != nil {
			return nil, err
		}
		if result == nil || len(result.Jobs) == 0 {
			break
		}
		jobs = append(jobs, result.Jobs...)
		if result.GetTotalCount() <= len(jobs) {
			break
		}
		page++
	}
	return jobs, nil
}

// detectDuplicateNamedJobs fails the gatekeeper loudly when any workflow on
// this ref defines two YAML jobs that share a display name. Same-suite
// same-name check runs are ambiguous via the GitHub API — they could be
// re-runs of one job or two distinct YAML jobs. Rather than silently picking
// one (which would drop CI signal), we use the workflow_jobs API at
// filter=latest to count names within a single workflow run; same-named
// entries there mean the YAML really has duplicates.
//
// The result is invariant for a given SHA, so the caller caches success. If
// any API call fails, we propagate the error and rely on the caller to retry
// on the next poll.
func (sv *statusValidator) detectDuplicateNamedJobs(ctx context.Context) error {
	workflowRuns, err := sv.listWorkflowRunsForRef(ctx)
	if err != nil {
		return fmt.Errorf("duplicate-named-jobs check: failed to fetch workflow runs: %w", err)
	}
	if len(workflowRuns) == 0 {
		return nil
	}

	// Pick one run per workflow_id. Any run for this SHA shares the same YAML,
	// so we just take the highest run_number to get the most recent metadata.
	type pickedRun struct {
		runID        int64
		workflowName string
		runNumber    int
	}
	perWorkflow := make(map[int64]pickedRun)
	for _, wr := range workflowRuns {
		if wr.WorkflowID == nil || wr.ID == nil {
			continue
		}
		wid := *wr.WorkflowID
		runNumber := 0
		if wr.RunNumber != nil {
			runNumber = *wr.RunNumber
		}
		if existing, ok := perWorkflow[wid]; ok && runNumber <= existing.runNumber {
			continue
		}
		workflowName := ""
		if wr.Name != nil {
			workflowName = *wr.Name
		}
		perWorkflow[wid] = pickedRun{
			runID:        *wr.ID,
			workflowName: workflowName,
			runNumber:    runNumber,
		}
	}

	for _, pr := range perWorkflow {
		jobs, err := sv.listAllWorkflowJobs(ctx, pr.runID, "latest")
		if err != nil {
			return fmt.Errorf(
				"duplicate-named-jobs check: failed to list jobs for workflow %q (run %d): %w",
				pr.workflowName, pr.runID, err)
		}
		nameCount := make(map[string]int)
		for _, job := range jobs {
			if job.Name == nil {
				continue
			}
			nameCount[*job.Name]++
		}
		for name, count := range nameCount {
			if count > 1 {
				return fmt.Errorf(
					"workflow %q defines %d jobs with the display name %q in a single run; "+
						"the gatekeeper cannot reliably distinguish same-workflow same-name jobs "+
						"from re-runs of one job. Rename the duplicates in the workflow YAML so "+
						"each job has a unique name.",
					pr.workflowName, count, name)
			}
		}
		sv.debugf("merge-gatekeeper [debug] duplicate-named-jobs check passed for workflow %q (%d jobs)\n",
			pr.workflowName, len(jobs))
	}
	return nil
}

// suiteWorkflowInfo identifies the workflow run that owns a given check suite.
// Used to disambiguate jobs that share a name across different workflows, and
// to keep runs of one workflow from different trigger events independent.
type suiteWorkflowInfo struct {
	workflowID   int64
	workflowName string
	event        string
}

// workflowEventKey identifies one stream of runs of a workflow: re-triggers
// for the same event supersede each other, while runs from different events
// (e.g. `on: [push, pull_request]` firing twice at the same SHA) are
// independent, concurrently-legitimate CI — the pull_request run executes the
// merge ref and the push run the branch head, so neither stands in for the
// other.
type workflowEventKey struct {
	workflowID int64
	event      string
}

// workflowLatest describes a workflow's latest non-cancelled run for this ref.
type workflowLatest struct {
	runNumber    int
	runAttempt   int
	suiteID      int64
	status       string
	runStartedAt *github.Timestamp
}

// refWorkflowState is what the workflow-runs listing says about this ref: which
// workflow owns each check suite and which suite holds each workflow's latest
// non-cancelled run. nil when no workflow runs were fetched.
type refWorkflowState struct {
	suiteToWorkflow       map[int64]suiteWorkflowInfo
	latestSuiteByWorkflow map[workflowEventKey]int64
	latestRunBySuite      map[int64]*workflowLatest
}

// eventOf returns the trigger event of a workflow run ("" when absent).
func eventOf(wr *github.WorkflowRun) string {
	if wr.Event != nil {
		return *wr.Event
	}
	return ""
}

const githubActionsAppSlug = "github-actions"

// isGitHubActionsCheckRun reports whether the check run was created by GitHub
// Actions, as opposed to a third-party check app (which never has workflow runs).
func isGitHubActionsCheckRun(run *github.CheckRun) bool {
	return run.GetApp().GetSlug() == githubActionsAppSlug
}

func suiteIDOf(run *github.CheckRun) int64 {
	if run.CheckSuite != nil && run.CheckSuite.ID != nil {
		return *run.CheckSuite.ID
	}
	return 0
}

func statusOf(run *github.CheckRun) string {
	if run.Status == nil {
		return "unknown"
	}
	return *run.Status
}

// isSettledSuccess reports whether the check run holds a conclusion that stays
// valid across superseding runs — "re-run failed jobs" doesn't repeat succeeded
// jobs, so these are kept across suites. Within a suite, the stale-attempt
// check in filterStaleCheckRuns still overrides this while a newer attempt of
// the run is executing ("Re-run all jobs" repeats succeeded jobs too).
func isSettledSuccess(run *github.CheckRun) bool {
	if run.Status == nil || *run.Status != checkRunCompletedStatus || run.Conclusion == nil {
		return false
	}
	switch *run.Conclusion {
	case checkRunSuccessConclusion, checkRunNeutralConclusion, checkRunSkipConclusion:
		return true
	}
	return false
}

// convertToPending clones the check run as queued so a later poll re-evaluates it.
func convertToPending(run *github.CheckRun) *github.CheckRun {
	pendingStatus := "queued"
	converted := *run
	converted.Status = &pendingStatus
	converted.Conclusion = nil
	return &converted
}

// isStaleAttemptConclusion reports whether the check run's conclusion predates
// the current attempt of its own suite's run. Re-run attempts reuse the check
// suite, so until the new attempt recreates a check run, the API (filter=all)
// still reports the previous attempt's outcome.
func isStaleAttemptConclusion(run *github.CheckRun, latest *workflowLatest) bool {
	if latest.runAttempt <= 1 || latest.runStartedAt == nil {
		return false
	}
	if run.Status == nil || *run.Status != checkRunCompletedStatus {
		return false
	}
	return run.CompletedAt != nil && run.CompletedAt.Time.Before(latest.runStartedAt.Time)
}

// preferOverExisting decides whether run should replace existing as the tracked
// instance for a dedup key. The suite of the workflow's latest non-cancelled
// run (latestSuiteID, 0 when unknown) wins outright: suite-ID ordering alone is
// wrong when one of two duplicate triggers is cancelled — the cancelled run can
// hold the higher suite ID, and its pending-converted check runs would mask the
// surviving run's real results (sequencer PR #14205). Otherwise, suite IDs are
// assigned in chronological order within a workflow, so the highest suite ID is
// the latest re-run; within the same suite, run IDs break the tie because a
// cancelled run's job can be scheduled later than the replacement run's earlier
// jobs.
func preferOverExisting(run, existing *github.CheckRun, latestSuiteID int64) bool {
	thisSuiteID := suiteIDOf(run)
	existingSuiteID := suiteIDOf(existing)
	if latestSuiteID != 0 && thisSuiteID != existingSuiteID {
		if thisSuiteID == latestSuiteID {
			return true
		}
		if existingSuiteID == latestSuiteID {
			return false
		}
	}
	thisTiebreaker := thisSuiteID
	existingTiebreaker := existingSuiteID
	if thisTiebreaker == existingTiebreaker {
		if run.ID != nil {
			thisTiebreaker = *run.ID
		}
		if existing.ID != nil {
			existingTiebreaker = *existing.ID
		}
	}
	return thisTiebreaker > existingTiebreaker
}

// filterStaleCheckRuns removes or converts check runs whose state is stale
// relative to the workflow-runs listing for this ref. Successful conclusions
// (success/neutral/skipped) are kept across superseded and orphan suites —
// "re-run failed jobs" doesn't repeat succeeded jobs, so they stay valid —
// with one exception: while their own suite is executing a newer re-run
// attempt, they are as stale as any other conclusion (case 3 below), because
// "Re-run all jobs" re-executes succeeded jobs too. Three kinds of staleness
// are handled:
//
//  1. Superseded suites: a newer non-cancelled run of the same workflow AND
//     the same trigger event exists. Superseding run still in progress →
//     convert to pending (replacement may still produce a result); completed →
//     drop (job not needed anymore). Runs from different events are never
//     superseded by each other — `on: [push, pull_request]` legitimately runs
//     a workflow twice at one SHA and both results count.
//
//  2. Orphan suites: a github-actions check run whose suite has no workflow run
//     in the listing. Every Actions check run belongs to a workflow run, so the
//     listing is transiently inconsistent (observed seconds after a concurrency
//     cancellation, sequencer PR #14205) — convert terminal conclusions to
//     pending rather than fail on half-synced state.
//
//  3. Stale attempts: re-runs reuse the check suite, so after "re-run failed
//     jobs" the previous attempt's failures stay visible until the new attempt
//     recreates each check run (sequencer PR #14106). While the latest run is
//     executing attempt > 1, conclusions older than the attempt's start are
//     converted to pending — successes included, since "Re-run all jobs"
//     re-executes succeeded jobs and their stale successes would otherwise
//     green-light the gatekeeper mid-re-run. Once the run completes, surviving
//     old conclusions are final — their jobs were not part of the re-run.
//
// Also returns the refWorkflowState used for these decisions, for callers that
// need to disambiguate same-name jobs across workflows or prefer the latest
// suite during dedup. The state is nil when no workflow runs were fetched
// (third-party-only check runs or empty workflow runs response).
func (sv *statusValidator) filterStaleCheckRuns(ctx context.Context, runResults []*github.CheckRun) ([]*github.CheckRun, *refWorkflowState, error) {
	// Without any check suite there is nothing to correlate with workflow runs.
	hasSuites := false
	for _, run := range runResults {
		if suiteIDOf(run) != 0 {
			hasSuites = true
			break
		}
	}
	if !hasSuites {
		return runResults, nil, nil
	}

	// Fetch workflow runs for this commit. Requires actions: read permission.
	workflowRuns, err := sv.listWorkflowRunsForRef(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch workflow runs (actions: read permission required): %w", err)
	}
	if len(workflowRuns) == 0 {
		return runResults, nil, nil
	}

	// Build suite_id → workflow info map. Used by listGhaStatuses to dedup by
	// (workflow_id, name) so that two workflows defining a job with the same
	// name (e.g. both Committer-CI and Blockifier-CI defining "benchmarking")
	// are tracked independently rather than collapsed by suite-ID ordering.
	state := &refWorkflowState{
		suiteToWorkflow:       make(map[int64]suiteWorkflowInfo, len(workflowRuns)),
		latestSuiteByWorkflow: make(map[workflowEventKey]int64),
		latestRunBySuite:      make(map[int64]*workflowLatest),
	}
	for _, wr := range workflowRuns {
		if wr.CheckSuiteID == nil {
			continue
		}
		info := suiteWorkflowInfo{event: eventOf(wr)}
		if wr.WorkflowID != nil {
			info.workflowID = *wr.WorkflowID
		}
		if wr.Name != nil {
			info.workflowName = *wr.Name
		}
		state.suiteToWorkflow[*wr.CheckSuiteID] = info
	}

	// For each (workflow, event), find the latest non-cancelled run by
	// (RunNumber, RunAttempt). Scoped by event: only a re-trigger of the same
	// event supersedes a run — `on: [push, pull_request]` runs twice at one
	// SHA and both runs are current.
	perWorkflow := make(map[workflowEventKey]*workflowLatest)
	for _, wr := range workflowRuns {
		if wr.WorkflowID == nil || wr.CheckSuiteID == nil {
			continue
		}
		// Skip cancelled runs — they can't supersede anything.
		if wr.Conclusion != nil && *wr.Conclusion == "cancelled" {
			continue
		}
		rn := 0
		if wr.RunNumber != nil {
			rn = *wr.RunNumber
		}
		ra := 0
		if wr.RunAttempt != nil {
			ra = *wr.RunAttempt
		}
		st := ""
		if wr.Status != nil {
			st = *wr.Status
		}
		wek := workflowEventKey{workflowID: *wr.WorkflowID, event: eventOf(wr)}
		existing, ok := perWorkflow[wek]
		if !ok || rn > existing.runNumber || (rn == existing.runNumber && ra > existing.runAttempt) {
			perWorkflow[wek] = &workflowLatest{
				runNumber:    rn,
				runAttempt:   ra,
				suiteID:      *wr.CheckSuiteID,
				status:       st,
				runStartedAt: wr.RunStartedAt,
			}
		}
	}
	for wek, latest := range perWorkflow {
		state.latestSuiteByWorkflow[wek] = latest.suiteID
		state.latestRunBySuite[latest.suiteID] = latest
	}

	// Build supersededSuites: map from CheckSuiteID → superseding run's Status.
	// A suite is superseded if it belongs to a (workflow, event) that has a
	// newer non-cancelled run.
	supersededSuites := make(map[int64]string) // suiteID → superseding run status
	for _, wr := range workflowRuns {
		if wr.WorkflowID == nil || wr.CheckSuiteID == nil {
			continue
		}
		latest, ok := perWorkflow[workflowEventKey{workflowID: *wr.WorkflowID, event: eventOf(wr)}]
		if !ok {
			// All runs of this (workflow, event) are cancelled — nothing superseded.
			continue
		}
		if *wr.CheckSuiteID != latest.suiteID {
			supersededSuites[*wr.CheckSuiteID] = latest.status
		}
	}

	if len(supersededSuites) != 0 {
		sv.debugf("merge-gatekeeper [debug] superseded suites detected: %d suite(s) from %d workflow run(s)\n",
			len(supersededSuites), len(workflowRuns))
	}

	filtered := make([]*github.CheckRun, 0, len(runResults))
	for _, run := range runResults {
		suiteID := suiteIDOf(run)
		name := ""
		if run.Name != nil {
			name = *run.Name
		}

		// Stale attempt: the conclusion predates the in-progress re-run attempt
		// of its own suite — the attempt may be about to replace it. Computed
		// before the settled-success keep because it trumps it: "Re-run all
		// jobs" re-executes succeeded jobs, so until the attempt completes a
		// conclusion older than the attempt's start proves nothing.
		staleAttempt := false
		if latest, ok := state.latestRunBySuite[suiteID]; ok &&
			isStaleAttemptConclusion(run, latest) && latest.status != checkRunCompletedStatus {
			staleAttempt = true
		}

		if isSettledSuccess(run) && !staleAttempt {
			filtered = append(filtered, run)
			continue
		}

		if supersedingStatus, isSuperseded := supersededSuites[suiteID]; isSuperseded {
			if supersedingStatus != checkRunCompletedStatus {
				// Superseding run is still in progress — convert to pending.
				sv.debugf("merge-gatekeeper [debug] job=%s: converted to pending (superseded suite %d, superseding run %s)\n",
					name, suiteID, supersedingStatus)
				filtered = append(filtered, convertToPending(run))
			} else {
				// Superseding run completed — drop the stale check.
				sv.debugf("merge-gatekeeper [debug] job=%s: dropped from superseded suite %d (superseding run completed)\n",
					name, suiteID)
			}
			continue
		}

		// Orphan suite: the listing is transiently inconsistent — soften
		// terminal conclusions to pending and let a later poll decide.
		if _, known := state.suiteToWorkflow[suiteID]; !known && isGitHubActionsCheckRun(run) {
			if run.Status != nil && *run.Status == checkRunCompletedStatus {
				sv.debugf("merge-gatekeeper [debug] job=%s: converted to pending (suite %d missing from workflow-runs listing)\n",
					name, suiteID)
				filtered = append(filtered, convertToPending(run))
			} else {
				filtered = append(filtered, run)
			}
			continue
		}

		if staleAttempt {
			sv.debugf("merge-gatekeeper [debug] job=%s: converted to pending (conclusion predates attempt %d of suite %d)\n",
				name, state.latestRunBySuite[suiteID].runAttempt, suiteID)
			filtered = append(filtered, convertToPending(run))
			continue
		}

		filtered = append(filtered, run)
	}

	// Live workflow runs whose suites have produced no check runs yet would
	// otherwise be invisible: the gatekeeper tracks check runs only, so an
	// all-green poll could green-light the PR before such a run's jobs are
	// materialized (queued behind runner capacity or a concurrency group, or
	// in the window right after the trigger). Synthesize a pending placeholder
	// so the gatekeeper waits for the run's jobs to appear.
	suitesWithCheckRuns := make(map[int64]struct{}, len(runResults))
	for _, run := range runResults {
		if id := suiteIDOf(run); id != 0 {
			suitesWithCheckRuns[id] = struct{}{}
		}
	}
	for _, wr := range workflowRuns {
		if wr.CheckSuiteID == nil || wr.Status == nil || *wr.Status == checkRunCompletedStatus {
			continue
		}
		if _, ok := suitesWithCheckRuns[*wr.CheckSuiteID]; ok {
			continue
		}
		if _, superseded := supersededSuites[*wr.CheckSuiteID]; superseded {
			continue
		}
		workflowName := ""
		if wr.Name != nil {
			workflowName = *wr.Name
		}
		if workflowName == "" {
			workflowName = fmt.Sprintf("workflow:%d", state.suiteToWorkflow[*wr.CheckSuiteID].workflowID)
		}
		// Square brackets on purpose: a "Name (...)" placeholder would trip the
		// matrix-parent heuristic for a real job named like the workflow.
		placeholderName := fmt.Sprintf("%s [workflow starting]", workflowName)
		pendingStatus := "queued"
		sv.debugf("merge-gatekeeper [debug] suite %d (workflow %q, run status %s) has no check runs yet: tracking placeholder %q\n",
			*wr.CheckSuiteID, workflowName, *wr.Status, placeholderName)
		filtered = append(filtered, &github.CheckRun{
			Name:       &placeholderName,
			Status:     &pendingStatus,
			CheckSuite: &github.CheckSuite{ID: wr.CheckSuiteID},
		})
	}
	return filtered, state, nil
}

// isConfigExcludedName reports whether the raw job name is the gatekeeper's
// own job (--self) or an explicitly ignored job (--ignored). Validate treats
// these as always-successful by comparing the configured names against each
// tracked job's display name, so callers must keep such names un-renamed.
func (sv *statusValidator) isConfigExcludedName(name string) bool {
	if name == sv.selfJobName {
		return true
	}
	for _, ignored := range sv.ignoredJobs {
		if name == ignored {
			return true
		}
	}
	return false
}

func (sv *statusValidator) debugf(format string, args ...interface{}) {
	if sv.debugLog != nil {
		sv.debugLog(format, args...)
	}
}

func (sv *statusValidator) listGhaStatuses(ctx context.Context) ([]*ghaStatus, error) {
	combined, err := sv.getCombinedStatus(ctx)
	if err != nil {
		return nil, err
	}

	runResults, err := sv.listCheckRunsForRef(ctx)
	if err != nil {
		return nil, err
	}

	sv.debugf("merge-gatekeeper [debug] ref=%s owner=%s repo=%s combined_status_count=%d check_runs_count=%d\n",
		sv.ref, sv.owner, sv.repo, len(combined), len(runResults))

	// Pre-filter: remove/convert check runs whose state is stale relative to the
	// workflow-runs listing (superseded suites, orphan suites, re-run attempts).
	// workflowState maps each check_suite_id to the owning workflow's identity,
	// used below to dedup by (workflow_id, name) instead of by name alone. It is
	// nil when no workflow runs were fetched for this ref.
	runResults, workflowState, err := sv.filterStaleCheckRuns(ctx, runResults)
	if err != nil {
		return nil, err
	}

	// workflowInfoFor returns the workflow identity for a check run's check suite.
	// Returns the zero value (workflowID=0) when the run has no suite (third-party
	// integrations) or when no workflow run map was loaded.
	workflowInfoFor := func(run *github.CheckRun) suiteWorkflowInfo {
		if workflowState == nil || run.CheckSuite == nil || run.CheckSuite.ID == nil {
			return suiteWorkflowInfo{}
		}
		return workflowState.suiteToWorkflow[*run.CheckSuite.ID]
	}

	// Dedup key: (workflow_id, event, name). Within a single workflow-event
	// stream, re-runs and concurrency-cancelled runs collapse via the
	// suite-ID/run-ID tiebreaker below. Across workflows, jobs with the same
	// name remain independent — otherwise a "benchmarking" job in workflow A
	// could mask the state of a "benchmarking" job in workflow B (see PR
	// starkware-libs/sequencer#13859). Across events of one workflow, runs are
	// likewise independent: `on: [push, pull_request]` executes the same job
	// twice at one SHA, and the pull_request result must not stand in for the
	// push result (or vice versa).
	type workflowJobKey struct {
		workflowID int64
		event      string
		appID      int64 // set only when no workflow owns the suite (third-party check apps)
		name       string
	}
	latestRunByKey := make(map[workflowJobKey]*github.CheckRun)
	runCountByKey := make(map[workflowJobKey]int)
	for _, run := range runResults {
		if run.Name == nil || run.Status == nil {
			return nil, fmt.Errorf("%w name: %v, status: %v", ErrInvalidCheckRunResponse, run.Name, run.Status)
		}
		name := *run.Name
		info := workflowInfoFor(run)
		key := workflowJobKey{workflowID: info.workflowID, event: info.event, name: name}
		if info.workflowID == 0 {
			// No owning workflow run (third-party check app, or a suite the
			// listing doesn't know): scope by the posting app, so two apps
			// publishing the same check name stay independent — the same
			// masking class as PR starkware-libs/sequencer#13859, but across
			// check apps instead of workflows.
			key.appID = run.GetApp().GetID()
		}
		runCountByKey[key]++
		existing, ok := latestRunByKey[key]
		if !ok {
			latestRunByKey[key] = run
			continue
		}
		latestSuiteID := int64(0)
		if workflowState != nil {
			latestSuiteID = workflowState.latestSuiteByWorkflow[workflowEventKey{workflowID: key.workflowID, event: key.event}]
		}
		if preferOverExisting(run, existing, latestSuiteID) {
			sv.debugf("merge-gatekeeper [debug] job=%s workflow=%d: picked run id=%v suite=%v status=%s (replaced run id=%v suite=%v status=%s)\n",
				name, key.workflowID, run.ID, suiteIDOf(run), statusOf(run), existing.ID, suiteIDOf(existing), statusOf(existing))
			latestRunByKey[key] = run
		} else {
			sv.debugf("merge-gatekeeper [debug] job=%s workflow=%d: keeping run id=%v suite=%v status=%s (dropped run id=%v suite=%v status=%s)\n",
				name, key.workflowID, existing.ID, suiteIDOf(existing), statusOf(existing), run.ID, suiteIDOf(run), statusOf(run))
		}
	}

	// Detect cross-workflow name collisions: a raw name shared by check runs from
	// two or more distinct workflows. Such names get disambiguated below to keep
	// each workflow's job individually trackable. Names that only appear in one
	// workflow are unaffected so the common case keeps clean output.
	rawNameWorkflows := make(map[string]map[int64]struct{})
	for key := range latestRunByKey {
		workflows, ok := rawNameWorkflows[key.name]
		if !ok {
			workflows = make(map[int64]struct{})
			rawNameWorkflows[key.name] = workflows
		}
		workflows[key.workflowID] = struct{}{}
	}

	// Build ordered list of keys plus the display name for each.
	keys := make([]workflowJobKey, 0, len(latestRunByKey))
	for key := range latestRunByKey {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].name != keys[j].name {
			return keys[i].name < keys[j].name
		}
		if keys[i].workflowID != keys[j].workflowID {
			return keys[i].workflowID < keys[j].workflowID
		}
		if keys[i].event != keys[j].event {
			return keys[i].event < keys[j].event
		}
		return keys[i].appID < keys[j].appID
	})

	displayNames := make(map[workflowJobKey]string, len(keys))
	collisionDetected := false
	for _, key := range keys {
		// Config-excluded names (--self/--ignored) are matched by Validate
		// against raw YAML job names, so they must never be disambiguated:
		// a renamed "<self> [Workflow]" would stop matching selfJobName and
		// the gatekeeper would wait on its own check run forever, and a
		// renamed ignored job would lose its ignore.
		if len(rawNameWorkflows[key.name]) <= 1 || sv.isConfigExcludedName(key.name) {
			displayNames[key] = key.name
			continue
		}
		collisionDetected = true
		run := latestRunByKey[key]
		info := workflowInfoFor(run)
		suffix := info.workflowName
		if suffix == "" {
			// Workflow name not available (e.g. mocked test data). Fall back to
			// the workflow ID so the display still disambiguates uniquely.
			suffix = fmt.Sprintf("workflow:%d", key.workflowID)
		}
		displayNames[key] = fmt.Sprintf("%s [%s]", key.name, suffix)
	}
	if collisionDetected {
		sv.debugf("merge-gatekeeper [debug] cross-workflow name collision detected; disambiguated %d job(s)\n",
			collisionsCount(rawNameWorkflows))
	}

	// Build ghaStatuses: check runs first (with latest-run logic). For any name that has check runs,
	// we prefer check run state so in-progress isn't overwritten by stale combined status (e.g. from a cancelled run).
	ghaStatuses := make([]*ghaStatus, 0, len(latestRunByKey)+len(combined))
	currentJobs := make(map[string]struct{})

	// Detect matrix parents: if name is "X" and the SAME workflow has another
	// job starting with "X (", then "X" is a matrix parent. GitHub Actions often
	// leaves matrix parent check runs as cancelled/stuck when a workflow is
	// cancelled, while new workflow runs only report the matrix children.
	// Tracking the parent would block forever. We compare raw names but only
	// within one workflow: a parent and its children always belong to the same
	// workflow, and an unrelated workflow's "X (...)" jobs must not swallow a
	// real job named "X" elsewhere — dropping it while pending would
	// green-light the gatekeeper with that job still running.
	isMatrixParent := make(map[workflowJobKey]bool)
	for _, key := range keys {
		for _, otherKey := range keys {
			if key.workflowID == otherKey.workflowID && key.event == otherKey.event &&
				key.appID == otherKey.appID &&
				key.name != otherKey.name && strings.HasPrefix(otherKey.name, key.name+" (") {
				isMatrixParent[key] = true
				break
			}
		}
	}

	for _, key := range keys {
		run := latestRunByKey[key]
		displayName := displayNames[key]

		// Register raw and display names up front so a combined-status entry
		// matching either form is recognized as already-covered by a check
		// run — including names this loop goes on to drop (skipped jobs,
		// stuck matrix parents). A check run for the name exists either way,
		// and a stale same-name commit status must not resurrect it.
		currentJobs[key.name] = struct{}{}
		currentJobs[displayName] = struct{}{}

		statusStr := ""
		if run.Status != nil {
			statusStr = *run.Status
		}
		conclusionStr := ""
		if run.Conclusion != nil {
			conclusionStr = *run.Conclusion
		}

		// Determine the state for this check run.
		ghaStatus := &ghaStatus{Job: displayName}
		if *run.Status != checkRunCompletedStatus {
			ghaStatus.State = pendingState
			sv.debugf("merge-gatekeeper [debug] job=%s state=pending (check_run id=%v status=%s) runs_with_same_key=%d\n",
				displayName, run.ID, statusStr, runCountByKey[key])
		} else if run.Conclusion == nil {
			ghaStatus.State = errorState
			sv.debugf("merge-gatekeeper [debug] job=%s state=error (check_run id=%v status=completed conclusion=nil)\n",
				displayName, run.ID)
		} else {
			switch *run.Conclusion {
			case checkRunNeutralConclusion, checkRunSuccessConclusion:
				ghaStatus.State = successState
			case checkRunSkipConclusion:
				sv.debugf("merge-gatekeeper [debug] job=%s skipped\n", displayName)
				continue
			case checkRunCancelledConclusion:
				ghaStatus.State = cancelledState
				sv.debugf("merge-gatekeeper [debug] job=%s state=cancelled (conclusion=cancelled) runs_with_same_key=%d\n",
					displayName, runCountByKey[key])
			default:
				ghaStatus.State = errorState
				sv.debugf("merge-gatekeeper [debug] job=%s state=failed (check_run id=%v status=%s conclusion=%s) runs_with_same_key=%d\n",
					displayName, run.ID, statusStr, conclusionStr, runCountByKey[key])
			}
		}

		// Only ignore matrix parents that are stuck (pending or cancelled).
		// If a detected "parent" has a terminal result (success/failure/error), let it through —
		// it's either redundant with its children (true matrix parent, harmless) or a
		// falsely-detected "parent" whose signal we must preserve.
		if isMatrixParent[key] && (ghaStatus.State == pendingState || ghaStatus.State == cancelledState) {
			sv.debugf("merge-gatekeeper [debug] job=%s is a matrix parent in %s state, ignoring it\n", displayName, ghaStatus.State)
			continue
		}

		ghaStatuses = append(ghaStatuses, ghaStatus)
	}

	// Then add combined status only for contexts that don't have a check run (so we don't overwrite with stale state).
	// The combined status API already returns only the latest status per context, so currentJobs
	// just prevents a combined status from overriding a check run for the same context name.
	for _, s := range combined {
		if s.Context == nil || s.State == nil {
			return nil, fmt.Errorf("%w context: %v, status: %v", ErrInvalidCombinedStatusResponse, s.Context, s.State)
		}
		if _, ok := currentJobs[*s.Context]; ok {
			sv.debugf("merge-gatekeeper [debug] skipped combined_status context=%s (using check run instead)\n", *s.Context)
			continue
		}
		currentJobs[*s.Context] = struct{}{}

		ghaStatuses = append(ghaStatuses, &ghaStatus{
			Job:   *s.Context,
			State: jobState(*s.State),
		})
		sv.debugf("merge-gatekeeper [debug] job=%s state=%s source=combined_status\n", *s.Context, *s.State)
	}

	sort.Slice(ghaStatuses, func(i, j int) bool { return ghaStatuses[i].Job < ghaStatuses[j].Job })
	return ghaStatuses, nil
}

// collisionsCount counts how many raw names appear in more than one workflow.
func collisionsCount(rawNameWorkflows map[string]map[int64]struct{}) int {
	n := 0
	for _, workflows := range rawNameWorkflows {
		if len(workflows) > 1 {
			n++
		}
	}
	return n
}
