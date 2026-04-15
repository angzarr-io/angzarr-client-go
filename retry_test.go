package angzarr

import (
	"errors"
	"testing"
	"time"
)

func TestExponentialBackoffRetry_SucceedsImmediately(t *testing.T) {
	policy := &ExponentialBackoffRetry{
		MinDelay:    1 * time.Millisecond,
		MaxDelay:    10 * time.Millisecond,
		MaxAttempts: 3,
		Jitter:      false,
	}

	attempts := 0
	err := policy.Execute(func() error {
		attempts++
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", attempts)
	}
}

func TestExponentialBackoffRetry_RetriesOnFailure(t *testing.T) {
	policy := &ExponentialBackoffRetry{
		MinDelay:    1 * time.Millisecond,
		MaxDelay:    10 * time.Millisecond,
		MaxAttempts: 5,
		Jitter:      false,
	}

	attempts := 0
	err := policy.Execute(func() error {
		attempts++
		if attempts < 3 {
			return errors.New("transient failure")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestExponentialBackoffRetry_ExhaustsAttempts(t *testing.T) {
	policy := &ExponentialBackoffRetry{
		MinDelay:    1 * time.Millisecond,
		MaxDelay:    10 * time.Millisecond,
		MaxAttempts: 3,
		Jitter:      false,
	}

	attempts := 0
	err := policy.Execute(func() error {
		attempts++
		return errors.New("permanent failure")
	})

	if err == nil {
		t.Fatal("expected error after exhausting attempts")
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
	if err.Error() != "permanent failure" {
		t.Errorf("expected last error, got %q", err.Error())
	}
}

func TestExponentialBackoffRetry_RespectsMaxDelay(t *testing.T) {
	policy := &ExponentialBackoffRetry{
		MinDelay:    100 * time.Millisecond,
		MaxDelay:    200 * time.Millisecond,
		MaxAttempts: 10,
		Jitter:      false,
	}

	// Attempt 5: 100ms * 2^5 = 3200ms, should be capped at 200ms
	delay := policy.computeDelay(5)
	if delay > 200*time.Millisecond {
		t.Errorf("delay %v exceeds max delay 200ms", delay)
	}
}

func TestDefaultRetryPolicy(t *testing.T) {
	policy := DefaultRetryPolicy()
	if policy == nil {
		t.Fatal("expected non-nil default policy")
	}

	exp, ok := policy.(*ExponentialBackoffRetry)
	if !ok {
		t.Fatal("expected ExponentialBackoffRetry")
	}
	if exp.MinDelay != 100*time.Millisecond {
		t.Errorf("expected MinDelay 100ms, got %v", exp.MinDelay)
	}
	if exp.MaxDelay != 5*time.Second {
		t.Errorf("expected MaxDelay 5s, got %v", exp.MaxDelay)
	}
	if exp.MaxAttempts != 10 {
		t.Errorf("expected MaxAttempts 10, got %d", exp.MaxAttempts)
	}
	if !exp.Jitter {
		t.Error("expected Jitter enabled")
	}
}

func TestNewQueryClient_WithRetryPolicy(t *testing.T) {
	attempts := 0
	policy := &ExponentialBackoffRetry{
		MinDelay:    1 * time.Millisecond,
		MaxDelay:    5 * time.Millisecond,
		MaxAttempts: 3,
		Jitter:      false,
	}

	// Connecting to a non-existent endpoint should use the retry policy
	// grpc.NewClient doesn't actually connect until first RPC, so this tests
	// that the retry policy is accepted and stored
	_, _ = NewQueryClientWithRetry("localhost:99999", policy)
	_ = attempts
}

func TestNewCommandHandlerClient_WithRetryPolicy(t *testing.T) {
	policy := &ExponentialBackoffRetry{
		MinDelay:    1 * time.Millisecond,
		MaxDelay:    5 * time.Millisecond,
		MaxAttempts: 3,
		Jitter:      false,
	}

	_, _ = NewCommandHandlerClientWithRetry("localhost:99999", policy)
}
