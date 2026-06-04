package mock

import (
	"context"

	"github.com/starkware-libs/merge-gatekeeper/internal/validators"
)

type Status struct {
	DetailFunc      func() string
	IsSuccessFunc   func() bool
	FingerprintFunc func() string
	TrackedJobsFunc func() int
}

func (s *Status) Detail() string {
	return s.DetailFunc()
}

func (s *Status) IsSuccess() bool {
	return s.IsSuccessFunc()
}

// Fingerprint and TrackedJobs are nil-safe so existing tests that only set
// Detail/IsSuccess keep working. NOTE: the zero values make every such status
// look like an EMPTY job set with a constant fingerprint — cli tests rely on
// TestMain setting confirmPolls=1 and emptyGraceSeconds=0 so the loop's
// quiescence and empty-set-grace branches stay neutral for them.

func (s *Status) Fingerprint() string {
	if s.FingerprintFunc == nil {
		return ""
	}
	return s.FingerprintFunc()
}

func (s *Status) TrackedJobs() int {
	if s.TrackedJobsFunc == nil {
		return 0
	}
	return s.TrackedJobsFunc()
}

type Validator struct {
	NameFunc     func() string
	ValidateFunc func(ctx context.Context) (validators.Status, error)
}

func (v *Validator) Name() string {
	return v.NameFunc()
}

func (v *Validator) Validate(ctx context.Context) (validators.Status, error) {
	return v.ValidateFunc(ctx)
}

var (
	_ validators.Validator = &Validator{}
	_ validators.Status    = &Status{}
)
