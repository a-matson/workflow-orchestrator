package scheduler

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/workflow-platform/backend/internal/persistence"
)

// Reconciler periodically scans PostgreSQL for executions that appear
// stuck (e.g. running tasks with no recent worker heartbeat) and emits
// correction results to unblock downstream DAG progression.
//
// This guards against the split-brain scenario where:
//   - Worker dequeued a task and acquired the lock
//   - Worker died before publishing the result
//   - Task stays in "running" state indefinitely
type Reconciler struct {
	store  *persistence.Store
	redis  *persistence.RedisClient
	period time.Duration
}

func NewReconciler(store *persistence.Store, redis *persistence.RedisClient) *Reconciler {
	return &Reconciler{
		store:  store,
		redis:  redis,
		period: 60 * time.Second,
	}
}

// Run starts the reconciler loop.
func (r *Reconciler) Run(ctx context.Context) {
	ticker := time.NewTicker(r.period)
	defer ticker.Stop()

	log.Info().Dur("period", r.period).Msg("execution reconciler started")

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("reconciler stopped")
			return
		case <-ticker.C:
			if err := r.reconcile(ctx); err != nil {
				log.Error().Err(err).Msg("reconciler scan failed")
			}
		}
	}
}

func (r *Reconciler) reconcile(ctx context.Context) error {
	// Find tasks that have been "running" for longer than the max allowed duration
	// without a heartbeat from their assigned worker.
	//
	// Strategy: check if the worker's heartbeat key exists in Redis.
	// If the key has expired, the worker is dead → reset task to "queued".
	tasks, err := r.store.ListStaleRunningTasks(ctx, 5*time.Minute)
	if err != nil {
		return err
	}

	for _, task := range tasks {
		if task.WorkerID == "" {
			continue
		}

		// Check worker liveness
		alive, err := r.redis.IsWorkerAlive(ctx, task.WorkerID)
		if err != nil {
			log.Warn().Err(err).Str("worker_id", task.WorkerID).Msg("could not check worker liveness")
			continue
		}

		if !alive {
			log.Warn().
				Str("task_exec_id", task.ID).
				Str("worker_id", task.WorkerID).
				Str("task_name", task.TaskName).
				Dur("stale_for", time.Since(*task.StartedAt)).
				Msg("stale task detected — resetting to queued for redelivery")

			// Reset to queued — the retry poller or dispatcher will re-enqueue
			now := time.Now()
			task.Status = "queued"
			task.WorkerID = ""
			task.StartedAt = nil
			task.UpdatedAt = now
			_ = r.store.UpdateTaskExecution(ctx, task)
		}
	}

	if len(tasks) > 0 {
		log.Info().Int("stale_tasks_found", len(tasks)).Msg("reconciler scan complete")
	}

	return nil
}
