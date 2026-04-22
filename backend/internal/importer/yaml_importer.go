// Package importer converts external YAML workflow definitions into Fluxor's
// internal WorkflowDefinition model.
//
// Supported formats
//
//   - GitHub Actions (.github/workflows/*.yml)
//     Mapped as: jobs → tasks, steps.run → generic/data_transform task,
//     steps.uses → container task (Docker action), needs → dependencies.
//
//   - Fluxor native YAML
//     A direct YAML representation of WorkflowDefinition — useful for
//     version-controlling workflows and loading them from CI pipelines.
package importer

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

// ── Public entry point ────────────────────────────────────────────────────────

// ParseYAML detects the format of the supplied YAML bytes and converts them
// to a Fluxor WorkflowDefinition. It never saves to the database; the caller
// decides whether to persist the returned definition.
func ParseYAML(data []byte, sourceName string) (*models.WorkflowDefinition, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty YAML input")
	}

	// Detect format by probing top-level keys
	var probe map[string]yaml.Node
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	if _, isGHA := probe["jobs"]; isGHA {
		return parseGitHubActions(data, sourceName)
	}
	if _, isNative := probe["tasks"]; isNative {
		return parseFluxorNative(data)
	}

	return nil, fmt.Errorf("unrecognised YAML format: expected GitHub Actions (has 'jobs:') or Fluxor native (has 'tasks:')")
}

// ── GitHub Actions parser ─────────────────────────────────────────────────────

// ghaWorkflow mirrors the subset of GitHub Actions YAML we care about.
type ghaWorkflow struct {
	Name string             `yaml:"name"`
	On   yaml.Node          `yaml:"on"`
	Jobs map[string]*ghaJob `yaml:"jobs"`
	Env  map[string]string  `yaml:"env"`
}

type ghaJob struct {
	Name   string            `yaml:"name"`
	RunsOn string            `yaml:"runs-on"`
	Needs  ghaNeeds          `yaml:"needs"`
	Env    map[string]string `yaml:"env"`
	Steps  []*ghaStep        `yaml:"steps"`
	// Container support
	Container *ghaContainer `yaml:"container"`
	// Timeout in minutes
	TimeoutMinutes int `yaml:"timeout-minutes"`
}

type ghaContainer struct {
	Image string            `yaml:"image"`
	Env   map[string]string `yaml:"env"`
}

// ghaNeeds accepts both a single string and a list of strings.
type ghaNeeds []string

func (n *ghaNeeds) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		*n = []string{value.Value}
		return nil
	}
	var list []string
	if err := value.Decode(&list); err != nil {
		return err
	}
	*n = list
	return nil
}

type ghaStep struct {
	ID   string            `yaml:"id"`
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]any    `yaml:"with"`
	Env  map[string]string `yaml:"env"`
}

