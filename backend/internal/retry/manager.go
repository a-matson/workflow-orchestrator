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

// EffectivePolicy is the policy a task's failures get: its own, else the
// workflow's global one, else DefaultPolicy. Everything that counts retries
// must use it, so the task row's max_retries matches what actually happens.
func EffectivePolicy(task, global *models.RetryPolicy) *models.RetryPolicy {
	switch {
	case task != nil:
		return task
	case global != nil:
		return global
	default:
		return &DefaultPolicy
	}
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

	// An unset multiplier (0 from JSON) would make every retry after the
	// first immediate; below 1 the delay would shrink. Both mean constant.
	multiple := max(policy.BackoffMultiple, 1)
	delay := float64(policy.InitialDelay) * math.Pow(multiple, float64(retryCount))

	// An unset max_delay means no cap, not a cap of 0.
	if policy.MaxDelay > 0 && delay > float64(policy.MaxDelay) {
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
