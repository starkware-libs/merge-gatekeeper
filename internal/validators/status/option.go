package status

import "strings"

// DebugLog is called with debug messages when debug is enabled. Use fmt-style format and args.
// In GitHub Actions, pass a function that prefixes with "::debug::" so messages show in debug output.
type DebugLog func(format string, args ...interface{})

type Option func(s *statusValidator)

func WithSelfJob(name string) Option {
	return func(s *statusValidator) {
		if len(name) != 0 {
			s.selfJobName = name
		}
	}
}

func WithGitHubOwnerAndRepo(owner, repo string) Option {
	return func(s *statusValidator) {
		if len(owner) != 0 {
			s.owner = owner
		}
		if len(repo) != 0 {
			s.repo = repo
		}
	}
}

func WithGitHubRef(ref string) Option {
	return func(s *statusValidator) {
		if len(ref) != 0 {
			s.ref = ref
		}
	}
}

func WithIgnoredJobs(names string) Option {
	return func(s *statusValidator) {
		if len(names) == 0 {
			return // No ignored jobs specified
		}

		jobs := []string{}
		ss := strings.Split(names, ",")
		for _, s := range ss {
			jobName := strings.TrimSpace(s)
			if len(jobName) == 0 {
				continue
			}
			jobs = append(jobs, jobName)
		}
		s.ignoredJobs = jobs
	}
}

func WithDebugLog(f DebugLog) Option {
	return func(s *statusValidator) {
		s.debugLog = f
	}
}
