package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/workflow-platform/backend/internal/models"
)

const (
	TaskQueueKey       = "workflow:tasks:queue"
	ResultQueueKey     = "workflow:results:queue"
	RetryZSetKey       = "workflow:tasks:retry"
	DeadLetterKey      = "workflow:tasks:dead_letter"
	WorkflowStateKey   = "workflow:state:%s"
	TaskLockKey        = "workflow:task:lock:%s"
	IdempotencyKey     = "workflow:idempotency:%s"
	MetricsKey         = "workflow:metrics"
)

// RedisClient wraps go-redis for workflow broker operations
type RedisClient struct {
	client *redis.Client
}

func NewRedisClient(addr, password string, db int) *RedisClient {
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		PoolSize:     20,
		MinIdleConns: 5,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})
	return &RedisClient{client: client}
}

func (r *RedisClient) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

func (r *RedisClient) Close() error {
	return r.client.Close()
}

// ==================== Task Queue ====================

// EnqueueTask pushes a task message to the Redis list (durable queue)
func (r *RedisClient) EnqueueTask(ctx context.Context, msg *models.TaskMessage) error {
	// Check idempotency: don't re-enqueue if already processed
	idem, err := r.CheckIdempotency(ctx, msg.IdempotencyKey)
	if err != nil {
		return fmt.Errorf("idempotency check: %w", err)
	}
	if idem {
		return nil // Already processed or in-flight
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling task message: %w", err)
	}

	return r.client.LPush(ctx, TaskQueueKey, data).Err()
}

// DequeueTask blocks and pops a task message from the queue
func (r *RedisClient) DequeueTask(ctx context.Context, timeout time.Duration) (*models.TaskMessage, error) {
	result, err := r.client.BRPop(ctx, timeout, TaskQueueKey).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil // Timeout, no message
		}
		return nil, fmt.Errorf("dequeuing task: %w", err)
	}

	var msg models.TaskMessage
	if err := json.Unmarshal([]byte(result[1]), &msg); err != nil {
		return nil, fmt.Errorf("unmarshaling task message: %w", err)
	}

	return &msg, nil
}

// ==================== Results ====================

// PublishResult pushes a task result back to the orchestrator
func (r *RedisClient) PublishResult(ctx context.Context, result *models.TaskResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshaling result: %w", err)
	}

	return r.client.LPush(ctx, ResultQueueKey, data).Err()
}

// DequeueResult blocks and pops a task result
func (r *RedisClient) DequeueResult(ctx context.Context, timeout time.Duration) (*models.TaskResult, error) {
	result, err := r.client.BRPop(ctx, timeout, ResultQueueKey).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, fmt.Errorf("dequeuing result: %w", err)
	}

	var taskResult models.TaskResult
	if err := json.Unmarshal([]byte(result[1]), &taskResult); err != nil {
		return nil, fmt.Errorf("unmarshaling task result: %w", err)
	}

	return &taskResult, nil
}

// ==================== Retry ZSet ====================

// ScheduleRetry adds a task to the retry sorted set with score = Unix timestamp of next retry
func (r *RedisClient) ScheduleRetry(ctx context.Context, msg *models.TaskMessage, retryAt time.Time) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	return r.client.ZAdd(ctx, RetryZSetKey, &redis.Z{
		Score:  float64(retryAt.Unix()),
		Member: string(data),
	}).Err()
}

// PopDueRetries retrieves all retry tasks whose time has come
func (r *RedisClient) PopDueRetries(ctx context.Context) ([]*models.TaskMessage, error) {
	now := float64(time.Now().Unix())

	// Atomic: ZRANGEBYSCORE + ZREM
	members, err := r.client.ZRangeByScore(ctx, RetryZSetKey, &redis.ZRangeBy{
		Min: "0",
		Max: fmt.Sprintf("%f", now),
	}).Result()
	if err != nil {
		return nil, err
	}

	var msgs []*models.TaskMessage
	for _, member := range members {
		// Remove from retry set atomically
		r.client.ZRem(ctx, RetryZSetKey, member)

		var msg models.TaskMessage
		if err := json.Unmarshal([]byte(member), &msg); err != nil {
			continue
		}
		msgs = append(msgs, &msg)
	}

	return msgs, nil
}

// ==================== Dead Letter ====================

func (r *RedisClient) SendToDeadLetter(ctx context.Context, msg *models.TaskMessage, reason string) error {
	payload := map[string]any{
		"message":   msg,
		"reason":    reason,
		"timestamp": time.Now(),
	}
	data, _ := json.Marshal(payload)
	return r.client.LPush(ctx, DeadLetterKey, data).Err()
}

// ==================== Distributed Locking ====================

// AcquireTaskLock uses SET NX EX for distributed task exclusion (idempotency)
func (r *RedisClient) AcquireTaskLock(ctx context.Context, taskExecID string, ttl time.Duration) (bool, error) {
	key := fmt.Sprintf(TaskLockKey, taskExecID)
	result, err := r.client.SetNX(ctx, key, "locked", ttl).Result()
	return result, err
}

func (r *RedisClient) ReleaseTaskLock(ctx context.Context, taskExecID string) error {
	key := fmt.Sprintf(TaskLockKey, taskExecID)
	return r.client.Del(ctx, key).Err()
}

// ==================== Idempotency ====================

func (r *RedisClient) SetIdempotency(ctx context.Context, key string, ttl time.Duration) error {
	idemKey := fmt.Sprintf(IdempotencyKey, key)
	return r.client.Set(ctx, idemKey, "1", ttl).Err()
}

func (r *RedisClient) CheckIdempotency(ctx context.Context, key string) (bool, error) {
	idemKey := fmt.Sprintf(IdempotencyKey, key)
	result, err := r.client.Exists(ctx, idemKey).Result()
	if err != nil {
		return false, err
	}
	return result > 0, nil
}

// ==================== Metrics ====================

func (r *RedisClient) IncrMetric(ctx context.Context, field string) error {
	return r.client.HIncrBy(ctx, MetricsKey, field, 1).Err()
}

func (r *RedisClient) GetMetrics(ctx context.Context) (map[string]string, error) {
	return r.client.HGetAll(ctx, MetricsKey).Result()
}

// QueueDepth returns the number of tasks currently in the task queue
func (r *RedisClient) QueueDepth(ctx context.Context) (int64, error) {
	return r.client.LLen(ctx, TaskQueueKey).Result()
}

// RetryQueueDepth returns the number of tasks awaiting retry
func (r *RedisClient) RetryQueueDepth(ctx context.Context) (int64, error) {
	return r.client.ZCard(ctx, RetryZSetKey).Result()
}
