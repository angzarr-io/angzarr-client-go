package angzarr

import (
	"math"
	"math/rand"
	"time"
)

// RetryPolicy defines a strategy for retrying failed operations.
type RetryPolicy interface {
	// Execute runs the operation, retrying on failure according to the policy.
	// Returns the result of the first successful attempt, or the last error.
	Execute(operation func() error) error
}

// ExponentialBackoffRetry retries with exponential backoff and jitter.
type ExponentialBackoffRetry struct {
	MinDelay    time.Duration
	MaxDelay    time.Duration
	MaxAttempts int
	Jitter      bool
}

// DefaultRetryPolicy returns the standard retry policy matching Rust's backoff config.
func DefaultRetryPolicy() RetryPolicy {
	return &ExponentialBackoffRetry{
		MinDelay:    100 * time.Millisecond,
		MaxDelay:    5 * time.Second,
		MaxAttempts: 10,
		Jitter:      true,
	}
}

// Execute runs the operation with exponential backoff retries.
func (r *ExponentialBackoffRetry) Execute(operation func() error) error {
	var lastErr error
	for attempt := 0; attempt < r.MaxAttempts; attempt++ {
		err := operation()
		if err == nil {
			return nil
		}
		lastErr = err

		if attempt < r.MaxAttempts-1 {
			delay := r.computeDelay(attempt)
			time.Sleep(delay)
		}
	}
	return lastErr
}

func (r *ExponentialBackoffRetry) computeDelay(attempt int) time.Duration {
	delay := float64(r.MinDelay) * math.Pow(2, float64(attempt))
	if delay > float64(r.MaxDelay) {
		delay = float64(r.MaxDelay)
	}
	if r.Jitter {
		delay = delay * (0.5 + rand.Float64()*0.5)
	}
	return time.Duration(delay)
}
