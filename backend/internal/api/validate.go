package api

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/dag"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/schedule"
	"github.com/a-matson/workflow-orchestrator/backend/internal/templating"
)

const maxTaskIDLen = 64

// Task IDs end up in workspace and artifact paths, so keep them to a path-safe charset.
var taskIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// validateDefinition rejects definitions that would only fail (or escape the
// workspace) at trigger time. Errors name the offending task.
func validateDefinition(def *models.WorkflowDefinition) error {
	if len(def.Tasks) == 0 {
		return fmt.Errorf("workflow has no tasks")
	}
	if def.Schedule != "" {
		if _, err := schedule.Next(def.Schedule, time.Now()); err != nil {
			return fmt.Errorf("%w (use five fields, e.g. \"0 2 * * *\", or a descriptor such as @daily; times are UTC)", err)
		}
	}
	for i := range def.Tasks {
		t := &def.Tasks[i]
		if len(t.ID) > maxTaskIDLen {
			return fmt.Errorf("task %q: id too long (max %d characters)", t.ID, maxTaskIDLen)
		}
		if !taskIDPattern.MatchString(t.ID) {
			return fmt.Errorf("task %q: id must be non-empty and match [A-Za-z0-9_-]", t.ID)
		}
		switch t.TriggerRule {
		case "", models.TriggerRuleAllSuccess, models.TriggerRuleAllDone, models.TriggerRuleOneFailed:
		default:
			return fmt.Errorf("task %q: trigger_rule %q must be all_success, all_done or one_failed", t.ID, t.TriggerRule)
		}
		if err := templating.Check(t.Config); err != nil {
			return fmt.Errorf("task %q: config: %w", t.ID, err)
		}
		if err := templating.CheckOutsideWorker(t.When); err != nil {
			return fmt.Errorf("task %q: when: %w", t.ID, err)
		}
		for _, refs := range [][]models.ArtifactRef{t.ArtifactsIn, t.ArtifactsOut} {
			for _, a := range refs {
				// Canonical, because artifacts are matched by path string: a
				// consumer's "./out.txt" would never find a producer's "out.txt".
				if !filepath.IsLocal(a.Path) || path.Clean(a.Path) != a.Path {
					return fmt.Errorf("task %q: artifact path %q must be a clean relative path inside the workspace, like results/data.csv", t.ID, a.Path)
				}
			}
		}
	}
	if _, err := dag.Parse(def); err != nil {
		return err
	}
	return nil
}

// planSchedule sets def.NextRunAt from its schedule, counting from now, or
// clears it. Saving a workflow restarts its schedule. Call after
// validateDefinition, which has parsed the expression.
func planSchedule(def *models.WorkflowDefinition, now time.Time) {
	def.NextRunAt = nil
	if def.Schedule == "" {
		return
	}
	if next, err := schedule.Next(def.Schedule, now); err == nil {
		def.NextRunAt = &next
	}
}
