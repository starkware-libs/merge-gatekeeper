package status

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/starkware-libs/merge-gatekeeper/internal/github"
	"github.com/starkware-libs/merge-gatekeeper/internal/github/mock"
	"github.com/starkware-libs/merge-gatekeeper/internal/validators"
)

func stringPtr(str string) *string {
	return &str
}

func int64Ptr(i int64) *int64 {
	return &i
}

func intPtr(i int) *int {
	return &i
}

func TestCreateValidator(t *testing.T) {
	tests := map[string]struct {
		c       github.Client
		opts    []Option
		want    validators.Validator
		wantErr bool
	}{
		"returns Validator when option is not empty": {
			c: &mock.Client{},
			opts: []Option{
				WithGitHubOwnerAndRepo("test-owner", "test-repo"),
				WithGitHubRef("sha"),
				WithSelfJob("job"),
				WithIgnoredJobs("job-01,job-02"),
			},
			want: &statusValidator{
				client:      &mock.Client{},
				owner:       "test-owner",
				repo:        "test-repo",
				ref:         "sha",
				selfJobName: "job",
				ignoredJobs: []string{"job-01", "job-02"},
			},
			wantErr: false,
		},
		"returns Validator when there are duplicate options": {
			c: &mock.Client{},
			opts: []Option{
				WithGitHubOwnerAndRepo("test", "test-repo"),
				WithGitHubRef("sha"),
				WithGitHubRef("sha-01"),
				WithSelfJob("job"),
				WithSelfJob("job-01"),
			},
			want: &statusValidator{
				client:      &mock.Client{},
				owner:       "test",
				repo:        "test-repo",
				ref:         "sha-01",
				selfJobName: "job-01",
			},
			wantErr: false,
		},
		"returns Validator when invalid string is provided for ignored jobs": {
			c: &mock.Client{},
			opts: []Option{
				WithGitHubOwnerAndRepo("test", "test-repo"),
				WithGitHubRef("sha"),
				WithGitHubRef("sha-01"),
				WithSelfJob("job"),
				WithSelfJob("job-01"),
				WithIgnoredJobs(","), // Malformed but handled
			},
			want: &statusValidator{
				client:      &mock.Client{},
				owner:       "test",
				repo:        "test-repo",
				ref:         "sha-01",
				selfJobName: "job-01",
				ignoredJobs: []string{}, // Not nil
			},
			wantErr: false,
		},
		"returns error when option is empty": {
			c:       &mock.Client{},
			want:    nil,
			wantErr: true,
		},
		"returns error when client is nil": {
			c: nil,
			opts: []Option{
				WithGitHubOwnerAndRepo("test", "test-repo"),
				WithGitHubRef("sha"),
				WithGitHubRef("sha-01"),
				WithSelfJob("job"),
				WithSelfJob("job-01"),
			},
			want:    nil,
			wantErr: true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := CreateValidator(tt.c, tt.opts...)
			if (err != nil) != tt.wantErr {
				t.Errorf("CreateValidator error = %v, wantErr: %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("CreateValidator() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestName(t *testing.T) {
	tests := map[string]struct {
		c    github.Client
		opts []Option
		want string
	}{
		"Name returns the correct job name which gets overridden": {
			c: &mock.Client{},
			opts: []Option{
				WithGitHubOwnerAndRepo("test-owner", "test-repo"),
				WithGitHubRef("sha"),
				WithSelfJob("job"),
				WithIgnoredJobs("job-01,job-02"),
			},
			want: "job",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := CreateValidator(tt.c, tt.opts...)
			if err != nil {
				t.Errorf("Unexpected error with CreateValidator: %v", err)
				return
			}
			if tt.want != got.Name() {
				t.Errorf("Job name didn't match, want: %s, got: %v", tt.want, got.Name())
			}
		})
	}
}

func Test_statusValidator_Validate(t *testing.T) {
	type test struct {
		selfJobName string
		ignoredJobs []string
		client      github.Client
		ctx         context.Context
		wantErr     bool
		wantErrStr  string
		wantStatus  validators.Status
	}
	tests := map[string]test{
		"returns error when listGhaStatuses return an error": {
			client: &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return nil, nil, errors.New("err")
				},
			},
			wantErr:    true,
			wantStatus: nil,
			wantErrStr: "err",
		},
		"returns succeeded status and nil when there is no job": {
			client: &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{}, nil, nil
				},
			},
			wantErr: false,
			wantStatus: &status{
				succeeded:     true,
				totalJobs:     []string{},
				completeJobs:  []string{},
				failedJobs:       []string{},
				cancelledJobs: []string{},
				ignoredJobs:   []string{},
			},
		},
		"returns succeeded status and nil when there is one job, which is itself": {
			selfJobName: "self-job",
			client: &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{
						Statuses: []*github.RepoStatus{
							{
								Context: stringPtr("self-job"),
								State:   stringPtr(string(pendingState)), // should be irrelevant
							},
						},
					}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{}, nil, nil
				},
			},
			wantErr: false,
			wantStatus: &status{
				succeeded:     true,
				totalJobs:     []string{},
				completeJobs:  []string{},
				failedJobs:       []string{},
				cancelledJobs: []string{},
				ignoredJobs:   []string{},
			},
		},
		"returns failed status and nil when there is one job": {
			client: &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{
						Statuses: []*github.RepoStatus{
							{
								Context: stringPtr("job"),
								State:   stringPtr(string(pendingState)),
							},
						},
					}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{}, nil, nil
				},
			},
			wantErr: false,
			wantStatus: &status{
				succeeded:     false,
				totalJobs:     []string{"job"},
				completeJobs:  []string{},
				failedJobs:       []string{},
				cancelledJobs: []string{},
				ignoredJobs:   []string{},
			},
		},
		"returns error when there is a failed job": {
			selfJobName: "self-job",
			client: &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{
						Statuses: []*github.RepoStatus{
							{
								Context: stringPtr("job-01"),
								State:   stringPtr(string(successState)),
							},
							{
								Context: stringPtr("job-02"),
								State:   stringPtr(string(errorState)),
							},
							{
								Context: stringPtr("self-job"),
								State:   stringPtr(string(pendingState)),
							},
						},
					}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{}, nil, nil
				},
			},
			wantErr: true,
			wantErrStr: (&status{
				totalJobs: []string{
					"job-01", "job-02",
				},
				completeJobs: []string{
					"job-01",
				},
				failedJobs: []string{
					"job-02",
				},
				cancelledJobs: []string{},
				ignoredJobs:   []string{},
			}).Detail(),
		},
		"returns error when there is a failed job with failure state": {
			selfJobName: "self-job",
			client: &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{
						Statuses: []*github.RepoStatus{
							{
								Context: stringPtr("job-01"),
								State:   stringPtr(string(successState)),
							},
							{
								Context: stringPtr("job-02"),
								State:   stringPtr(string(failureState)),
							},
							{
								Context: stringPtr("self-job"),
								State:   stringPtr(string(pendingState)),
							},
						},
					}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{}, nil, nil
				},
			},
			wantErr: true,
			wantErrStr: (&status{
				totalJobs: []string{
					"job-01", "job-02",
				},
				completeJobs: []string{
					"job-01",
				},
				failedJobs: []string{
					"job-02",
				},
				cancelledJobs: []string{},
				ignoredJobs:   []string{},
			}).Detail(),
		},
		"returns failed status and nil when successful job count is less than total": {
			selfJobName: "self-job",
			client: &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{
						Statuses: []*github.RepoStatus{
							{
								Context: stringPtr("job-01"),
								State:   stringPtr(string(successState)),
							},
							{
								Context: stringPtr("job-02"),
								State:   stringPtr(string(pendingState)),
							},
							{
								Context: stringPtr("self-job"),
								State:   stringPtr(string(pendingState)),
							},
						},
					}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{}, nil, nil
				},
			},
			wantErr: false,
			wantStatus: &status{
				succeeded: false,
				totalJobs: []string{
					"job-01",
					"job-02",
				},
				completeJobs: []string{
					"job-01",
				},
				failedJobs:       []string{},
				cancelledJobs: []string{},
				ignoredJobs:   []string{},
			},
		},
		"returns succeeded status and nil when validation is success": {
			selfJobName: "self-job",
			client: &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{
						Statuses: []*github.RepoStatus{
							{
								Context: stringPtr("job-01"),
								State:   stringPtr(string(successState)),
							},
							{
								Context: stringPtr("job-02"),
								State:   stringPtr(string(successState)),
							},
							{
								Context: stringPtr("self-job"),
								State:   stringPtr(string(pendingState)),
							},
						},
					}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{}, nil, nil
				},
			},
			wantErr: false,
			wantStatus: &status{
				succeeded: true,
				totalJobs: []string{
					"job-01",
					"job-02",
				},
				completeJobs: []string{
					"job-01",
					"job-02",
				},
				failedJobs:       []string{},
				cancelledJobs: []string{},
				ignoredJobs:   []string{},
			},
		},
		"returns succeeded status and nil when only an ignored job is failing": {
			selfJobName: "self-job",
			ignoredJobs: []string{"job-02", "job-03"}, // String input here should be already TrimSpace'd
			client: &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{
						Statuses: []*github.RepoStatus{
							{
								Context: stringPtr("job-01"),
								State:   stringPtr(string(successState)),
							},
							{
								Context: stringPtr("job-02"),
								State:   stringPtr(string(errorState)),
							},
							{
								Context: stringPtr("self-job"),
								State:   stringPtr(string(pendingState)),
							},
						},
					}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{}, nil, nil
				},
			},
			wantErr: false,
			wantStatus: &status{
				succeeded:     true,
				totalJobs:     []string{"job-01"},
				completeJobs:  []string{"job-01"},
				failedJobs:       []string{},
				cancelledJobs: []string{},
				ignoredJobs:   []string{"job-02", "job-03"},
			},
		},
		"returns succeeded status and nil when only an ignored job is failing, with failure state": {
			selfJobName: "self-job",
			ignoredJobs: []string{"job-02", "job-03"},
			client: &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{
						Statuses: []*github.RepoStatus{
							{
								Context: stringPtr("job-01"),
								State:   stringPtr(string(successState)),
							},
							{
								Context: stringPtr("job-02"),
								State:   stringPtr(string(failureState)),
							},
							{
								Context: stringPtr("self-job"),
								State:   stringPtr(string(pendingState)),
							},
						},
					}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{}, nil, nil
				},
			},
			wantErr: false,
			wantStatus: &status{
				succeeded:     true,
				totalJobs:     []string{"job-01"},
				completeJobs:  []string{"job-01"},
				failedJobs:       []string{},
				cancelledJobs: []string{},
				ignoredJobs:   []string{"job-02", "job-03"},
			},
		},
		"returns error when a check run is cancelled": {
			selfJobName: "self-job",
			client: &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								ID:         int64Ptr(1),
								Name:       stringPtr("build"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
							},
							{
								ID:         int64Ptr(2),
								Name:       stringPtr("test"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunCancelledConclusion),
							},
						},
					}, nil, nil
				},
			},
			wantErr: true,
			wantErrStr: (&status{
				totalJobs:     []string{"build", "test"},
				completeJobs:  []string{"build"},
				failedJobs:    []string{},
				cancelledJobs: []string{"test"},
				ignoredJobs:   []string{},
			}).Detail(),
			wantStatus: nil,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			sv := &statusValidator{
				selfJobName: tt.selfJobName,
				ignoredJobs: tt.ignoredJobs,
				client:      tt.client,
			}
			got, err := sv.Validate(tt.ctx)
			if (err != nil) != tt.wantErr {
				t.Errorf("statusValidator.Validate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				if err.Error() != tt.wantErrStr {
					t.Errorf("statusValidator.Validate() error.Error() = %s, wantErrStr %s", err.Error(), tt.wantErrStr)
				}
			}
			if !reflect.DeepEqual(got, tt.wantStatus) {
				t.Errorf("statusValidator.Validate() status = %v, want %v", got, tt.wantStatus)
			}
		})
	}
}

