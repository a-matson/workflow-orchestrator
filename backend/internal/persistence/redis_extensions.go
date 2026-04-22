package persistence

import (
	"context"
	"fmt"
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
