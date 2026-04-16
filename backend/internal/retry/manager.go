package retry

import (
	"context"
	"math"
	"math/rand"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/rs/zerolog/log"
)

// DefaultPolicy is used when no task-level policy is specified
var DefaultPolicy = models.RetryPolicy{
	MaxRetries:      3,
	InitialDelay:    2 * time.Second,
	MaxDelay:        5 * time.Minute,
	BackoffMultiple: 2.0,
	Jitter:          true,
}

// Manager handles retry scheduling with exponential backoff
type Manager struct {
	deadLetterQueue chan *models.TaskExecution
}

func NewManager() *Manager {
	return &Manager{
		deadLetterQueue: make(chan *models.TaskExecution, 1000),
	}
}

// ShouldRetry returns true if the task has retries remaining
func (m *Manager) ShouldRetry(task *models.TaskExecution, policy *models.RetryPolicy) bool {
	if policy == nil {
		policy = &DefaultPolicy
	}
	return task.RetryCount < policy.MaxRetries
}

// NextRetryDelay calculates the delay for the next retry attempt using exponential backoff
func (m *Manager) NextRetryDelay(retryCount int, policy *models.RetryPolicy) time.Duration {
	if policy == nil {
		policy = &DefaultPolicy
	}

	// delay = initialDelay * multiplier^retryCount
	delay := float64(policy.InitialDelay) * math.Pow(policy.BackoffMultiple, float64(retryCount))

	// Cap at max delay
	if delay > float64(policy.MaxDelay) {
		delay = float64(policy.MaxDelay)
	}

	// Add jitter: ±25% of the computed delay to prevent thundering herd
	if policy.Jitter {
		jitter := delay * 0.25 * (2*rand.Float64() - 1)
		delay += jitter
	}

	if delay < 0 {
		delay = float64(policy.InitialDelay)
	}

	return time.Duration(delay)
}

// ScheduleRetry sets the task state for a retry attempt
func (m *Manager) ScheduleRetry(ctx context.Context, task *models.TaskExecution, policy *models.RetryPolicy, errMsg string) {
	task.RetryCount++
	task.Error = errMsg
	task.Status = models.TaskStatusRetrying

	delay := m.NextRetryDelay(task.RetryCount-1, policy)
	nextRetry := time.Now().Add(delay)
	task.NextRetryAt = &nextRetry
	task.UpdatedAt = time.Now()

	log.Info().
		Str("task_exec_id", task.ID).
		Str("task_name", task.TaskName).
		Int("retry_count", task.RetryCount).
		Int("max_retries", task.MaxRetries).
		Dur("delay", delay).
		Time("next_retry_at", nextRetry).
		Msg("task scheduled for retry")
}

// SendToDeadLetter moves a task to the dead letter queue after exhausting retries
func (m *Manager) SendToDeadLetter(task *models.TaskExecution) {
	task.Status = models.TaskStatusDeadLetter
	task.UpdatedAt = time.Now()

	log.Warn().
		Str("task_exec_id", task.ID).
		Str("task_name", task.TaskName).
		Int("retry_count", task.RetryCount).
		Str("error", task.Error).
		Msg("task moved to dead letter queue")

	select {
	case m.deadLetterQueue <- task:
	default:
		log.Error().
			Str("task_exec_id", task.ID).
			Msg("dead letter queue full, dropping task")
	}
}

// DeadLetterQueue returns the channel for dead letter tasks (for monitoring)
func (m *Manager) DeadLetterQueue() <-chan *models.TaskExecution {
	return m.deadLetterQueue
}

// ReplayFromDeadLetter resets a dead letter task for re-execution
func (m *Manager) ReplayFromDeadLetter(task *models.TaskExecution) {
	task.Status = models.TaskStatusPending
	task.RetryCount = 0
	task.Error = ""
	task.NextRetryAt = nil
	task.StartedAt = nil
	task.CompletedAt = nil
	task.UpdatedAt = time.Now()

	log.Info().
		Str("task_exec_id", task.ID).
		Str("task_name", task.TaskName).
		Msg("task replayed from dead letter queue")
}
