package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/workflow-platform/backend/internal/models"
	"github.com/workflow-platform/backend/internal/persistence"
)

// Worker is a task executor that pulls work from Redis and publishes results
type Worker struct {
	id           string
	redis        *persistence.RedisClient
	concurrency  int
	semaphore    chan struct{}
}

// Pool manages multiple workers
type Pool struct {
	workers []*Worker
	redis   *persistence.RedisClient
}

func NewPool(redis *persistence.RedisClient, workerCount, concurrencyPerWorker int) *Pool {
	workers := make([]*Worker, workerCount)
	for i := 0; i < workerCount; i++ {
		workers[i] = &Worker{
			id:          fmt.Sprintf("worker-%s", uuid.New().String()[:8]),
			redis:       redis,
			concurrency: concurrencyPerWorker,
			semaphore:   make(chan struct{}, concurrencyPerWorker),
		}
	}
	return &Pool{workers: workers, redis: redis}
}

func (p *Pool) Start(ctx context.Context) {
	var wg sync.WaitGroup
	for _, w := range p.workers {
		wg.Add(1)
		go func(worker *Worker) {
			defer wg.Done()
			worker.run(ctx)
		}(w)
	}
	log.Info().Int("worker_count", len(p.workers)).Msg("worker pool started")
	wg.Wait()
}

func (w *Worker) run(ctx context.Context) {
	log.Info().Str("worker_id", w.id).Msg("worker started")

	for {
		select {
		case <-ctx.Done():
			log.Info().Str("worker_id", w.id).Msg("worker shutting down")
			return
		default:
			msg, err := w.redis.DequeueTask(ctx, 2*time.Second)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Error().Err(err).Str("worker_id", w.id).Msg("error dequeuing task")
				time.Sleep(time.Second)
				continue
			}

			if msg == nil {
				continue
			}

			// Acquire concurrency slot
			select {
			case w.semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}

			go func(taskMsg *models.TaskMessage) {
				defer func() { <-w.semaphore }()
				w.executeTask(ctx, taskMsg)
			}(msg)
		}
	}
}

func (w *Worker) executeTask(ctx context.Context, msg *models.TaskMessage) {
	startedAt := time.Now()

	log.Info().
		Str("worker_id", w.id).
		Str("task_exec_id", msg.TaskExecID).
		Str("task_type", msg.TaskType).
		Int("retry_count", msg.RetryCount).
		Msg("executing task")

	var logs []models.LogEntry

	addLog := func(level, message string, fields map[string]any) {
		logs = append(logs, models.LogEntry{
			Timestamp: time.Now(),
			Level:     level,
			Message:   message,
			Fields:    fields,
		})
	}

	// Create execution context with timeout
	taskCtx := ctx
	if msg.Timeout > 0 {
		var cancel context.CancelFunc
		taskCtx, cancel = context.WithTimeout(ctx, msg.Timeout)
		defer cancel()
	}

	// Acquire distributed lock for idempotency
	locked, err := w.redis.AcquireTaskLock(taskCtx, msg.TaskExecID, 10*time.Minute)
	if err != nil || !locked {
		log.Warn().Str("task_exec_id", msg.TaskExecID).Msg("task already locked by another worker, skipping")
		return
	}
	defer w.redis.ReleaseTaskLock(ctx, msg.TaskExecID)

	addLog("info", "Task execution started", map[string]any{
		"worker_id": w.id,
		"task_type": msg.TaskType,
		"retry":     msg.RetryCount,
	})

	// Simulate task execution based on task type
	output, execErr := w.simulateTaskExecution(taskCtx, msg, addLog)

	completedAt := time.Now()

	result := &models.TaskResult{
		TaskExecID:     msg.TaskExecID,
		WorkflowExecID: msg.WorkflowExecID,
		WorkerID:       w.id,
		Logs:           logs,
		StartedAt:      startedAt,
		CompletedAt:    completedAt,
	}

	if execErr != nil {
		result.Success = false
		result.Error = execErr.Error()
		addLog("error", "Task execution failed", map[string]any{"error": execErr.Error()})
	} else {
		result.Success = true
		outputJSON, _ := json.Marshal(output)
		result.Output = outputJSON
		addLog("info", "Task completed successfully", map[string]any{"duration_ms": completedAt.Sub(startedAt).Milliseconds()})
	}
	result.Logs = logs

	if err := w.redis.PublishResult(ctx, result); err != nil {
		log.Error().Err(err).Str("task_exec_id", msg.TaskExecID).Msg("failed to publish result")
	}
}

