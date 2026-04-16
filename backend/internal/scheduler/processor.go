package scheduler

import (
	"context"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/rs/zerolog/log"
)

// ResultProcessor continuously reads from the Redis result queue
// and forwards completions to the orchestrator
type ResultProcessor struct {
	redis        *persistence.RedisClient
	orchestrator *orchestrator.Orchestrator
}

func NewResultProcessor(redis *persistence.RedisClient, orch *orchestrator.Orchestrator) *ResultProcessor {
	return &ResultProcessor{redis: redis, orchestrator: orch}
}

func (p *ResultProcessor) Run(ctx context.Context) {
	log.Info().Msg("result processor started")

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("result processor shutting down")
			return
		default:
			result, err := p.redis.DequeueResult(ctx, 2*time.Second)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Error().Err(err).Msg("error dequeuing result")
				time.Sleep(500 * time.Millisecond)
				continue
			}

			if result == nil {
				continue // Timeout, loop again
			}

			log.Debug().
				Str("task_exec_id", result.TaskExecID).
				Bool("success", result.Success).
				Msg("processing task result")

			if err := p.orchestrator.ProcessResult(ctx, result); err != nil {
				log.Error().Err(err).
					Str("task_exec_id", result.TaskExecID).
					Msg("failed to process task result")
			}
		}
	}
}

// RetryPoller scans the Redis retry ZSet and re-enqueues tasks whose time has come
type RetryPoller struct {
	redis        *persistence.RedisClient
	orchestrator *orchestrator.Orchestrator
}

func NewRetryPoller(redis *persistence.RedisClient, orch *orchestrator.Orchestrator) *RetryPoller {
	return &RetryPoller{redis: redis, orchestrator: orch}
}

func (p *RetryPoller) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	log.Info().Msg("retry poller started")

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("retry poller shutting down")
			return
		case <-ticker.C:
			msgs, err := p.redis.PopDueRetries(ctx)
			if err != nil {
				log.Error().Err(err).Msg("error polling retries")
				continue
			}

			for _, msg := range msgs {
				log.Info().
					Str("task_exec_id", msg.TaskExecID).
					Int("retry_count", msg.RetryCount).
					Msg("re-enqueuing task for retry")

				if err := p.redis.EnqueueTask(ctx, msg); err != nil {
					log.Error().Err(err).Str("task_exec_id", msg.TaskExecID).Msg("failed to re-enqueue retry")
				}
			}
		}
	}
}
