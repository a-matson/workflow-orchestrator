//go:build integration

package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

func setupRedis(t *testing.T) *persistence.RedisClient {
	t.Helper()
	_, client := testutil.Env(t)
	return client
}

func TestRedis_EnqueueAndDequeue(t *testing.T) {
	client := setupRedis(t)
	ctx := context.Background()

	msg := &models.TaskMessage{
		TaskExecID:       uuid.NewString(),
		WorkflowExecID:   uuid.NewString(),
		TaskDefinitionID: "def-1",
		TaskName:         "Test Task",
		TaskType:         "generic",
		EnqueuedAt:       time.Now(),
	}

	if err := client.EnqueueTask(ctx, msg); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	got := testutil.Drain(t, client, 1)[0]
	if got.TaskExecID != msg.TaskExecID {
		t.Errorf("task exec ID mismatch: %s != %s", got.TaskExecID, msg.TaskExecID)
	}
}

func TestRedis_PublishAndConsumeResult(t *testing.T) {
	client := setupRedis(t)
	ctx := context.Background()

	result := &models.TaskResult{
		TaskExecID:     uuid.NewString(),
		WorkflowExecID: uuid.NewString(),
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
		t.Errorf("task exec ID mismatch: %s != %s", got.TaskExecID, result.TaskExecID)
	}
	if !got.Success {
		t.Error("expected Success=true")
	}
}

func TestRedis_DeadLetter(t *testing.T) {
	client := setupRedis(t)

	msg := &models.TaskMessage{
		TaskExecID:     uuid.NewString(),
		WorkflowExecID: uuid.NewString(),
	}

	// The client has no read API for the dead-letter list, so this only
	// checks the push succeeds.
	if err := client.SendToDeadLetter(context.Background(), msg, "max retries exceeded"); err != nil {
		t.Fatalf("send to DLQ failed: %v", err)
	}
}