// simulateTaskExecution mimics various task types with realistic behavior
func (w *Worker) simulateTaskExecution(ctx context.Context, msg *models.TaskMessage, addLog func(level, msg string, fields map[string]any)) (map[string]any, error) {
	switch msg.TaskType {
	case "http_request":
		return w.simulateHTTPTask(ctx, msg, addLog)
	case "data_transform":
		return w.simulateDataTask(ctx, msg, addLog)
	case "database_query":
		return w.simulateDBTask(ctx, msg, addLog)
	case "ml_inference":
		return w.simulateMLTask(ctx, msg, addLog)
	case "notification":
		return w.simulateNotificationTask(ctx, msg, addLog)
	default:
		return w.simulateGenericTask(ctx, msg, addLog)
	}
}

func (w *Worker) simulateHTTPTask(ctx context.Context, msg *models.TaskMessage, addLog func(string, string, map[string]any)) (map[string]any, error) {
	url, _ := msg.Config["url"].(string)
	addLog("info", fmt.Sprintf("Making HTTP request to %s", url), nil)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Duration(200+rand.Intn(800)) * time.Millisecond):
	}

	// Simulate occasional failures
	if rand.Float32() < 0.05 {
		return nil, fmt.Errorf("HTTP 503: service temporarily unavailable")
	}

	addLog("info", "HTTP request completed", map[string]any{"status": 200, "latency_ms": rand.Intn(500) + 100})
	return map[string]any{"status": 200, "response_size": rand.Intn(10000)}, nil
}

func (w *Worker) simulateDataTask(ctx context.Context, msg *models.TaskMessage, addLog func(string, string, map[string]any)) (map[string]any, error) {
	rows := 1000 + rand.Intn(50000)
	addLog("info", fmt.Sprintf("Processing %d records", rows), nil)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Duration(500+rand.Intn(2000)) * time.Millisecond):
	}

	addLog("info", "Data transformation complete", map[string]any{"records_processed": rows, "output_records": rows * 95 / 100})
	return map[string]any{"records_processed": rows, "rows_written": rows * 95 / 100}, nil
}

func (w *Worker) simulateDBTask(ctx context.Context, msg *models.TaskMessage, addLog func(string, string, map[string]any)) (map[string]any, error) {
	addLog("info", "Executing database query", nil)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Duration(100+rand.Intn(500)) * time.Millisecond):
	}

	return map[string]any{"rows_affected": rand.Intn(10000), "execution_time_ms": rand.Intn(400) + 50}, nil
}

func (w *Worker) simulateMLTask(ctx context.Context, msg *models.TaskMessage, addLog func(string, string, map[string]any)) (map[string]any, error) {
	addLog("info", "Loading model checkpoint", nil)
	time.Sleep(200 * time.Millisecond)
	addLog("info", "Running inference batch", map[string]any{"batch_size": 64})

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Duration(1000+rand.Intn(3000)) * time.Millisecond):
	}

	return map[string]any{"predictions": rand.Intn(1000), "accuracy": 0.92 + rand.Float64()*0.06}, nil
}

func (w *Worker) simulateNotificationTask(ctx context.Context, msg *models.TaskMessage, addLog func(string, string, map[string]any)) (map[string]any, error) {
	addLog("info", "Sending notification", nil)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Duration(50+rand.Intn(200)) * time.Millisecond):
	}

	return map[string]any{"delivered": true, "recipients": rand.Intn(100) + 1}, nil
}

func (w *Worker) simulateGenericTask(ctx context.Context, msg *models.TaskMessage, addLog func(string, string, map[string]any)) (map[string]any, error) {
	duration := time.Duration(500+rand.Intn(2500)) * time.Millisecond
	addLog("info", fmt.Sprintf("Executing task (estimated: %s)", duration.Round(time.Millisecond)), nil)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(duration):
	}

	// Random failure rate for testing retry logic
	if rand.Float32() < 0.03 {
		return nil, fmt.Errorf("transient error: resource temporarily unavailable")
	}

	return map[string]any{"status": "success", "duration_ms": duration.Milliseconds()}, nil
}
