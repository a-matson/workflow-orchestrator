// Package importer converts external YAML workflow definitions into Fluxor's
// internal WorkflowDefinition model.
//
// Supported formats:
//
//   - GitHub Actions (.github/workflows/*.yml)
//     Each Job becomes exactly ONE Fluxor TaskDefinition of type "gha_job".
//     All Steps are embedded in Config["steps"] so they share a single runner
//     container — preserving the shared filesystem and environment that GitHub
//     Actions requires for step-to-step state passing.
//
//   - Fluxor native YAML
//     A direct YAML representation of WorkflowDefinition — useful for
//     version-controlling workflows and loading them from CI pipelines.
package importer

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

// ── Public entry point ────────────────────────────────────────────────────────

// ParseYAML detects the format and converts to a Fluxor WorkflowDefinition.
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

	return nil, fmt.Errorf("unrecognised YAML format: expected GitHub Actions ('jobs:') or Fluxor native ('tasks:')")
}

// GitHub Actions structs

// ghaWorkflow mirrors the subset of GitHub Actions YAML we care about.
type ghaWorkflow struct {
	Name string             `yaml:"name"`
	On   yaml.Node          `yaml:"on"`
	Jobs map[string]*ghaJob `yaml:"jobs"`
	Env  map[string]string  `yaml:"env"`
}

type ghaJob struct {
	Name            string            `yaml:"name"`
	RunsOn          ghaRunsOn         `yaml:"runs-on"`
	Needs           ghaNeeds          `yaml:"needs"`
	Env             map[string]string `yaml:"env"`
	Steps           []*ghaRawStep     `yaml:"steps"`
	Container       *ghaContainer     `yaml:"container"`
	TimeoutMinutes  int               `yaml:"timeout-minutes"`
	Outputs         map[string]string `yaml:"outputs"`
	Strategy        *ghaStrategy      `yaml:"strategy"`
	ContinueOnError bool              `yaml:"continue-on-error"`
}

// ghaRunsOn handles both string and map ({"group": ...}) forms
type ghaRunsOn struct{ Value string }

func (r *ghaRunsOn) UnmarshalYAML(v *yaml.Node) error {
	if v.Kind == yaml.ScalarNode {
		r.Value = v.Value
		return nil
	}
	r.Value = "ubuntu-latest" // default for complex runners
	return nil
}

type ghaNeeds []string

func (n *ghaNeeds) UnmarshalYAML(v *yaml.Node) error {
	if v.Kind == yaml.ScalarNode {
		*n = []string{v.Value}
		return nil
	}
	var list []string
	if err := v.Decode(&list); err != nil {
		return err
	}
	*n = list
	return nil
}

type ghaContainer struct {
	Image       string            `yaml:"image"`
	Env         map[string]string `yaml:"env"`
	Credentials *struct {
		Username string `yaml:"username"`
		Password string `yaml:"password"`
	} `yaml:"credentials"`
	Options string `yaml:"options"`
}

type ghaStrategy struct {
	Matrix   map[string]any `yaml:"matrix"`
	FailFast *bool          `yaml:"fail-fast"`
}

// ghaRawStep preserves every field using yaml.Node for With (mixed types)
type ghaRawStep struct {
	ID              string            `yaml:"id"`
	Name            string            `yaml:"name"`
	Uses            string            `yaml:"uses"`
	Run             string            `yaml:"run"`
	Shell           string            `yaml:"shell"`
	WorkingDir      string            `yaml:"working-directory"`
	Env             map[string]string `yaml:"env"`
	If              string            `yaml:"if"`
	ContinueOnError bool              `yaml:"continue-on-error"`
	With            yaml.Node         `yaml:"with"`
	TimeoutMinutes  int               `yaml:"timeout-minutes"`
}

