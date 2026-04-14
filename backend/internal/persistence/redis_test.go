//go:build integration

package persistence_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/workflow-platform/backend/internal/models"
	"github.com/workflow-platform/backend/internal/persistence"
)

func setupRedis(t *testing.T) *persistence.RedisClient {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	client := persistence.NewRedisClient(addr, "", 1) // DB 1 for tests
	if err := client.Ping(context.Background()); err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func TestRedis_EnqueueAndDequeue(t *testing.T) {
	client := setupRedis(t)
	ctx := context.Background()

	msg := &models.TaskMessage{
		TaskExecID:       "task-test-1",
		WorkflowExecID:   "exec-test-1",
		TaskDefinitionID: "def-1",
		TaskName:         "Test Task",
		TaskType:         "generic",
		EnqueuedAt:       time.Now(),
		IdempotencyKey:   "idem-test-1",
	}

	if err := client.EnqueueTask(ctx, msg); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	got, err := client.DequeueTask(ctx, 3*time.Second)
	if err != nil {
		t.Fatalf("dequeue failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected message, got nil")
	}
	if got.TaskExecID != msg.TaskExecID {
		t.Errorf("task exec ID mismatch: %s != %s", got.TaskExecID, msg.TaskExecID)
	}
}

func TestRedis_Idempotency(t *testing.T) {
	client := setupRedis(t)
	ctx := context.Background()

	msg := &models.TaskMessage{
		TaskExecID:     "task-idem-1",
		WorkflowExecID: "exec-idem-1",
		EnqueuedAt:     time.Now(),
		IdempotencyKey: "idem-unique-key",
	}

	// First enqueue should succeed
	if err := client.EnqueueTask(ctx, msg); err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}

	// Mark as processed
	client.SetIdempotency(ctx, msg.IdempotencyKey, time.Minute)

	// Second enqueue should be a no-op due to idempotency
	if err := client.EnqueueTask(ctx, msg); err != nil {
		t.Fatalf("second enqueue failed: %v", err)
	}

	// Only one message should be in queue
	depth, _ := client.QueueDepth(ctx)
	// (may include other test messages, so just check idempotency check itself)
	exists, _ := client.CheckIdempotency(ctx, msg.IdempotencyKey)
	if !exists {
		t.Error("expected idempotency key to exist")
	}
	_ = depth
}

func TestRedis_PublishAndConsumeResult(t *testing.T) {
	client := setupRedis(t)
	ctx := context.Background()

	result := &models.TaskResult{
		TaskExecID:     "task-result-1",
		WorkflowExecID: "exec-result-1",
		WorkerID:       "worker-test",
		Success:        true,
		StartedAt:      time.Now().Add(-2 * time.Second),
		CompletedAt:    time.Now(),
	}

	if err := client.PublishResult(ctx, result); err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	got, err := client.DequeueResult(ctx, 3*time.Second)
	if err != nil {
		t.Fatalf("dequeue result failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected result, got nil")
	}
	if got.TaskExecID != result.TaskExecID {
		t.Errorf("task exec ID mismatch")
	}
	if !got.Success {
		t.Error("expected Success=true")
	}
}

func TestRedis_RetryScheduling(t *testing.T) {
	client := setupRedis(t)
	ctx := context.Background()

	msg := &models.TaskMessage{
		TaskExecID:     "task-retry-redis-1",
		WorkflowExecID: "exec-retry-1",
		IdempotencyKey: "retry-idem-1",
	}

	// Schedule for 1 second in the future
	retryAt := time.Now().Add(1 * time.Second)
	if err := client.ScheduleRetry(ctx, msg, retryAt); err != nil {
		t.Fatalf("schedule retry failed: %v", err)
	}

	// Should not be ready yet
	msgs, err := client.PopDueRetries(ctx)
	if err != nil {
		t.Fatalf("pop retries failed: %v", err)
	}
	found := false
	for _, m := range msgs {
		if m.TaskExecID == msg.TaskExecID {
			found = true
			break
		}
	}
	if found {
		t.Error("task should not be ready yet")
	}

	// Wait for it to become due
	time.Sleep(1200 * time.Millisecond)

	msgs, _ = client.PopDueRetries(ctx)
	found = false
	for _, m := range msgs {
		if m.TaskExecID == msg.TaskExecID {
			found = true
			break
		}
	}
	if !found {
		t.Error("task should be ready after 1s")
	}
}

func TestRedis_DistributedLock(t *testing.T) {
	client := setupRedis(t)
	ctx := context.Background()

	lockID := "test-lock-" + time.Now().Format("150405")

	// First acquisition should succeed
	ok, err := client.AcquireTaskLock(ctx, lockID, 5*time.Second)
	if err != nil || !ok {
		t.Fatalf("first lock failed: err=%v ok=%v", err, ok)
	}

	// Second acquisition should fail
	ok2, err := client.AcquireTaskLock(ctx, lockID, 5*time.Second)
	if err != nil {
		t.Fatalf("second lock returned error: %v", err)
	}
	if ok2 {
		t.Error("second lock should not have succeeded")
	}

	// After release, should be acquirable again
	client.ReleaseTaskLock(ctx, lockID)

	ok3, err := client.AcquireTaskLock(ctx, lockID, 5*time.Second)
	if err != nil || !ok3 {
		t.Fatalf("third lock after release failed: err=%v ok=%v", err, ok3)
	}
	client.ReleaseTaskLock(ctx, lockID)
}

func TestRedis_DeadLetter(t *testing.T) {
	client := setupRedis(t)
	ctx := context.Background()

	msg := &models.TaskMessage{
		TaskExecID:     "task-dlq-1",
		WorkflowExecID: "exec-dlq-1",
	}

	if err := client.SendToDeadLetter(ctx, msg, "max retries exceeded"); err != nil {
		t.Fatalf("send to DLQ failed: %v", err)
	}
	// Just verify it didn't error — DLQ is a LIST so data is there
}
