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
