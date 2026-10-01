package scheduler

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/internal/schedule"
)

// CronScheduler starts runs of workflows whose schedule fired.
type CronScheduler struct {
	store        *persistence.Store
	orchestrator *orchestrator.Orchestrator
}

func NewCronScheduler(store *persistence.Store, orch *orchestrator.Orchestrator) *CronScheduler {
	return &CronScheduler{store: store, orchestrator: orch}
}

// Run ticks every 15 s, the granularity of a schedule, until ctx is done.
func (c *CronScheduler) Run(ctx context.Context) {
	log.Info().Msg("cron scheduler started")
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		c.Tick(ctx, time.Now())
		select {
		case <-ctx.Done():
			log.Info().Msg("cron scheduler shutting down")
			return
		case <-ticker.C:
		}
	}
}

// Tick starts one run for each schedule due at now. A schedule missed while
// the backend was down fires once, not once per missed time. The claim comes
// before the start, so a failed start loses that run rather than repeating it.
func (c *CronScheduler) Tick(ctx context.Context, now time.Time) {
	due, err := c.store.ListDueSchedules(ctx, now)
	if err != nil {
		log.Error().Err(err).Msg("cron: could not list due schedules")
		return
	}
	for _, d := range due {
		var next *time.Time
		if n, err := schedule.Next(d.Schedule, now); err != nil {
			log.Error().Err(err).Str("workflow_id", d.ID).Msg("cron: stopping a schedule that no longer parses")
		} else {
			next = &n
		}
		claimed, err := c.store.ClaimSchedule(ctx, d.ID, d.NextRunAt, next)
		if err != nil || !claimed || next == nil {
			if err != nil {
				log.Error().Err(err).Str("workflow_id", d.ID).Msg("cron: could not claim schedule")
			}
			continue
		}
		def, err := c.store.GetWorkflowDefinition(ctx, d.ID)
		if err != nil {
			log.Error().Err(err).Str("workflow_id", d.ID).Msg("cron: could not load scheduled workflow")
			continue
		}
		exec, err := c.orchestrator.StartWorkflow(ctx, def, map[string]any{"scheduled_at": d.NextRunAt.UTC().Format(time.RFC3339)})
		if err != nil {
			log.Error().Err(err).Str("workflow_id", d.ID).Msg("cron: could not start scheduled run")
			continue
		}
		log.Info().Str("workflow_id", d.ID).Str("exec_id", exec.ID).Time("scheduled_at", d.NextRunAt).Msg("cron: started scheduled run")
	}
}
