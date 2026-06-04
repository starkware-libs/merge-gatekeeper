package validators

import (
	"context"
)

type Status interface {
	Detail() string
	IsSuccess() bool
	// Fingerprint identifies the world state this status was computed from:
	// the resolved head SHA plus the gated job set (self/ignored excluded).
	// Two polls with equal fingerprints observed the same, quiescent world —
	// the polling loop requires the fingerprint to hold steady across
	// consecutive green polls before it reports success.
	Fingerprint() string
	// TrackedJobs reports how many gated jobs (excluding self/ignored) were
	// observed. Zero means the gate found nothing to wait on — suspicious
	// right after a push, when CI may simply not have materialized yet.
	TrackedJobs() int
}

type Validator interface {
	Name() string
	Validate(ctx context.Context) (Status, error)
}
