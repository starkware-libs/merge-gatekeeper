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

// filterSupersededRuns removes or converts stale check runs from superseded workflow suites.
// A suite is superseded when a newer non-cancelled run of the same workflow exists for this commit.
// Check runs from superseded suites are handled as follows:
//   - conclusion is success/neutral/skipped → keep (valid result, needed for "re-run failed jobs")
//   - superseding run status != "completed" → convert to pending (replacement still in progress)
//   - superseding run status == "completed" → drop (replacement finished, job not needed)
func (sv *statusValidator) filterSupersededRuns(ctx context.Context, runResults []*github.CheckRun) ([]*github.CheckRun, error) {
	// Count distinct non-zero suite IDs. If <=1, nothing can be superseded.
	distinctSuiteIDs := make(map[int64]struct{})
	for _, run := range runResults {
		if run.CheckSuite != nil && run.CheckSuite.ID != nil && *run.CheckSuite.ID != 0 {
			distinctSuiteIDs[*run.CheckSuite.ID] = struct{}{}
		}
	}
	if len(distinctSuiteIDs) <= 1 {
		return runResults, nil
	}

	// Fetch workflow runs for this commit. Requires actions: read permission.
	workflowRuns, err := sv.listWorkflowRunsForRef(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch workflow runs (actions: read permission required): %w", err)
	}
	if len(workflowRuns) == 0 {
		return runResults, nil
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
		return runResults, nil
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
	return filtered, nil
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
	runResults, err = sv.filterSupersededRuns(ctx, runResults)
	if err != nil {
		return nil, err
	}

	// Because multiple jobs with the same name may exist when jobs are created dynamically by third-party tools, etc.,
	// only the latest job should be managed.
	currentJobs := make(map[string]struct{})

	// When multiple check runs share the same name (e.g. re-runs, concurrency-cancelled runs),
	// keep the one from the most recent workflow run. We compare by check suite ID (assigned
	// when the workflow is triggered) rather than check run ID (assigned when the job starts
	// on a runner). Suite ID correctly orders workflow runs chronologically, whereas run IDs
	// can be out-of-order when a cancelled run's job was scheduled after the replacement
	// run's early jobs. Falls back to run ID for third-party integrations without CheckSuite.
	latestRunByName := make(map[string]*github.CheckRun)
	runCountByName := make(map[string]int)
	for _, run := range runResults {
		if run.Name == nil || run.Status == nil {
			return nil, fmt.Errorf("%w name: %v, status: %v", ErrInvalidCheckRunResponse, run.Name, run.Status)
		}
		name := *run.Name
		runCountByName[name]++
		existing, ok := latestRunByName[name]
		if !ok {
			latestRunByName[name] = run
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
		thisKey := thisSuiteID
		existingKey := existingSuiteID
		if thisKey == existingKey {
			if run.ID != nil {
				thisKey = *run.ID
			}
			if existing.ID != nil {
				existingKey = *existing.ID
			}
		}
		if thisKey > existingKey {
			statusStr := "unknown"
			if run.Status != nil {
				statusStr = *run.Status
			}
			existingStatusStr := "unknown"
			if existing.Status != nil {
				existingStatusStr = *existing.Status
			}
			sv.debugf("merge-gatekeeper [debug] job=%s: picked run id=%v suite=%v status=%s (replaced run id=%v suite=%v status=%s)\n",
				name, run.ID, thisSuiteID, statusStr, existing.ID, existingSuiteID, existingStatusStr)
			latestRunByName[name] = run
		} else {
			statusStr := "unknown"
			if run.Status != nil {
				statusStr = *run.Status
			}
			existingStatusStr := "unknown"
			if existing.Status != nil {
				existingStatusStr = *existing.Status
			}
			sv.debugf("merge-gatekeeper [debug] job=%s: keeping run id=%v suite=%v status=%s (dropped run id=%v suite=%v status=%s)\n",
				name, existing.ID, existingSuiteID, existingStatusStr, run.ID, thisSuiteID, statusStr)
		}
	}

	// Build ghaStatuses: check runs first (with latest-run logic). For any name that has check runs,
	// we prefer check run state so in-progress isn't overwritten by stale combined status (e.g. from a cancelled run).
	ghaStatuses := make([]*ghaStatus, 0, len(latestRunByName)+len(combined))
	names := make([]string, 0, len(latestRunByName))
	for name := range latestRunByName {
		names = append(names, name)
	}
	sort.Strings(names)

	// Detect matrix parents: if name is "X" and there is another job starting with "X (", then "X" is a matrix parent.
	// GitHub Actions often leaves matrix parent check runs as cancelled/stuck when a workflow is cancelled,
	// while new workflow runs only report the matrix children. Tracking the parent would block forever.
	isMatrixParent := make(map[string]bool)
	for _, name := range names {
		for _, otherName := range names {
			if name != otherName && strings.HasPrefix(otherName, name+" (") {
				isMatrixParent[name] = true
				break
			}
		}
	}

	for _, name := range names {
		run := latestRunByName[name]

		statusStr := ""
		if run.Status != nil {
			statusStr = *run.Status
		}
		conclusionStr := ""
		if run.Conclusion != nil {
			conclusionStr = *run.Conclusion
		}

		// Determine the state for this check run.
		ghaStatus := &ghaStatus{Job: name}
		if *run.Status != checkRunCompletedStatus {
			ghaStatus.State = pendingState
			sv.debugf("merge-gatekeeper [debug] job=%s state=pending (check_run id=%v status=%s) runs_with_same_name=%d\n",
				name, run.ID, statusStr, runCountByName[name])
		} else if run.Conclusion == nil {
			ghaStatus.State = errorState
			sv.debugf("merge-gatekeeper [debug] job=%s state=error (check_run id=%v status=completed conclusion=nil)\n",
				name, run.ID)
		} else {
			switch *run.Conclusion {
			case checkRunNeutralConclusion, checkRunSuccessConclusion:
				ghaStatus.State = successState
			case checkRunSkipConclusion:
				sv.debugf("merge-gatekeeper [debug] job=%s skipped\n", name)
				continue
			case checkRunCancelledConclusion:
				ghaStatus.State = cancelledState
				sv.debugf("merge-gatekeeper [debug] job=%s state=cancelled (conclusion=cancelled) runs_with_same_name=%d\n",
					name, runCountByName[name])
			default:
				ghaStatus.State = errorState
				sv.debugf("merge-gatekeeper [debug] job=%s state=failed (check_run id=%v status=%s conclusion=%s) runs_with_same_name=%d\n",
					name, run.ID, statusStr, conclusionStr, runCountByName[name])
			}
		}

		// Only ignore matrix parents that are stuck (pending or cancelled).
		// If a detected "parent" has a terminal result (success/failure/error), let it through —
		// it's either redundant with its children (true matrix parent, harmless) or a
		// falsely-detected "parent" whose signal we must preserve.
		if isMatrixParent[name] && (ghaStatus.State == pendingState || ghaStatus.State == cancelledState) {
			sv.debugf("merge-gatekeeper [debug] job=%s is a matrix parent in %s state, ignoring it\n", name, ghaStatus.State)
			continue
		}

		currentJobs[name] = struct{}{}
		ghaStatuses = append(ghaStatuses, ghaStatus)
	}

	// Then add combined status only for contexts that don't have a check run (so we don't overwrite with stale state).
	// NOTE: The GitHub combined status API returns statuses most-recent-first per context,
	// so first-wins dedup via currentJobs is correct — it keeps the latest status for each context.
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
