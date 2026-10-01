package scheduler

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
)

// RetentionSweeper deletes finished runs (with their tasks and logs) and
// audit entries once they are older than their retention. A retention of 0
// keeps them forever.
type RetentionSweeper struct {
	store          *persistence.Store
	runs, auditLog time.Duration
}

func NewRetentionSweeper(store *persistence.Store, runs, auditLog time.Duration) *RetentionSweeper {
	return &RetentionSweeper{store: store, runs: runs, auditLog: auditLog}
}

// Run sweeps at start and then hourly until ctx is done.
func (r *RetentionSweeper) Run(ctx context.Context) {
	if r.runs <= 0 && r.auditLog <= 0 {
		return
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		r.Sweep(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sweep deletes what is past its retention at now.
func (r *RetentionSweeper) Sweep(ctx context.Context, now time.Time) {
	if r.runs > 0 {
		if n, err := r.store.DeleteFinishedExecutionsBefore(ctx, now.Add(-r.runs)); err != nil {
			log.Error().Err(err).Msg("retention: could not delete old runs")
		} else if n > 0 {
			log.Info().Int64("runs", n).Msg("retention: deleted old runs")
		}
	}
	if r.auditLog > 0 {
		if n, err := r.store.DeleteAuditBefore(ctx, now.Add(-r.auditLog)); err != nil {
			log.Error().Err(err).Msg("retention: could not delete old audit entries")
		} else if n > 0 {
			log.Info().Int64("entries", n).Msg("retention: deleted old audit entries")
		}
	}
}
