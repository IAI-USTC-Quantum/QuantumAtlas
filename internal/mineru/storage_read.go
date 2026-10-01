package mineru

import (
	"errors"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

// An unsuccessful cache probe is observational: no job, cooldown, acquisition
// events, miss counter, or external conversion is created. Recovery is immediate
// once storage is readable again.
func storageReadFailure(canonical string, err error) *Job {
	return &Job{
		Canonical: canonical,
		State:     JobStateFailed,
		Err:       errors.Join(objstore.ErrUnavailable, err),
		ErrKind:   ErrRetryable,
	}
}
