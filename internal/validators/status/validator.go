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

// suiteWorkflowInfo identifies the workflow that owns a given check suite.
// Used to disambiguate jobs that share a name across different workflows.
type suiteWorkflowInfo struct {
	workflowID   int64
	workflowName string
}

// filterSupersededRuns removes or converts stale check runs from superseded workflow suites.
// A suite is superseded when a newer non-cancelled run of the same workflow exists for this commit.
// Check runs from superseded suites are handled as follows:
//   - conclusion is success/neutral/skipped → keep (valid result, needed for "re-run failed jobs")
//   - superseding run status != "completed" → convert to pending (replacement still in progress)
//   - superseding run status == "completed" → drop (replacement finished, job not needed)
//
// Also returns a suite_id → workflow info map for callers that need to disambiguate
// same-name jobs across workflows. The map is nil when no workflow runs were fetched
// (single suite, third-party-only check runs, or empty workflow runs response).
func (sv *statusValidator) filterSupersededRuns(ctx context.Context, runResults []*github.CheckRun) ([]*github.CheckRun, map[int64]suiteWorkflowInfo, error) {
	// Count distinct non-zero suite IDs. If <=1, nothing can be superseded.
	distinctSuiteIDs := make(map[int64]struct{})
	for _, run := range runResults {
		if run.CheckSuite != nil && run.CheckSuite.ID != nil && *run.CheckSuite.ID != 0 {
			distinctSuiteIDs[*run.CheckSuite.ID] = struct{}{}
		}
	}
	if len(distinctSuiteIDs) <= 1 {
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
	suiteToWorkflow := make(map[int64]suiteWorkflowInfo, len(workflowRuns))
	for _, wr := range workflowRuns {
		if wr.CheckSuiteID == nil {
			continue
		}
		info := suiteWorkflowInfo{}
		if wr.WorkflowID != nil {
			info.workflowID = *wr.WorkflowID
		}
		if wr.Name != nil {
			info.workflowName = *wr.Name
		}
		suiteToWorkflow[*wr.CheckSuiteID] = info
	}

	// For each workflow, find the latest non-cancelled run by (RunNumber, RunAttempt).
	type workflowLatest struct {
		runNumber  int
		runAttempt int
		suiteID    int64
		status     string
	}
	perWorkflow := make(map[int64]*workflowLatest) // keyed by WorkflowID
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
		wid := *wr.WorkflowID
		existing, ok := perWorkflow[wid]
		if !ok || rn > existing.runNumber || (rn == existing.runNumber && ra > existing.runAttempt) {
			perWorkflow[wid] = &workflowLatest{
				runNumber:  rn,
				runAttempt: ra,
				suiteID:    *wr.CheckSuiteID,
				status:     st,
			}
		}
	}

	// Build supersededSuites: map from CheckSuiteID → superseding run's Status.
	// A suite is superseded if it belongs to a workflow that has a newer non-cancelled run.
	supersededSuites := make(map[int64]string) // suiteID → superseding run status
	for _, wr := range workflowRuns {
		if wr.WorkflowID == nil || wr.CheckSuiteID == nil {
			continue
		}
		latest, ok := perWorkflow[*wr.WorkflowID]
		if !ok {
			// All runs of this workflow are cancelled — nothing superseded.
			continue
		}
		if *wr.CheckSuiteID != latest.suiteID {
			supersededSuites[*wr.CheckSuiteID] = latest.status
		}
	}

	if len(supersededSuites) == 0 {
		return runResults, suiteToWorkflow, nil
	}

	sv.debugf("merge-gatekeeper [debug] superseded suites detected: %d suite(s) from %d workflow run(s)\n",
		len(supersededSuites), len(workflowRuns))

	// Filter check runs from superseded suites.
	filtered := make([]*github.CheckRun, 0, len(runResults))
	for _, run := range runResults {
		suiteID := int64(0)
		if run.CheckSuite != nil && run.CheckSuite.ID != nil {
			suiteID = *run.CheckSuite.ID
		}
		supersedingStatus, isSuperseded := supersededSuites[suiteID]
		if !isSuperseded {
			filtered = append(filtered, run)
			continue
		}

		name := ""
		if run.Name != nil {
			name = *run.Name
		}

		// Keep successful/neutral/skipped checks from superseded suites (valid results).
		if run.Status != nil && *run.Status == checkRunCompletedStatus && run.Conclusion != nil {
			switch *run.Conclusion {
			case checkRunSuccessConclusion, checkRunNeutralConclusion, checkRunSkipConclusion:
				sv.debugf("merge-gatekeeper [debug] job=%s: keeping %s check from superseded suite %d\n",
					name, *run.Conclusion, suiteID)
				filtered = append(filtered, run)
				continue
			}
		}

		if supersedingStatus != checkRunCompletedStatus {
			// Superseding run is still in progress — convert to pending.
			// Clone the check run to avoid mutating the original.
			pendingStatus := "queued"
			converted := *run
			converted.Status = &pendingStatus
			converted.Conclusion = nil
			sv.debugf("merge-gatekeeper [debug] job=%s: converted to pending (superseded suite %d, superseding run %s)\n",
				name, suiteID, supersedingStatus)
			filtered = append(filtered, &converted)
		} else {
			// Superseding run completed — drop the stale check.
			sv.debugf("merge-gatekeeper [debug] job=%s: dropped from superseded suite %d (superseding run completed)\n",
				name, suiteID)
		}
	}
	return filtered, suiteToWorkflow, nil
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

	// Pre-filter: remove/convert stale check runs from superseded workflow suites.
	// suiteToWorkflow maps each check_suite_id to the owning workflow's identity,
	// used below to dedup by (workflow_id, name) instead of by name alone. The map
	// is nil when only one workflow suite exists for this ref.
	runResults, suiteToWorkflow, err := sv.filterSupersededRuns(ctx, runResults)
	if err != nil {
		return nil, err
	}

	// workflowInfoFor returns the workflow identity for a check run's check suite.
	// Returns the zero value (workflowID=0) when the run has no suite (third-party
	// integrations) or when no workflow run map was loaded (single-suite ref).
	workflowInfoFor := func(run *github.CheckRun) suiteWorkflowInfo {
		if suiteToWorkflow == nil || run.CheckSuite == nil || run.CheckSuite.ID == nil {
			return suiteWorkflowInfo{}
		}
		return suiteToWorkflow[*run.CheckSuite.ID]
	}

	// Dedup key: (workflow_id, name). Within a single workflow, re-runs and
	// concurrency-cancelled runs collapse via the suite-ID/run-ID tiebreaker
	// below. Across workflows, jobs with the same name remain independent —
	// otherwise a "benchmarking" job in workflow A could mask the state of a
	// "benchmarking" job in workflow B (see PR starkware-libs/sequencer#13859).
	type workflowJobKey struct {
		workflowID int64
		name       string
	}
	latestRunByKey := make(map[workflowJobKey]*github.CheckRun)
	runCountByKey := make(map[workflowJobKey]int)
	for _, run := range runResults {
		if run.Name == nil || run.Status == nil {
			return nil, fmt.Errorf("%w name: %v, status: %v", ErrInvalidCheckRunResponse, run.Name, run.Status)
		}
		name := *run.Name
		key := workflowJobKey{workflowID: workflowInfoFor(run).workflowID, name: name}
		runCountByKey[key]++
		existing, ok := latestRunByKey[key]
		if !ok {
			latestRunByKey[key] = run
			continue
		}
		thisSuiteID := int64(0)
		if run.CheckSuite != nil && run.CheckSuite.ID != nil {
			thisSuiteID = *run.CheckSuite.ID
		}
		existingSuiteID := int64(0)
		if existing.CheckSuite != nil && existing.CheckSuite.ID != nil {
			existingSuiteID = *existing.CheckSuite.ID
		}
		// Compare by suite ID; if equal (including both zero / no suite), fall back to run ID.
		// Within the same workflow, suite IDs are assigned in chronological order, so the
		// highest suite ID is the latest re-run. Run IDs can be out-of-order when a cancelled
		// run's job was scheduled later than the replacement run's earlier jobs.
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
		if thisTiebreaker > existingTiebreaker {
			statusStr := "unknown"
			if run.Status != nil {
				statusStr = *run.Status
			}
			existingStatusStr := "unknown"
			if existing.Status != nil {
				existingStatusStr = *existing.Status
			}
			sv.debugf("merge-gatekeeper [debug] job=%s workflow=%d: picked run id=%v suite=%v status=%s (replaced run id=%v suite=%v status=%s)\n",
				name, key.workflowID, run.ID, thisSuiteID, statusStr, existing.ID, existingSuiteID, existingStatusStr)
			latestRunByKey[key] = run
		} else {
			statusStr := "unknown"
			if run.Status != nil {
				statusStr = *run.Status
			}
			existingStatusStr := "unknown"
			if existing.Status != nil {
				existingStatusStr = *existing.Status
			}
			sv.debugf("merge-gatekeeper [debug] job=%s workflow=%d: keeping run id=%v suite=%v status=%s (dropped run id=%v suite=%v status=%s)\n",
				name, key.workflowID, existing.ID, existingSuiteID, existingStatusStr, run.ID, thisSuiteID, statusStr)
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
		return keys[i].workflowID < keys[j].workflowID
	})

	displayNames := make(map[workflowJobKey]string, len(keys))
	collisionDetected := false
	for _, key := range keys {
		if len(rawNameWorkflows[key.name]) <= 1 {
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

	// Detect matrix parents: if name is "X" and there is another job starting with "X (",
	// then "X" is a matrix parent. GitHub Actions often leaves matrix parent check runs as
	// cancelled/stuck when a workflow is cancelled, while new workflow runs only report the
	// matrix children. Tracking the parent would block forever. We match on raw names so
	// the heuristic still works after collision-disambiguation: a parent and its children
	// belong to the same workflow and so disambiguate together.
	isMatrixParent := make(map[string]bool)
	for _, key := range keys {
		for _, otherKey := range keys {
			if key.name != otherKey.name && strings.HasPrefix(otherKey.name, key.name+" (") {
				isMatrixParent[key.name] = true
				break
			}
		}
	}

	for _, key := range keys {
		run := latestRunByKey[key]
		displayName := displayNames[key]

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
		if isMatrixParent[key.name] && (ghaStatus.State == pendingState || ghaStatus.State == cancelledState) {
			sv.debugf("merge-gatekeeper [debug] job=%s is a matrix parent in %s state, ignoring it\n", displayName, ghaStatus.State)
			continue
		}

		// Track both raw and display names so a combined-status entry matching
		// either form is recognized as already-covered by a check run.
		currentJobs[key.name] = struct{}{}
		currentJobs[displayName] = struct{}{}
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