// GitHub Actions parser
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
		name = "Imported GHA Workflow"
	}

	now := time.Now()
	def := &models.WorkflowDefinition{
		ID:          uuid.New().String(),
		Name:        name,
		Description: fmt.Sprintf("Imported from GitHub Actions: %s", sourceName),
		Version:     "1.0.0",
		MaxParallel: 10,
		Tags: map[string]string{
			"source": "github-actions",
			"file":   sourceName,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Preserve job insertion order
	var rawOrder struct {
		Jobs yaml.Node `yaml:"jobs"`
	}
	yaml.Unmarshal(data, &rawOrder) //nolint:errcheck
	jobOrder := extractMapKeys(&rawOrder.Jobs)

	// Map job key → the single Fluxor task ID for that job (used by needs: resolution)
	jobToTaskID := make(map[string]string)

	for _, jobKey := range jobOrder {
		job, ok := gha.Jobs[jobKey]
		if !ok {
			continue
		}

		taskID := stableID(jobKey)
		displayName := job.Name
		if displayName == "" {
			displayName = jobKey
		}

		// Resolve needs → Fluxor task IDs
		var deps []string
		for _, need := range job.Needs {
			if tid, ok := jobToTaskID[need]; ok {
				deps = append(deps, tid)
			}
		}

		// Timeout
		timeout := time.Duration(job.TimeoutMinutes) * time.Minute
		if timeout == 0 {
			timeout = 60 * time.Minute
		}

		// Merge env: workflow-level → job-level
		mergedEnv := make(map[string]string)
		for k, v := range gha.Env {
			mergedEnv[k] = v
		}
		for k, v := range job.Env {
			mergedEnv[k] = v
		}

		// Convert raw steps to GHAStep model objects
		steps := convertSteps(job.Steps, mergedEnv)

		// Detect artifact actions and wire ArtifactsIn/Out automatically
		artifactsIn, artifactsOut := detectArtifacts(steps)

		// Serialise steps list into Config so the worker can deserialise them
		stepsJSON, _ := json.Marshal(steps)
		var stepsAny []any
		json.Unmarshal(stepsJSON, &stepsAny) //nolint:errcheck

		// Runner image: honour job.container.image if set, else use our runner
		runnerImage := "fluxor/gha-runner:latest"
		if job.Container != nil && job.Container.Image != "" {
			runnerImage = job.Container.Image
		}

		// Decide the runs-on label so the UI can display it
		runsOn := job.RunsOn.Value
		if runsOn == "" {
			runsOn = "ubuntu-latest"
		}

		task := models.TaskDefinition{
			ID:           taskID,
			Name:         displayName,
			Type:         "gha_job",
			Dependencies: deps,
			Timeout:      timeout,
			// The gha_job executor reads these fields:
			Config: map[string]any{
				"steps":        stepsAny,    // []GHAStep serialised
				"runs_on":      runsOn,      // informational / routing
				"runner_image": runnerImage, // Docker image for the runner container
				"env":          mergedEnv,   // workflow+job level env
				"job_key":      jobKey,      // original job key for act -j
				"outputs":      job.Outputs, // job outputs map
			},
			// Container spec for the runner — the gha_job executor overrides this
			// at runtime with the resolved runner image + DinD socket mount
			Container: &models.ContainerSpec{
				Image:     runnerImage,
				MemoryMB:  2048,
				CPUMillis: 2000,
				Env:       mergedEnv,
			},
			ArtifactsIn:  artifactsIn,
			ArtifactsOut: artifactsOut,
			Metadata: map[string]string{
				"gha_job":     jobKey,
				"gha_runs_on": runsOn,
				"gha_source":  sourceName,
			},
		}

		def.Tasks = append(def.Tasks, task)
		jobToTaskID[jobKey] = taskID
	}

	if len(def.Tasks) == 0 {
		return nil, fmt.Errorf("GitHub Actions workflow has no jobs")
	}

	return def, nil
}

// convertSteps maps raw YAML steps to the canonical GHAStep model.
func convertSteps(raw []*ghaRawStep, _ map[string]string) []models.GHAStep {
	out := make([]models.GHAStep, 0, len(raw))
	for i, r := range raw {
		if r == nil {
			continue
		}
		step := models.GHAStep{
			ID:      r.ID,
			Name:    r.Name,
			Uses:    r.Uses,
			Run:     r.Run,
			Shell:   r.Shell,
			WorkDir: r.WorkingDir,
			If:      r.If,
		}
		if step.Name == "" {
			if step.Uses != "" {
				step.Name = step.Uses
			} else {
				step.Name = fmt.Sprintf("step-%d", i+1)
			}
		}
		if step.Shell == "" && step.Run != "" {
			step.Shell = "bash"
		}

		// Merge env
		if len(r.Env) > 0 {
			step.Env = make(map[string]string)
			for k, v := range r.Env {
				step.Env[k] = v
			}
		}

		// Decode With into map[string]any
		if r.With.Kind != 0 {
			var withMap map[string]any
			if err := r.With.Decode(&withMap); err == nil {
				step.With = withMap
			}
		}

		out = append(out, step)
	}
	return out
}

// detectArtifacts scans steps for upload-artifact / download-artifact uses
// and returns corresponding ArtifactRef slices for MinIO bridging.
func detectArtifacts(steps []models.GHAStep) (in []models.ArtifactRef, out []models.ArtifactRef) {
	for _, s := range steps {
		if !strings.Contains(s.Uses, "actions/upload-artifact") &&
			!strings.Contains(s.Uses, "actions/download-artifact") {
			continue
		}
		name := ""
		path := ""
		if s.With != nil {
			if n, ok := s.With["name"].(string); ok {
				name = n
			}
			if p, ok := s.With["path"].(string); ok {
				path = p
			}
		}
		if path == "" {
			path = name
		}
		if path == "" {
			continue
		}
		ref := models.ArtifactRef{Path: path, Description: name}
		if strings.Contains(s.Uses, "upload-artifact") {
			out = append(out, ref)
		} else {
			in = append(in, ref)
		}
	}
	return
}

// Fluxor native YAML parser

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

	declared := make(map[string]bool)
	for _, t := range fy.Tasks {
		if t.ID == "" {
			return nil, fmt.Errorf("fluxor YAML: every task must have an 'id'")
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

// Helpers

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
