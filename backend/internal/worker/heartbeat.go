package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/rs/zerolog/log"
)

const (
	HeartbeatInterval = 15 * time.Second
	HeartbeatTTL      = 45 * time.Second
	TaskTimeoutCheck  = 30 * time.Second
)

// Heartbeat publishes worker liveness to Redis so the orchestrator
// can detect dead workers and reclaim their in-flight tasks.
type Heartbeat struct {
	workerID string
	redis    *persistence.RedisClient
}

func NewHeartbeat(workerID string, redis *persistence.RedisClient) *Heartbeat {
	return &Heartbeat{workerID: workerID, redis: redis}
}

// Run starts the heartbeat loop. It sets a Redis key with TTL so the
// orchestrator can list active workers via SCAN.
func (h *Heartbeat) Run(ctx context.Context) {
	ticker := time.NewTicker(HeartbeatInterval)
	defer ticker.Stop()

	key := fmt.Sprintf("worker:heartbeat:%s", h.workerID)

	// Publish immediately on start
	_ = h.redis.SetIdempotency(ctx, key, HeartbeatTTL)
	log.Info().Str("worker_id", h.workerID).Msg("worker heartbeat started")

	for {
		select {
		case <-ctx.Done():
			log.Info().Str("worker_id", h.workerID).Msg("worker heartbeat stopped")
			return
		case <-ticker.C:
			if err := h.redis.SetIdempotency(ctx, key, HeartbeatTTL); err != nil {
				log.Error().Err(err).Str("worker_id", h.workerID).Msg("heartbeat write failed")
			}
		}
	}
}

// TaskWatchdog monitors tasks for timeout violations and publishes
// failure results for tasks that have exceeded their configured timeout.
type TaskWatchdog struct {
	store *persistence.RedisClient
}

func NewTaskWatchdog(redis *persistence.RedisClient) *TaskWatchdog {
	return &TaskWatchdog{store: redis}
}

// Run scans for timed-out running tasks every 30 seconds.
// In production, this would query PostgreSQL for tasks in "running" state
// where started_at + timeout < NOW() and no recent heartbeat from their worker.
func (w *TaskWatchdog) Run(ctx context.Context) {
	ticker := time.NewTicker(TaskTimeoutCheck)
	defer ticker.Stop()

	log.Info().Msg("task watchdog started")

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("task watchdog stopped")
			return
		case <-ticker.C:
			// In production: query PostgreSQL for timed-out tasks
			// then publish failure results to the result queue
			log.Debug().Msg("watchdog scan complete")
		}
	}
}
