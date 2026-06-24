package jobs

import (
	"errors"
	"fmt"
)

// Sentinel errors for job operations.
// These can be checked with errors.Is().
var (
	ErrJobNotFound   = errors.New("job not found")
	ErrJobNotRunning = errors.New("job is not running")
)

// jobNotFoundError returns a wrapped error for a missing job.
func jobNotFoundError(id string) error {
	return fmt.Errorf("%w: %s", ErrJobNotFound, id)
}

// jobNotRunningError returns a wrapped error for a job in an unexpected state.
func jobNotRunningError(id string, status Status) error {
	return fmt.Errorf("%w (status: %s): %s", ErrJobNotRunning, status, id)
}
