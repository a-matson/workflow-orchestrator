package persistence

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

// IsWorkerAlive checks if a worker's heartbeat key still exists in Redis.
// Returns false if the key has expired (worker presumed dead).
func (r *RedisClient) IsWorkerAlive(ctx context.Context, workerID string) (bool, error) {
	key := fmt.Sprintf("worker:heartbeat:%s", workerID)
	exists, err := r.client.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	return exists > 0, nil
}

// ListActiveWorkers returns IDs of all workers with live heartbeat keys.
func (r *RedisClient) ListActiveWorkers(ctx context.Context) ([]string, error) {
	pattern := "worker:heartbeat:*"
	keys, err := r.client.Keys(ctx, pattern).Result()
	if err != nil {
		return nil, err
	}

	prefix := "worker:heartbeat:"
	workers := make([]string, 0, len(keys))
	for _, key := range keys {
		if len(key) > len(prefix) {
			workers = append(workers, key[len(prefix):])
		}
	}
	return workers, nil
}

// PublishLiveLog streams a single log entry to the live_logs pub/sub channel
func (r *RedisClient) PublishLiveLog(ctx context.Context, taskExecID string, entry models.LogEntry) error {
	event := models.WebSocketEvent{
		Type: models.WSEventTaskLog,
		Payload: map[string]any{
			"task_exec_id": taskExecID,
			"entry":        entry,
		},
	}

	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return r.client.Publish(ctx, "workflow:live_logs", data).Err()
}

// ListenForLiveLogs subscribes to the pub/sub channel and triggers a callback
func (r *RedisClient) ListenForLiveLogs(ctx context.Context, onMessage func(payload string)) {
	pubsub := r.client.Subscribe(ctx, "workflow:live_logs")
	defer func() { _ = pubsub.Close() }()

	// Ensure the loop unblocks and cleans up if the application context is cancelled
	go func() {
		<-ctx.Done()
		_ = pubsub.Close()
	}()

	// Range securely over the channel until pubsub is closed
	for msg := range pubsub.Channel() {
		onMessage(msg.Payload)
	}
}
