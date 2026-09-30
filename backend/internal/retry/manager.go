package retry

import (
	"math"
	"math/rand"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
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
type Manager struct{}

func NewManager() *Manager {
	return &Manager{}
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