func Test_statusValidator_listStatuses(t *testing.T) {
	type fields struct {
		repo        string
		owner       string
		ref         string
		selfJobName string
		client      github.Client
	}
	type test struct {
		fields  fields
		ctx     context.Context
		wantErr bool
		want    []*ghaStatus
	}
	tests := map[string]test{
		"succeeds to get job statuses even if the same job exists": func() test {
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{
						Statuses: []*github.RepoStatus{
							// The first element here is the latest state.
							{
								Context: stringPtr("job-01"),
								State:   stringPtr(string(successState)),
							},
							{
								Context: stringPtr("job-01"), // Same as above job name, and thus should be disregarded as old job status.
								State:   stringPtr(string(errorState)),
							},
						},
					}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							// The first element here is the latest state.
							{
								Name:   stringPtr("job-02"),
								Status: stringPtr("failure"),
							},
							{
								Name:       stringPtr("job-02"), // Same as above job name, and thus should be disregarded as old job status.
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunNeutralConclusion),
							},
							{
								Name:       stringPtr("job-03"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunNeutralConclusion),
							},
							{
								Name:       stringPtr("job-04"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
							},
							{
								Name:       stringPtr("job-05"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr("failure"),
							},
							{
								Name:       stringPtr("job-06"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSkipConclusion),
							},
						},
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want: []*ghaStatus{
					{
						Job:   "job-01",
						State: successState,
					},
					{
						Job:   "job-02",
						State: pendingState,
					},
					{
						Job:   "job-03",
						State: successState,
					},
					{
						Job:   "job-04",
						State: successState,
					},
					{
						Job:   "job-05",
						State: errorState,
					},
				},
			}
		}(),
		"returns error when the GetCombinedStatus returns an error": func() test {
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return nil, nil, errors.New("err")
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: true,
			}
		}(),
		"returns error when the GetCombinedStatus response is invalid": func() test {
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{
						Statuses: []*github.RepoStatus{
							{},
						},
					}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{CheckRuns: []*github.CheckRun{}}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: true,
			}
		}(),
		"returns error when the ListCheckRunsForRef returns an error": func() test {
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return nil, nil, errors.New("error")
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: true,
			}
		}(),
		"returns error when the ListCheckRunsForRef response is invalid": func() test {
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{},
						},
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: true,
			}
		}(),
		"returns nil when no error occurs": func() test {
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{
						Statuses: []*github.RepoStatus{
							{
								Context: stringPtr("job-01"),
								State:   stringPtr(string(successState)),
							},
						},
					}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								Name:   stringPtr("job-02"),
								Status: stringPtr("failure"),
							},
							{
								Name:       stringPtr("job-03"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunNeutralConclusion),
							},
							{
								Name:       stringPtr("job-04"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
							},
							{
								Name:       stringPtr("job-05"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr("failure"),
							},
							{
								Name:       stringPtr("job-06"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSkipConclusion),
							},
						},
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want: []*ghaStatus{
					{
						Job:   "job-01",
						State: successState,
					},
					{
						Job:   "job-02",
						State: pendingState,
					},
					{
						Job:   "job-03",
						State: successState,
					},
					{
						Job:   "job-04",
						State: successState,
					},
					{
						Job:   "job-05",
						State: errorState,
					},
				},
			}
		}(),
		"succeeds to retrieve 100 statuses": func() test {
			num_statuses := 100
			statuses := make([]*github.RepoStatus, num_statuses)
			checkRuns := make([]*github.CheckRun, num_statuses)
			expectedGhaStatuses := make([]*ghaStatus, num_statuses)
			for i := 0; i < num_statuses; i++ {
				statuses[i] = &github.RepoStatus{
					Context: stringPtr(fmt.Sprintf("job-%d", i)),
					State:   stringPtr(string(successState)),
				}

				checkRuns[i] = &github.CheckRun{
					Name:       stringPtr(fmt.Sprintf("job-%d", i)),
					Status:     stringPtr(checkRunCompletedStatus),
					Conclusion: stringPtr(checkRunNeutralConclusion),
				}

				expectedGhaStatuses[i] = &ghaStatus{
					Job:   fmt.Sprintf("job-%d", i),
					State: successState,
				}
			}
			sort.Slice(expectedGhaStatuses, func(i, j int) bool { return expectedGhaStatuses[i].Job < expectedGhaStatuses[j].Job })

			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					max := min(opts.Page*opts.PerPage, len(statuses))
					sts := statuses[(opts.Page-1)*opts.PerPage : max]
					total := len(statuses)
					return &github.CombinedStatus{
						Statuses:   sts,
						TotalCount: &total,
					}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					l := len(checkRuns)
					return &github.ListCheckRunsResults{
						CheckRuns: checkRuns,
						Total:     &l,
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want:    expectedGhaStatuses,
			}
		}(),
		"succeeds to retrieve 162 statuses": func() test {
			num_statuses := 162
			statuses := make([]*github.RepoStatus, num_statuses)
			checkRuns := make([]*github.CheckRun, num_statuses)
			expectedGhaStatuses := make([]*ghaStatus, num_statuses)
			for i := 0; i < num_statuses; i++ {
				statuses[i] = &github.RepoStatus{
					Context: stringPtr(fmt.Sprintf("job-%d", i)),
					State:   stringPtr(string(successState)),
				}

				checkRuns[i] = &github.CheckRun{
					Name:       stringPtr(fmt.Sprintf("job-%d", i)),
					Status:     stringPtr(checkRunCompletedStatus),
					Conclusion: stringPtr(checkRunNeutralConclusion),
				}

				expectedGhaStatuses[i] = &ghaStatus{
					Job:   fmt.Sprintf("job-%d", i),
					State: successState,
				}
			}
			sort.Slice(expectedGhaStatuses, func(i, j int) bool { return expectedGhaStatuses[i].Job < expectedGhaStatuses[j].Job })

			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					max := min(opts.Page*opts.PerPage, len(statuses))
					sts := statuses[(opts.Page-1)*opts.PerPage : max]
					total := len(statuses)
					return &github.CombinedStatus{
						Statuses:   sts,
						TotalCount: &total,
					}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					l := len(checkRuns)
					return &github.ListCheckRunsResults{
						CheckRuns: checkRuns,
						Total:     &l,
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want:    expectedGhaStatuses,
			}
		}(),
		"succeeds to retrieve 587 statuses": func() test {
			num_statuses := 587
			statuses := make([]*github.RepoStatus, num_statuses)
			checkRuns := make([]*github.CheckRun, num_statuses)
			expectedGhaStatuses := make([]*ghaStatus, num_statuses)
			for i := 0; i < num_statuses; i++ {
				statuses[i] = &github.RepoStatus{
					Context: stringPtr(fmt.Sprintf("job-%d", i)),
					State:   stringPtr(string(successState)),
				}

				checkRuns[i] = &github.CheckRun{
					Name:       stringPtr(fmt.Sprintf("job-%d", i)),
					Status:     stringPtr(checkRunCompletedStatus),
					Conclusion: stringPtr(checkRunNeutralConclusion),
				}

				expectedGhaStatuses[i] = &ghaStatus{
					Job:   fmt.Sprintf("job-%d", i),
					State: successState,
				}
			}
			sort.Slice(expectedGhaStatuses, func(i, j int) bool { return expectedGhaStatuses[i].Job < expectedGhaStatuses[j].Job })

			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					max := min(opts.Page*opts.PerPage, len(statuses))
					sts := statuses[(opts.Page-1)*opts.PerPage : max]
					total := len(statuses)
					return &github.CombinedStatus{
						Statuses:   sts,
						TotalCount: &total,
					}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					l := len(checkRuns)
					return &github.ListCheckRunsResults{
						CheckRuns: checkRuns,
						Total:     &l,
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want:    expectedGhaStatuses,
			}
		}(),
		"ignores matrix parent in cancelled state when children exist": func() test {
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								ID:         int64Ptr(1),
								Name:       stringPtr("build"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunCancelledConclusion),
							},
							{
								ID:         int64Ptr(2),
								Name:       stringPtr("build (ubuntu)"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
							},
							{
								ID:         int64Ptr(3),
								Name:       stringPtr("build (macos)"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
							},
						},
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want: []*ghaStatus{
					{Job: "build (macos)", State: successState},
					{Job: "build (ubuntu)", State: successState},
				},
			}
		}(),
		"ignores matrix parent stuck in-progress when children succeed": func() test {
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								ID:     int64Ptr(1),
								Name:   stringPtr("build"),
								Status: stringPtr("in_progress"),
							},
							{
								ID:         int64Ptr(2),
								Name:       stringPtr("build (ubuntu)"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
							},
							{
								ID:         int64Ptr(3),
								Name:       stringPtr("build (macos)"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
							},
						},
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want: []*ghaStatus{
					{Job: "build (macos)", State: successState},
					{Job: "build (ubuntu)", State: successState},
				},
			}
		}(),
		"includes matrix parent with successful terminal state": func() test {
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								ID:         int64Ptr(1),
								Name:       stringPtr("build"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
							},
							{
								ID:         int64Ptr(2),
								Name:       stringPtr("build (ubuntu)"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
							},
							{
								ID:         int64Ptr(3),
								Name:       stringPtr("build (macos)"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
							},
						},
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want: []*ghaStatus{
					{Job: "build", State: successState},
					{Job: "build (macos)", State: successState},
					{Job: "build (ubuntu)", State: successState},
				},
			}
		}(),
		"does not ignore falsely detected matrix parent with failure": func() test {
			// "deploy" and "deploy (staging)" are independent jobs, not a matrix.
			// "deploy" should NOT be silently dropped just because its name is a
			// prefix of "deploy (staging)".
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								ID:         int64Ptr(1),
								Name:       stringPtr("deploy"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr("failure"),
							},
							{
								ID:         int64Ptr(2),
								Name:       stringPtr("deploy (staging)"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
							},
						},
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want: []*ghaStatus{
					{Job: "deploy", State: errorState},
					{Job: "deploy (staging)", State: successState},
				},
			}
		}(),
		"dedup prefers newer check suite over higher run ID": func() test {
			// Run A (suite 5000) is cancelled mid-flight; its job "test" got run ID 200.
			// Run B (suite 5001) replaces it; its job "test" got run ID 150 (started earlier
			// on a runner due to scheduling). Suite-based dedup should pick Run B's check.
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								ID:         int64Ptr(200),
								Name:       stringPtr("test"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunCancelledConclusion),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(5000)},
							},
							{
								ID:         int64Ptr(150),
								Name:       stringPtr("test"),
								Status:     stringPtr("in_progress"),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(5001)},
							},
						},
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want: []*ghaStatus{
					{Job: "test", State: pendingState}, // in_progress → pending
				},
			}
		}(),
		"dedup falls back to run ID when no check suite present": func() test {
			// Third-party integrations may not populate CheckSuite.
			// Dedup should fall back to run ID comparison.
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								ID:         int64Ptr(10),
								Name:       stringPtr("lint"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr("failure"),
							},
							{
								ID:         int64Ptr(20),
								Name:       stringPtr("lint"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
							},
						},
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want: []*ghaStatus{
					{Job: "lint", State: successState}, // higher run ID wins
				},
			}
		}(),
		"standalone cancelled check run reports cancelled state": func() test {
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								ID:         int64Ptr(1),
								Name:       stringPtr("lint"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunCancelledConclusion),
							},
							{
								ID:         int64Ptr(2),
								Name:       stringPtr("test"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
							},
						},
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want: []*ghaStatus{
					{Job: "lint", State: cancelledState},
					{Job: "test", State: successState},
				},
			}
		}(),
		"superseded suite: cancelled checks converted to pending when superseding run in-progress": func() test {
			// Old run (suite 100, workflow 1) was cancelled. New run (suite 200, workflow 1) is in-progress.
			// "test" only exists in the old suite — should be converted to pending, not hard-fail.
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								ID:         int64Ptr(1),
								Name:       stringPtr("build"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunCancelledConclusion),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(100)},
							},
							{
								ID:         int64Ptr(2),
								Name:       stringPtr("test"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunCancelledConclusion),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(100)},
							},
							{
								ID:         int64Ptr(3),
								Name:       stringPtr("build"),
								Status:     stringPtr("in_progress"),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(200)},
							},
						},
					}, nil, nil
				},
				ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
					wfID := int64(1)
					total := 2
					return &github.WorkflowRuns{
						TotalCount: &total,
						WorkflowRuns: []*github.WorkflowRun{
							{ID: int64Ptr(10), WorkflowID: &wfID, RunNumber: intPtr(1), RunAttempt: intPtr(1), CheckSuiteID: int64Ptr(100), Status: stringPtr("completed"), Conclusion: stringPtr("cancelled")},
							{ID: int64Ptr(11), WorkflowID: &wfID, RunNumber: intPtr(2), RunAttempt: intPtr(1), CheckSuiteID: int64Ptr(200), Status: stringPtr("in_progress")},
						},
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want: []*ghaStatus{
					{Job: "build", State: pendingState},  // dedup picks suite 200 (in-progress)
					{Job: "test", State: pendingState},    // converted from cancelled (superseded suite, superseding run in-progress)
				},
			}
		}(),
		"superseded suite: cancelled checks dropped when superseding run completed": func() test {
			// Old run (suite 100) cancelled, new run (suite 200) completed successfully.
			// "test" only in old suite — should be dropped (new run decided not to run it).
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								ID:         int64Ptr(1),
								Name:       stringPtr("build"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunCancelledConclusion),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(100)},
							},
							{
								ID:         int64Ptr(2),
								Name:       stringPtr("test"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunCancelledConclusion),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(100)},
							},
							{
								ID:         int64Ptr(3),
								Name:       stringPtr("build"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(200)},
							},
						},
					}, nil, nil
				},
				ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
					wfID := int64(1)
					total := 2
					return &github.WorkflowRuns{
						TotalCount: &total,
						WorkflowRuns: []*github.WorkflowRun{
							{ID: int64Ptr(10), WorkflowID: &wfID, RunNumber: intPtr(1), RunAttempt: intPtr(1), CheckSuiteID: int64Ptr(100), Status: stringPtr("completed"), Conclusion: stringPtr("cancelled")},
							{ID: int64Ptr(11), WorkflowID: &wfID, RunNumber: intPtr(2), RunAttempt: intPtr(1), CheckSuiteID: int64Ptr(200), Status: stringPtr("completed"), Conclusion: stringPtr("success")},
						},
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want: []*ghaStatus{
					{Job: "build", State: successState}, // dedup picks suite 200 (success)
					// "test" dropped — superseded suite, superseding run completed
				},
			}
		}(),
		"superseded suite: success checks from old suite kept (re-run failed jobs)": func() test {
			// Old run (suite 100): build=success, test=failure. Re-run (suite 200): test=in-progress.
			// build=success from old suite should be kept (not re-run in new attempt).
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								ID:         int64Ptr(1),
								Name:       stringPtr("build"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(100)},
							},
							{
								ID:         int64Ptr(2),
								Name:       stringPtr("test"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr("failure"),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(100)},
							},
							{
								ID:         int64Ptr(3),
								Name:       stringPtr("test"),
								Status:     stringPtr("in_progress"),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(200)},
							},
						},
					}, nil, nil
				},
				ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
					wfID := int64(1)
					total := 2
					return &github.WorkflowRuns{
						TotalCount: &total,
						WorkflowRuns: []*github.WorkflowRun{
							{ID: int64Ptr(10), WorkflowID: &wfID, RunNumber: intPtr(1), RunAttempt: intPtr(1), CheckSuiteID: int64Ptr(100), Status: stringPtr("completed"), Conclusion: stringPtr("failure")},
							{ID: int64Ptr(11), WorkflowID: &wfID, RunNumber: intPtr(1), RunAttempt: intPtr(2), CheckSuiteID: int64Ptr(200), Status: stringPtr("in_progress")},
						},
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want: []*ghaStatus{
					{Job: "build", State: successState}, // kept from superseded suite (success)
					{Job: "test", State: pendingState},  // dedup picks suite 200 (in-progress)
				},
			}
		}(),
		"superseded suite: single suite skips workflow runs API": func() test {
			// Only one suite — no workflow runs API call should be made.
			// ListRepositoryWorkflowRunsFunc panics if called, proving it's not invoked.
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								ID:         int64Ptr(1),
								Name:       stringPtr("build"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(100)},
							},
							{
								ID:         int64Ptr(2),
								Name:       stringPtr("test"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunSuccessConclusion),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(100)},
							},
						},
					}, nil, nil
				},
				ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
					panic("ListRepositoryWorkflowRuns should not be called for single suite")
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want: []*ghaStatus{
					{Job: "build", State: successState},
					{Job: "test", State: successState},
				},
			}
		}(),
		"superseded suite: multi-workflow no cross-contamination": func() test {
			// ci.yml (workflow 1): suite 100 cancelled, suite 200 in-progress.
			// deploy.yml (workflow 2): suite 150, running independently.
			// Suite 150 should NOT be marked superseded by suite 200 (different workflow).
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								ID:         int64Ptr(1),
								Name:       stringPtr("build"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunCancelledConclusion),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(100)},
							},
							{
								ID:         int64Ptr(2),
								Name:       stringPtr("build"),
								Status:     stringPtr("in_progress"),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(200)},
							},
							{
								ID:         int64Ptr(3),
								Name:       stringPtr("deploy"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunCancelledConclusion),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(150)},
							},
						},
					}, nil, nil
				},
				ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
					wf1 := int64(1) // ci.yml
					wf2 := int64(2) // deploy.yml
					total := 3
					return &github.WorkflowRuns{
						TotalCount: &total,
						WorkflowRuns: []*github.WorkflowRun{
							{ID: int64Ptr(10), WorkflowID: &wf1, RunNumber: intPtr(1), RunAttempt: intPtr(1), CheckSuiteID: int64Ptr(100), Status: stringPtr("completed"), Conclusion: stringPtr("cancelled")},
							{ID: int64Ptr(11), WorkflowID: &wf1, RunNumber: intPtr(2), RunAttempt: intPtr(1), CheckSuiteID: int64Ptr(200), Status: stringPtr("in_progress")},
							{ID: int64Ptr(12), WorkflowID: &wf2, RunNumber: intPtr(1), RunAttempt: intPtr(1), CheckSuiteID: int64Ptr(150), Status: stringPtr("completed"), Conclusion: stringPtr("cancelled")},
						},
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want: []*ghaStatus{
					{Job: "build", State: pendingState},     // dedup picks suite 200 (in-progress)
					{Job: "deploy", State: cancelledState},  // suite 150 NOT superseded (only run of workflow 2, and it's cancelled)
				},
			}
		}(),
		"superseded suite: all runs cancelled, nothing superseded": func() test {
			// Both runs of the same workflow are cancelled. Nothing is superseded.
			// Cancelled checks pass through to the existing hard-fail path.
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								ID:         int64Ptr(1),
								Name:       stringPtr("build"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunCancelledConclusion),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(100)},
							},
							{
								ID:         int64Ptr(2),
								Name:       stringPtr("build"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunCancelledConclusion),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(200)},
							},
						},
					}, nil, nil
				},
				ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
					wfID := int64(1)
					total := 2
					return &github.WorkflowRuns{
						TotalCount: &total,
						WorkflowRuns: []*github.WorkflowRun{
							{ID: int64Ptr(10), WorkflowID: &wfID, RunNumber: intPtr(1), RunAttempt: intPtr(1), CheckSuiteID: int64Ptr(100), Status: stringPtr("completed"), Conclusion: stringPtr("cancelled")},
							{ID: int64Ptr(11), WorkflowID: &wfID, RunNumber: intPtr(2), RunAttempt: intPtr(1), CheckSuiteID: int64Ptr(200), Status: stringPtr("completed"), Conclusion: stringPtr("cancelled")},
						},
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want: []*ghaStatus{
					{Job: "build", State: cancelledState}, // dedup picks suite 200 (higher), still cancelled
				},
			}
		}(),
		"superseded suite: workflow runs API failure returns error": func() test {
			// API failure should propagate as an error (actions: read is required).
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								ID:         int64Ptr(1),
								Name:       stringPtr("build"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunCancelledConclusion),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(100)},
							},
							{
								ID:         int64Ptr(2),
								Name:       stringPtr("build"),
								Status:     stringPtr("in_progress"),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(200)},
							},
						},
					}, nil, nil
				},
				ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
					return nil, nil, errors.New("403 Forbidden: requires actions: read permission")
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: true,
			}
		}(),
		"superseded suite: third-party checks without suite unaffected": func() test {
			// Third-party checks (no CheckSuite) should not be affected by the pre-filter.
			c := &mock.Client{
				GetCombinedStatusFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListOptions) (*github.CombinedStatus, *github.Response, error) {
					return &github.CombinedStatus{}, nil, nil
				},
				ListCheckRunsForRefFunc: func(ctx context.Context, owner, repo, ref string, opts *github.ListCheckRunsOptions) (*github.ListCheckRunsResults, *github.Response, error) {
					return &github.ListCheckRunsResults{
						CheckRuns: []*github.CheckRun{
							{
								ID:         int64Ptr(1),
								Name:       stringPtr("ci/external"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunCancelledConclusion),
								// No CheckSuite
							},
							{
								ID:         int64Ptr(2),
								Name:       stringPtr("build"),
								Status:     stringPtr(checkRunCompletedStatus),
								Conclusion: stringPtr(checkRunCancelledConclusion),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(100)},
							},
							{
								ID:         int64Ptr(3),
								Name:       stringPtr("build"),
								Status:     stringPtr("in_progress"),
								CheckSuite: &github.CheckSuite{ID: int64Ptr(200)},
							},
						},
					}, nil, nil
				},
				ListRepositoryWorkflowRunsFunc: func(ctx context.Context, owner, repo string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
					wfID := int64(1)
					total := 2
					return &github.WorkflowRuns{
						TotalCount: &total,
						WorkflowRuns: []*github.WorkflowRun{
							{ID: int64Ptr(10), WorkflowID: &wfID, RunNumber: intPtr(1), RunAttempt: intPtr(1), CheckSuiteID: int64Ptr(100), Status: stringPtr("completed"), Conclusion: stringPtr("cancelled")},
							{ID: int64Ptr(11), WorkflowID: &wfID, RunNumber: intPtr(2), RunAttempt: intPtr(1), CheckSuiteID: int64Ptr(200), Status: stringPtr("in_progress")},
						},
					}, nil, nil
				},
			}
			return test{
				fields: fields{
					client:      c,
					selfJobName: "self-job",
					owner:       "test-owner",
					repo:        "test-repo",
					ref:         "main",
				},
				wantErr: false,
				want: []*ghaStatus{
					{Job: "build", State: pendingState},        // dedup picks suite 200 (in-progress)
					{Job: "ci/external", State: cancelledState}, // third-party, unaffected by pre-filter
				},
			}
		}(),
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			sv := &statusValidator{
				repo:        tt.fields.repo,
				owner:       tt.fields.owner,
				ref:         tt.fields.ref,
				selfJobName: tt.fields.selfJobName,
				client:      tt.fields.client,
			}
			got, err := sv.listGhaStatuses(tt.ctx)
			if (err != nil) != tt.wantErr {
				t.Errorf("statusValidator.listStatuses() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got, want := len(got), len(tt.want); got != want {
				t.Errorf("statusValidator.listStatuses() length = %v, want %v", got, want)
			}
			for i := range tt.want {
				if !reflect.DeepEqual(got[i], tt.want[i]) {
					t.Errorf("statusValidator.listStatuses() - %d = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
