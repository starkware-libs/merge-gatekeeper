package validators

import "errors"

// TransientError marks a validation error caused by infrastructure — a
// GitHub API call that failed — rather than by CI results. The polling loop
// keeps retrying on transient errors until its deadline instead of failing
// the gatekeeper on the first hiccup; real validation outcomes (failed or
// cancelled jobs, invalid configuration) are returned untyped and abort the
// run immediately.
type TransientError struct {
	Err error
}

func (e *TransientError) Error() string { return e.Err.Error() }
func (e *TransientError) Unwrap() error { return e.Err }

// IsTransient reports whether err is (or wraps) a TransientError.
func IsTransient(err error) bool {
	var te *TransientError
	return errors.As(err, &te)
}
