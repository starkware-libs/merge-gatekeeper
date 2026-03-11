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

const (
	successState = "success"
	errorState   = "error"
	failureState = "failure"
	pendingState = "pending"
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
	State string
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
		totalJobs:    make([]string, 0, len(ghaStatuses)),
		completeJobs: make([]string, 0, len(ghaStatuses)),
		errJobs:      make([]string, 0, len(ghaStatuses)/2),
		ignoredJobs:  make([]string, 0, len(ghaStatuses)),
		succeeded:    true,
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
			st.errJobs = append(st.errJobs, ghaStatus.Job)
		}
	}
	if len(st.errJobs) != 0 {
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
		if c.GetTotalCount() < maxStatusesPerPage {
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

	// Because multiple jobs with the same name may exist when jobs are created dynamically by third-party tools, etc.,
	// only the latest job should be managed.
	currentJobs := make(map[string]struct{})

	// When multiple check runs share the same name (e.g. re-runs, concurrency-cancelled runs),
	// keep the "current" one: prefer in-progress/queued over completed; if both completed, prefer highest ID (most recent).
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
		// Keep the one with the higher ID (more recent), as it represents the latest state
		// (e.g. from a re-run or a newer check suite for the same commit).
		thisID := int64(0)
		if run.ID != nil {
			thisID = *run.ID
		}
		existingID := int64(0)
		if existing.ID != nil {
			existingID = *existing.ID
		}
		if thisID > existingID {
			statusStr := "unknown"
			if run.Status != nil {
				statusStr = *run.Status
			}
			existingStatusStr := "unknown"
			if existing.Status != nil {
				existingStatusStr = *existing.Status
			}
			sv.debugf("merge-gatekeeper [debug] job=%s: picked newer run id=%d status=%s (replaced run id=%d status=%s)\n",
				name, thisID, statusStr, existingID, existingStatusStr)
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
			sv.debugf("merge-gatekeeper [debug] job=%s: keeping existing run id=%d status=%s (dropped older run id=%d status=%s)\n",
				name, existingID, existingStatusStr, thisID, statusStr)
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
		if isMatrixParent[name] {
			sv.debugf("merge-gatekeeper [debug] job=%s is a matrix parent (has children), ignoring it\n", name)
			continue
		}
		currentJobs[name] = struct{}{}

		ghaStatus := &ghaStatus{Job: name}
		statusStr := ""
		if run.Status != nil {
			statusStr = *run.Status
		}
		conclusionStr := ""
		if run.Conclusion != nil {
			conclusionStr = *run.Conclusion
		}

		if *run.Status != checkRunCompletedStatus {
			ghaStatus.State = pendingState
			ghaStatuses = append(ghaStatuses, ghaStatus)
			sv.debugf("merge-gatekeeper [debug] job=%s state=pending (check_run id=%v status=%s) runs_with_same_name=%d\n",
				name, run.ID, statusStr, runCountByName[name])
			continue
		}

		if run.Conclusion == nil {
			ghaStatus.State = errorState
			ghaStatuses = append(ghaStatuses, ghaStatus)
			sv.debugf("merge-gatekeeper [debug] job=%s state=error (check_run id=%v status=completed conclusion=nil)\n",
				name, run.ID)
			continue
		}
		switch *run.Conclusion {
		case checkRunNeutralConclusion, checkRunSuccessConclusion:
			ghaStatus.State = successState
		case checkRunSkipConclusion:
			continue
		case checkRunCancelledConclusion:
			// Treat as pending: we keep waiting for this job (don't fail, don't pass). Covers both:
			// - old run cancelled by concurrency while new run hasn't started this job yet;
			// - someone cancelled the only run → we still require a successful run before merge.
			ghaStatus.State = pendingState
			sv.debugf("merge-gatekeeper [debug] job=%s state=pending (conclusion=cancelled, still waiting) runs_with_same_name=%d\n",
				name, runCountByName[name])
		default:
			ghaStatus.State = errorState
			sv.debugf("merge-gatekeeper [debug] job=%s state=failed (check_run id=%v status=%s conclusion=%s) runs_with_same_name=%d\n",
				name, run.ID, statusStr, conclusionStr, runCountByName[name])
		}
		ghaStatuses = append(ghaStatuses, ghaStatus)
	}

	// Then add combined status only for contexts that don't have a check run (so we don't overwrite with stale state).
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
			State: *s.State,
		})
		sv.debugf("merge-gatekeeper [debug] job=%s state=%s source=combined_status\n", *s.Context, *s.State)
	}

	sort.Slice(ghaStatuses, func(i, j int) bool { return ghaStatuses[i].Job < ghaStatuses[j].Job })
	return ghaStatuses, nil
}
