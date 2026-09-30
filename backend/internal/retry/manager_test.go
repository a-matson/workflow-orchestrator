package retry_test

import (
	"testing"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/retry"
)

func TestNextRetryDelay_ExponentialGrowth(t *testing.T) {
	mgr := retry.NewManager()
	policy := &models.RetryPolicy{
		InitialDelay:    1 * time.Second,
		MaxDelay:        5 * time.Minute,
		BackoffMultiple: 2.0,
		Jitter:          false,
	}

	delays := make([]time.Duration, 5)
	for i := range delays {
		delays[i] = mgr.NextRetryDelay(i, policy)
	}

	// Each delay should be ~2x the previous
	for i := 1; i < len(delays); i++ {
		ratio := float64(delays[i]) / float64(delays[i-1])
		if ratio < 1.9 || ratio > 2.1 {
			t.Errorf("retry %d: expected ~2x growth, got ratio %.2f (delay=%s)", i, ratio, delays[i])
		}
	}
}

func TestNextRetryDelay_MaxDelayCap(t *testing.T) {
	mgr := retry.NewManager()
	policy := &models.RetryPolicy{
		InitialDelay:    1 * time.Second,
		MaxDelay:        10 * time.Second,
		BackoffMultiple: 10.0,
		Jitter:          false,
	}

	for i := 0; i < 10; i++ {
		d := mgr.NextRetryDelay(i, policy)
		if d > policy.MaxDelay+time.Millisecond {
			t.Errorf("retry %d: delay %s exceeds max %s", i, d, policy.MaxDelay)
		}
	}
}

func TestNextRetryDelay_JitterAdded(t *testing.T) {
	mgr := retry.NewManager()
	policy := &models.RetryPolicy{
		InitialDelay:    1 * time.Second,
		MaxDelay:        1 * time.Minute,
		BackoffMultiple: 2.0,
		Jitter:          true,
	}

	// Collect 20 delays at attempt 0; they should vary
	seen := map[time.Duration]bool{}
	for i := 0; i < 20; i++ {
		seen[mgr.NextRetryDelay(0, policy)] = true
	}
	if len(seen) == 1 {
		t.Error("jitter enabled but all delays identical — jitter not applied")
	}
}

func TestShouldRetry(t *testing.T) {
	mgr := retry.NewManager()
	policy := &models.RetryPolicy{MaxRetries: 3}

	task := &models.TaskExecution{RetryCount: 0}
	if !mgr.ShouldRetry(task, policy) {
		t.Error("should retry at count 0")
	}

	task.RetryCount = 3
	if mgr.ShouldRetry(task, policy) {
		t.Error("should NOT retry at max retries")
	}
}
