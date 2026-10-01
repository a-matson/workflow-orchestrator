package api

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"

	"github.com/a-matson/workflow-orchestrator/backend/internal/dag"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
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
