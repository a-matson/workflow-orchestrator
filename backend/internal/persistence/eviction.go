package persistence

import (
	"context"
	"fmt"
)

// CheckEvictionPolicy accepts only noeviction: the queues, retry set and locks
// live in Redis, so an evicted key is a silently lost task.
func CheckEvictionPolicy(policy string) error {
	if policy == "noeviction" {
		return nil
	}
	return fmt.Errorf("redis maxmemory-policy %q may evict queued tasks; set it to noeviction", policy)
}

// EvictionPolicy returns the server's maxmemory-policy.
func (r *RedisClient) EvictionPolicy(ctx context.Context) (string, error) {
	vals, err := r.client.ConfigGet(ctx, "maxmemory-policy").Result()
	if err != nil {
		return "", err
	}
	// Reply is a flat [name, value] list.
	if len(vals) != 2 {
		return "", fmt.Errorf("unexpected CONFIG GET reply: %v", vals)
	}
	policy, ok := vals[1].(string)
	if !ok {
		return "", fmt.Errorf("unexpected CONFIG GET value type %T", vals[1])
	}
	return policy, nil
}
