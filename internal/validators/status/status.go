package status

import "fmt"

type status struct {
	totalJobs     []string
	completeJobs  []string
	failedJobs       []string
	cancelledJobs []string
	ignoredJobs   []string
	succeeded     bool
}

func prettyPrintJobList(jobs []string) string {
	result := ""
	if len(jobs) == 0 {
		result = "[]"
	}
	for i, job := range jobs {
		result += fmt.Sprintf("- %s", job)
		if i != len(jobs)-1 {
			result += "\n"
		}
	}

	return result
}

func (s *status) Detail() string {
	result := fmt.Sprintf(
		`%d out of %d

Total job count:       %d
Completed job count:   %d
Incompleted job count: %d
Failed job count:      %d
Cancelled job count:   %d
Ignored job count:     %d
`,
		len(s.completeJobs), len(s.totalJobs),
		len(s.totalJobs),
		len(s.completeJobs),
		len(s.getIncompleteJobs()),
		len(s.failedJobs),
		len(s.cancelledJobs),
		len(s.ignoredJobs),
	)

	result = fmt.Sprintf(`%s
::group::Failed jobs
%s
::endgroup::

::group::Completed jobs
%s
::endgroup::

::group::Incomplete jobs
%s
::endgroup::

::group::Cancelled jobs
%s
::endgroup::

::group::Ignored jobs
%s
::endgroup::

::group::All jobs
%s
::endgroup::
`,
		result,
		prettyPrintJobList(s.failedJobs),
		prettyPrintJobList(s.completeJobs),
		prettyPrintJobList(s.getIncompleteJobs()),
		prettyPrintJobList(s.cancelledJobs),
		prettyPrintJobList(s.ignoredJobs),
		prettyPrintJobList(s.totalJobs),
	)

	return result
}

func (s *status) IsSuccess() bool {
	return s.succeeded
}

func (s *status) getIncompleteJobs() []string {
	done := make(map[string]struct{}, len(s.completeJobs)+len(s.failedJobs)+len(s.cancelledJobs)+len(s.ignoredJobs))
	for _, j := range s.completeJobs {
		done[j] = struct{}{}
	}
	for _, j := range s.failedJobs {
		done[j] = struct{}{}
	}
	for _, j := range s.cancelledJobs {
		done[j] = struct{}{}
	}
	for _, j := range s.ignoredJobs {
		done[j] = struct{}{}
	}

	var incomplete []string
	for _, job := range s.totalJobs {
		if _, ok := done[job]; !ok {
			incomplete = append(incomplete, job)
		}
	}
	return incomplete
}
