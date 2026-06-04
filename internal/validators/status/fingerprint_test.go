package status

import (
	"context"
	"strings"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
)

// fingerprintClient builds a mock client serving third-party (non
// github-actions) check runs by name->(status, conclusion), so the workflow
// listing, the duplicate-jobs guard, and the orphan rule stay out of the
// picture and the tests exercise only the fingerprint plumbing. resolveSHA is
// what GetCommitSHA1 returns for branch refs.
func fingerprintClient(checkRuns map[string][2]string, resolveSHA *string) *mock.Client {
	return &mock.Client{
		GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
			return &github.CombinedStatus{}, nil, nil
		},
		ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
			runs := make([]*github.CheckRun, 0, len(checkRuns))
			id := int64(9000)
			for name, st := range checkRuns {
				name, status, conclusion := name, st[0], st[1]
				run := &github.CheckRun{
					ID:     int64Ptr(id),
					Name:   stringPtr(name),
					Status: &status,
					App:    &github.App{ID: int64Ptr(77), Slug: stringPtr("third-party-ci")},
				}
				if conclusion != "" {
					run.Conclusion = &conclusion
				}
				runs = append(runs, run)
				id++
			}
			total := len(runs)
			return &github.ListCheckRunsResults{Total: &total, CheckRuns: runs}, nil, nil
		},
		ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
			zero := 0
			return &github.WorkflowRuns{TotalCount: &zero}, nil, nil
		},
		GetCommitSHA1Func: func(ctx context.Context, owner, repo, ref string) (string, *github.Response, error) {
			return *resolveSHA, nil, nil
		},
	}
}

const fullSHARef = "0123456789abcdef0123456789abcdef01234567"

func Test_status_Fingerprint_StableAcrossIdenticalPolls(t *testing.T) {
	resolved := ""
	sv := &statusValidator{
		client:      fingerprintClient(map[string][2]string{"build": {"completed", "success"}}, &resolved),
		owner:       "test-owner",
		repo:        "test-repo",
		ref:         fullSHARef,
		selfJobName: "self-job",
	}

	st1, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() poll 1 returned unexpected error: %v", err)
	}
	st2, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() poll 2 returned unexpected error: %v", err)
	}

	if st1.Fingerprint() != st2.Fingerprint() {
		t.Errorf("two polls over an identical world must produce identical fingerprints:\n  poll1: %q\n  poll2: %q",
			st1.Fingerprint(), st2.Fingerprint())
	}
	if !strings.HasPrefix(st1.Fingerprint(), fullSHARef+"\x00") {
		t.Errorf("the fingerprint must carry the resolved head SHA; got %q", st1.Fingerprint())
	}
}

func Test_status_Fingerprint_ChangesWhenHeadSHAChanges(t *testing.T) {
	// A branch --ref re-resolves every poll; an advance mid-run must change
	// the fingerprint so the loop's quiescence streak resets (the same hazard
	// the per-SHA memo reset guards inside the validator).
	resolved := "1111111111111111111111111111111111111111"
	sv := &statusValidator{
		client:      fingerprintClient(map[string][2]string{"build": {"completed", "success"}}, &resolved),
		owner:       "test-owner",
		repo:        "test-repo",
		ref:         "feature-branch",
		selfJobName: "self-job",
	}

	st1, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() poll 1 returned unexpected error: %v", err)
	}
	resolved = "2222222222222222222222222222222222222222"
	st2, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() poll 2 returned unexpected error: %v", err)
	}

	if st1.Fingerprint() == st2.Fingerprint() {
		t.Errorf("the branch advanced between polls but the fingerprint did not change: %q", st1.Fingerprint())
	}
}

func Test_status_Fingerprint_ChangesWhenJobSetChanges(t *testing.T) {
	resolved := ""
	checkRuns := map[string][2]string{"build": {"completed", "success"}}
	sv := &statusValidator{
		client:      fingerprintClient(checkRuns, &resolved),
		owner:       "test-owner",
		repo:        "test-repo",
		ref:         fullSHARef,
		selfJobName: "self-job",
	}

	st1, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() poll 1 returned unexpected error: %v", err)
	}
	// A new check run materialized between polls.
	checkRuns["integration-tests"] = [2]string{"queued", ""}
	st2, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() poll 2 returned unexpected error: %v", err)
	}

	if st1.Fingerprint() == st2.Fingerprint() {
		t.Errorf("a job appeared between polls but the fingerprint did not change: %q", st1.Fingerprint())
	}
}

func Test_status_Fingerprint_ExcludesSelfAndIgnored(t *testing.T) {
	// Guard from the design review: a still-running --ignored job (or the
	// gatekeeper's own job) must not churn the fingerprint, or it would stall
	// the loop's quiescence streak that --ignored explicitly exempts.
	resolved := ""
	checkRuns := map[string][2]string{
		"build":     {"completed", "success"},
		"flaky":     {"in_progress", ""},
		"self-job":  {"in_progress", ""},
	}
	sv := &statusValidator{
		client:      fingerprintClient(checkRuns, &resolved),
		owner:       "test-owner",
		repo:        "test-repo",
		ref:         fullSHARef,
		selfJobName: "self-job",
		ignoredJobs: []string{"flaky"},
	}

	st1, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() poll 1 returned unexpected error: %v", err)
	}
	// The ignored job concluded between polls; the gated world is unchanged.
	checkRuns["flaky"] = [2]string{"completed", "success"}
	st2, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() poll 2 returned unexpected error: %v", err)
	}

	if st1.Fingerprint() != st2.Fingerprint() {
		t.Errorf("an --ignored job's state change must not alter the fingerprint:\n  poll1: %q\n  poll2: %q",
			st1.Fingerprint(), st2.Fingerprint())
	}
	if strings.Contains(st1.Fingerprint(), "flaky") || strings.Contains(st1.Fingerprint(), "self-job") {
		t.Errorf("the fingerprint must exclude self/ignored names; got %q", st1.Fingerprint())
	}
}

func Test_status_TrackedJobs_CountsOnlyGatedJobs(t *testing.T) {
	resolved := ""
	sv := &statusValidator{
		client: fingerprintClient(map[string][2]string{
			"build":    {"completed", "success"},
			"flaky":    {"in_progress", ""},
			"self-job": {"in_progress", ""},
		}, &resolved),
		owner:       "test-owner",
		repo:        "test-repo",
		ref:         fullSHARef,
		selfJobName: "self-job",
		ignoredJobs: []string{"flaky"},
	}

	st, err := sv.Validate(context.Background())
	if err != nil {
		t.Fatalf("Validate() returned unexpected error: %v", err)
	}
	if got := st.TrackedJobs(); got != 1 {
		t.Errorf("TrackedJobs() must count only gated jobs (build), excluding self/ignored; got %d", got)
	}
}