func parseGitHubActions(data []byte, sourceName string) (*models.WorkflowDefinition, error) {
	var gha ghaWorkflow
	if err := yaml.Unmarshal(data, &gha); err != nil {
		return nil, fmt.Errorf("parse GitHub Actions YAML: %w", err)
	}

	name := gha.Name
	if name == "" {
		name = sourceName
	}
	if name == "" {
		name = "Imported Workflow"
	}

	now := time.Now()
	def := &models.WorkflowDefinition{
		ID:          uuid.New().String(),
		Name:        name,
		Description: fmt.Sprintf("Imported from GitHub Actions workflow: %s", sourceName),
		Version:     "1.0.0",
		MaxParallel: 10,
		Tags: map[string]string{
			"source": "github-actions",
			"file":   sourceName,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Stable job ordering: collect in insertion order via a second parse
	var rawOrder struct {
		Jobs yaml.Node `yaml:"jobs"`
	}
	yaml.Unmarshal(data, &rawOrder) //nolint:errcheck

	jobOrder := extractMapKeys(&rawOrder.Jobs)

	// Map each job to one or more Fluxor tasks.
	// A job with a single "run:" step becomes one generic task.
	// A job with multiple steps becomes a task per step, chained via dependencies.
	// A job using "uses:" with a docker:// image becomes a container task.

	// jobToTaskIDs maps job name → last task ID in that job's chain
	// so that "needs:" references resolve to the right task ID.
	jobToLastTaskID := make(map[string]string)

	for _, jobKey := range jobOrder {
		job, ok := gha.Jobs[jobKey]
		if !ok {
			continue
		}

		jobDisplayName := job.Name
		if jobDisplayName == "" {
			jobDisplayName = jobKey
		}

		// Resolve dependencies: needs → IDs of the last task in each needed job
		var jobDeps []string
		for _, need := range job.Needs {
			if lastID, ok := jobToLastTaskID[need]; ok {
				jobDeps = append(jobDeps, lastID)
			}
		}

		// Build timeout
		timeout := time.Duration(job.TimeoutMinutes) * time.Minute
		if timeout == 0 {
			timeout = 60 * time.Minute
		}

		// Merge workflow-level env with job-level env
		mergedEnv := make(map[string]string)
		for k, v := range gha.Env {
			mergedEnv[k] = v
		}
		for k, v := range job.Env {
			mergedEnv[k] = v
		}

		// If the job has no steps, create a single placeholder task
		if len(job.Steps) == 0 {
			id := stableID(jobKey)
			task := buildJobTask(id, jobDisplayName, jobKey, job, jobDeps, mergedEnv, timeout)
			def.Tasks = append(def.Tasks, task)
			jobToLastTaskID[jobKey] = id
			continue
		}

		// Multiple steps: create a chain
		prevID := ""
		for si, step := range job.Steps {
			stepDeps := jobDeps
			if prevID != "" {
				stepDeps = []string{prevID}
			}

			stepName := step.Name
			if stepName == "" {
				stepName = fmt.Sprintf("%s / step %d", jobDisplayName, si+1)
			}

			stepEnv := make(map[string]string)
			for k, v := range mergedEnv {
				stepEnv[k] = v
			}
			for k, v := range step.Env {
				stepEnv[k] = v
			}

			id := stableID(fmt.Sprintf("%s-%d", jobKey, si))
			task := buildStepTask(id, stepName, step, job, stepDeps, stepEnv, timeout)
			def.Tasks = append(def.Tasks, task)
			prevID = id
		}
		jobToLastTaskID[jobKey] = prevID
	}

	if len(def.Tasks) == 0 {
		return nil, fmt.Errorf("GitHub Actions workflow has no jobs or steps to import")
	}

	return def, nil
}

// buildJobTask creates a Fluxor task for a job that has no steps.
func buildJobTask(id, displayName, jobKey string, job *ghaJob, deps []string, env map[string]string, timeout time.Duration) models.TaskDefinition {
	return models.TaskDefinition{
		ID:           id,
		Name:         displayName,
		Type:         "generic",
		Dependencies: deps,
		Config: map[string]any{
			"command": "echo",
			"args":    []string{fmt.Sprintf("job '%s' has no steps", jobKey)},
			"env":     env,
		},
		Timeout: timeout,
		Metadata: map[string]string{
			"gha_job":    jobKey,
			"gha_runsOn": job.RunsOn,
		},
	}
}

// buildStepTask converts a single GitHub Actions step into a Fluxor task.
func buildStepTask(id, displayName string, step *ghaStep, job *ghaJob, deps []string, env map[string]string, timeout time.Duration) models.TaskDefinition {
	task := models.TaskDefinition{
		ID:           id,
		Name:         displayName,
		Dependencies: deps,
		Timeout:      timeout,
		Metadata: map[string]string{
			"gha_runsOn": job.RunsOn,
		},
	}
	if step.ID != "" {
		task.Metadata["gha_step_id"] = step.ID
	}

	switch {
	case step.Run != "":
		// Shell script step → generic task
		task.Type = "generic"
		task.Config = map[string]any{
			"command": "sh",
			"args":    []string{"-c", step.Run},
			"env":     env,
		}

	case strings.HasPrefix(step.Uses, "docker://"):
		// docker:// action → container task
		image := strings.TrimPrefix(step.Uses, "docker://")
		task.Type = "generic"
		task.Container = &models.ContainerSpec{
			Image:     image,
			MemoryMB:  512,
			CPUMillis: 1000,
			Env:       env,
		}
		// Build command from "with" inputs if present
		if entrypoint, ok := step.With["entrypoint"].(string); ok {
			task.Config = map[string]any{"command": entrypoint}
		} else {
			task.Config = map[string]any{}
		}
		if args, ok := step.With["args"].(string); ok {
			task.Config["args"] = strings.Fields(args)
		}

	case step.Uses != "":
		// Named action (e.g. actions/checkout@v4) → informational placeholder
		// We can't execute GitHub-hosted actions natively, so we note the action
		// and create a shell task the user can customise.
		task.Type = "generic"
		actionNote := fmt.Sprintf("# GitHub Action: %s\n# This action cannot run natively in Fluxor.\n# Replace with an equivalent shell command or container task.", step.Uses)
		task.Config = map[string]any{
			"command": "sh",
			"args":    []string{"-c", fmt.Sprintf("echo 'Action: %s — configure manually'", step.Uses)},
			"env":     env,
		}
		task.Metadata["gha_action"] = step.Uses
		task.Metadata["gha_note"] = actionNote

	default:
		task.Type = "generic"
		task.Config = map[string]any{
			"command": "echo",
			"args":    []string{"empty step"},
			"env":     env,
		}
	}

	return task
}

// ── Fluxor native YAML parser ─────────────────────────────────────────────────

// fluxorYAML is the YAML representation of a WorkflowDefinition.
// Field names use snake_case to match JSON tags.
type fluxorYAML struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Version     string            `yaml:"version"`
	MaxParallel int               `yaml:"max_parallel"`
	Tags        map[string]string `yaml:"tags"`
	GlobalRetry *fluxorRetry      `yaml:"global_retry"`
	Tasks       []fluxorTask      `yaml:"tasks"`
}

type fluxorRetry struct {
	MaxRetries        int     `yaml:"max_retries"`
	InitialDelaySec   float64 `yaml:"initial_delay_seconds"`
	MaxDelaySec       float64 `yaml:"max_delay_seconds"`
	BackoffMultiplier float64 `yaml:"backoff_multiplier"`
	Jitter            bool    `yaml:"jitter"`
}

type fluxorTask struct {
	ID           string            `yaml:"id"`
	Name         string            `yaml:"name"`
	Type         string            `yaml:"type"`
	Dependencies []string          `yaml:"dependencies"`
	Config       map[string]any    `yaml:"config"`
	TimeoutSec   float64           `yaml:"timeout_seconds"`
	MaxParallel  int               `yaml:"max_parallel"`
	Metadata     map[string]string `yaml:"metadata"`
	Retry        *fluxorRetry      `yaml:"retry"`
	Container    *fluxorContainer  `yaml:"container"`
	ArtifactsIn  []fluxorArtifact  `yaml:"artifacts_in"`
	ArtifactsOut []fluxorArtifact  `yaml:"artifacts_out"`
}

type fluxorContainer struct {
	Image     string            `yaml:"image"`
	MemoryMB  int64             `yaml:"memory_mb"`
	CPUMillis int64             `yaml:"cpu_millis"`
	Env       map[string]string `yaml:"env"`
	WorkDir   string            `yaml:"work_dir"`
}

type fluxorArtifact struct {
	Path        string `yaml:"path"`
	Description string `yaml:"description"`
}

func parseFluxorNative(data []byte) (*models.WorkflowDefinition, error) {
	var fy fluxorYAML
	if err := yaml.Unmarshal(data, &fy); err != nil {
		return nil, fmt.Errorf("parse Fluxor YAML: %w", err)
	}

	if fy.Name == "" {
		return nil, fmt.Errorf("fluxor YAML: 'name' is required")
	}
	if len(fy.Tasks) == 0 {
		return nil, fmt.Errorf("fluxor YAML: 'tasks' must not be empty")
	}

	now := time.Now()
	def := &models.WorkflowDefinition{
		ID:          uuid.New().String(),
		Name:        fy.Name,
		Description: fy.Description,
		Version:     fy.Version,
		MaxParallel: fy.MaxParallel,
		Tags:        fy.Tags,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if def.Version == "" {
		def.Version = "1.0.0"
	}
	if def.MaxParallel <= 0 {
		def.MaxParallel = 10
	}

	if fy.GlobalRetry != nil {
		def.GlobalRetry = convertRetry(fy.GlobalRetry)
	}

	// Build a set of declared IDs for dependency validation
	declared := make(map[string]bool)
	for _, t := range fy.Tasks {
		if t.ID == "" {
			return nil, fmt.Errorf("fluxor YAML: every task must have an 'id' field")
		}
		declared[t.ID] = true
	}

	for _, ft := range fy.Tasks {
		for _, dep := range ft.Dependencies {
			if !declared[dep] {
				return nil, fmt.Errorf("fluxor YAML: task %q depends on unknown task %q", ft.ID, dep)
			}
		}

		task := models.TaskDefinition{
			ID:           ft.ID,
			Name:         ft.Name,
			Type:         ft.Type,
			Dependencies: ft.Dependencies,
			Config:       ft.Config,
			MaxParallel:  ft.MaxParallel,
			Metadata:     ft.Metadata,
		}
		if task.Name == "" {
			task.Name = ft.ID
		}
		if task.Type == "" {
			task.Type = "generic"
		}
		if task.Config == nil {
			task.Config = map[string]any{}
		}
		if task.Dependencies == nil {
			task.Dependencies = []string{}
		}

		if ft.TimeoutSec > 0 {
			task.Timeout = time.Duration(ft.TimeoutSec * float64(time.Second))
		} else {
			task.Timeout = 5 * time.Minute
		}

		if ft.Retry != nil {
			task.RetryPolicy = convertRetry(ft.Retry)
		}

		if ft.Container != nil {
			task.Container = &models.ContainerSpec{
				Image:     ft.Container.Image,
				MemoryMB:  ft.Container.MemoryMB,
				CPUMillis: ft.Container.CPUMillis,
				Env:       ft.Container.Env,
				WorkDir:   ft.Container.WorkDir,
			}
		}

		for _, a := range ft.ArtifactsIn {
			task.ArtifactsIn = append(task.ArtifactsIn, models.ArtifactRef{Path: a.Path, Description: a.Description})
		}
		for _, a := range ft.ArtifactsOut {
			task.ArtifactsOut = append(task.ArtifactsOut, models.ArtifactRef{Path: a.Path, Description: a.Description})
		}

		def.Tasks = append(def.Tasks, task)
	}

	return def, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// stableID creates a deterministic, URL-safe task ID from a string.
// It is short enough to read in the DAG but unique within a workflow.
var nonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)

func stableID(s string) string {
	s = strings.ToLower(s)
	s = nonAlphanumRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" {
		s = "task"
	}
	return s
}

// extractMapKeys returns the YAML map keys in document order.
func extractMapKeys(n *yaml.Node) []string {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	var keys []string
	for i := 0; i < len(n.Content)-1; i += 2 {
		keys = append(keys, n.Content[i].Value)
	}
	return keys
}

func convertRetry(r *fluxorRetry) *models.RetryPolicy {
	if r == nil {
		return nil
	}
	return &models.RetryPolicy{
		MaxRetries:      r.MaxRetries,
		InitialDelay:    time.Duration(r.InitialDelaySec * float64(time.Second)),
		MaxDelay:        time.Duration(r.MaxDelaySec * float64(time.Second)),
		BackoffMultiple: r.BackoffMultiplier,
		Jitter:          r.Jitter,
	}
}
