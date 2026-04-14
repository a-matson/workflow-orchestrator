package retry_test

import (
	"context"
	"testing"
	"time"

	"github.com/workflow-platform/backend/internal/models"
	"github.com/workflow-platform/backend/internal/retry"
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

func TestScheduleRetry_SetsNextRetryAt(t *testing.T) {
	mgr := retry.NewManager()
	policy := &models.RetryPolicy{
		MaxRetries:      3,
		InitialDelay:    500 * time.Millisecond,
		MaxDelay:        10 * time.Second,
		BackoffMultiple: 2.0,
		Jitter:          false,
	}

	task := &models.TaskExecution{ID: "task-1", RetryCount: 0}
	before := time.Now()
	mgr.ScheduleRetry(context.TODO(), task, policy, "test error")
	after := time.Now()

	if task.NextRetryAt == nil {
		t.Fatal("NextRetryAt should be set after ScheduleRetry")
	}
	if task.NextRetryAt.Before(before) {
		t.Error("NextRetryAt should be in the future")
	}
	if task.Status != models.TaskStatusRetrying {
		t.Errorf("expected status Retrying, got %s", task.Status)
	}
	if task.RetryCount != 1 {
		t.Errorf("expected RetryCount 1, got %d", task.RetryCount)
	}
	_ = after
}

func TestSendToDeadLetter(t *testing.T) {
	mgr := retry.NewManager()
	task := &models.TaskExecution{ID: "dead-task", RetryCount: 3, Error: "max retries"}
	mgr.SendToDeadLetter(task)

	if task.Status != models.TaskStatusDeadLetter {
		t.Errorf("expected DeadLetter status, got %s", task.Status)
	}

	// Should be receivable on the DLQ channel
	select {
	case dlTask := <-mgr.DeadLetterQueue():
		if dlTask.ID != task.ID {
			t.Errorf("expected task ID %s, got %s", task.ID, dlTask.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("dead letter task not received on DLQ channel")
	}
}

func TestReplayFromDeadLetter(t *testing.T) {
	mgr := retry.NewManager()
	task := &models.TaskExecution{
		ID:         "replay-task",
		Status:     models.TaskStatusDeadLetter,
		RetryCount: 5,
		Error:      "some error",
	}

	mgr.ReplayFromDeadLetter(task)

	if task.Status != models.TaskStatusPending {
		t.Errorf("expected Pending after replay, got %s", task.Status)
	}
	if task.RetryCount != 0 {
		t.Errorf("expected RetryCount 0 after replay, got %d", task.RetryCount)
	}
	if task.Error != "" {
		t.Errorf("expected empty error after replay, got %q", task.Error)
	}
}
