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

	// OnRetry fires before each backoff sleep with (attempt-index, err).
	// Does NOT fire before the first attempt and does NOT fire after the
	// final failure. Mirrors Rs `on_retry` (spec MED-6.3) and Ja
	// `Builder.onRetry`.
	OnRetry func(attempt int, err error)
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
			if r.OnRetry != nil {
				r.OnRetry(attempt+1, err)
			}
			delay := r.ComputeDelay(attempt)
			time.Sleep(delay)
		}
	}
	return lastErr
}

// ComputeDelay returns the backoff delay for the given attempt index (0-based).
//
// Spec MED-6.4 / MED-6.7: the exponent is capped at 30 to prevent
// IEEE-754 +Inf when MaxAttempts is configured large. Public so
// cross-language unit tests can pin the formula directly.
func (r *ExponentialBackoffRetry) ComputeDelay(attempt int) time.Duration {
	capped := attempt
	if capped > 30 {
		capped = 30
	}
	delay := float64(r.MinDelay) * math.Pow(2, float64(capped))
	if delay > float64(r.MaxDelay) {
		delay = float64(r.MaxDelay)
	}
	if r.Jitter {
		delay = delay * (0.5 + rand.Float64()*0.5)
	}
	return time.Duration(delay)
}

// computeDelay is the legacy package-private accessor kept as a thin
// forwarder so older test fixtures continue to compile.
func (r *ExponentialBackoffRetry) computeDelay(attempt int) time.Duration {
	return r.ComputeDelay(attempt)
}

// ExecuteFunc is the value-returning variant of RetryPolicy.Execute.
//
// Spec HIGH-6.2: side-effect-only Execute makes it awkward to retry
// connect()-style operations. ExecuteFunc[T] mirrors Py
// `RetryPolicy.execute(op) -> T`, Rs `execute<F,T,E>(op) -> Result<T,E>`,
// Ja `<T> T execute(Callable<T>)`.
//
// On success returns the value from the first successful attempt; on
// exhaustion returns the zero value of T plus the last error.
func ExecuteFunc[T any](policy RetryPolicy, op func() (T, error)) (T, error) {
	var (
		out T
		err error
	)
	wrapped := func() error {
		out, err = op()
		return err
	}
	if execErr := policy.Execute(wrapped); execErr != nil {
		var zero T
		return zero, execErr
	}
	return out, nil
}
