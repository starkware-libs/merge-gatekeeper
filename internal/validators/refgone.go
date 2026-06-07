package validators

import "fmt"

// RefGoneError reports that the watched ref resolved earlier in the run and
// then was missing for several consecutive polls — i.e. the branch or tag
// was deleted while the gatekeeper was running. Terminal, deliberately NOT
// transient: a deleted ref never comes back, so retrying would only ghost-
// poll the remaining timeout budget.
//
// No Unwrap(): Err is the last listing error, which is a TransientError —
// exposing it to errors.As would make IsTransient true and the polling loop
// would retry a terminal condition. It is carried for the message only.
type RefGoneError struct {
	Ref   string
	Polls int
	Err   error
}

func (e *RefGoneError) Error() string {
	return fmt.Sprintf(
		"ref %q not found for %d consecutive polls: the branch or tag was deleted while the gatekeeper was running (a merge-queue ref disappears when its batch merges or is dequeued); last error: %v",
		e.Ref, e.Polls, e.Err)
}
