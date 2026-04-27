package worker

// gha_executor.go — GitHub Actions job executor for Fluxor
//
// Architecture:
//   The worker spawns a single "runner container" (fluxor/gha-runner:latest or
//   a custom image) per GHA job. All steps run sequentially inside that one
//   container, sharing /workspace and environment — exactly mirroring how a
//   real GitHub Actions runner operates.
//
//   Step execution, ${{ }} evaluation, and GITHUB_ENV/GITHUB_OUTPUT/GITHUB_PATH
//   management are performed by a lightweight "step-runner" binary that is
//   baked into the runner image. Fluxor injects the step list as a JSON file
//   at /fluxor/steps.json before starting the container.
//
// DinD (Docker-in-Docker):
//   The runner container receives /var/run/docker.sock so steps that use
//   docker/build-push-action or run containers themselves work correctly.
//
// Artifact bridging:
//   actions/upload-artifact → detected during import, mapped to ArtifactsOut.
//   actions/download-artifact → mapped to ArtifactsIn, pre-flight downloaded.
//   The runner image intercepts these action names and redirects to /workspace.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

const (
	// DefaultRunnerImage is used when no runner image is specified.
	// Build this from runner/Dockerfile.gha-runner in the repository.
	DefaultRunnerImage = "fluxor/gha-runner:latest"

	// GHATaskNetwork is a separate network for GHA runner containers.
	// Unlike fluxor-tasks (internal/no-internet), GHA jobs often need internet
	// access to install packages, clone repos, etc.
	// Set to "bridge" for full internet access.
	GHATaskNetwork = "bridge"

	// StepsFilePath is the path inside the runner container where Fluxor
	// writes the serialised step list.
	StepsFilePath = "/fluxor/steps.json"

	// GHAContextFilePath is where Fluxor writes the GitHub context JSON.
	GHAContextFilePath = "/fluxor/context.json"
)

// RunGHAJob executes a gha_job task in a single persistent runner container.
// All steps share one container, one workspace, and one environment — exactly
// matching GitHub Actions job semantics.
func (ce *ContainerExecutor) RunGHAJob(
	ctx context.Context,
	msg *models.TaskMessage,
	addLog logFn,
) (stdout string, artifacts []models.ResolvedArtifact, err error) {
	cfg := msg.Config

	// 1. Resolve runner image
	runnerImage := DefaultRunnerImage
	if ri, ok := cfg["runner_image"].(string); ok && ri != "" {
		runnerImage = ri
	}

	// 2. Extract steps
	steps, err := extractSteps(cfg)
	if err != nil {
		return "", nil, fmt.Errorf("gha_job: %w", err)
	}
	if len(steps) == 0 {
		addLog("warn", "gha_job has no steps", nil)
		return "no steps", nil, nil
	}

	// 3. Build GitHub context
	ghaCtx := buildGHAContext(msg)

	// 4. Pull runner image
	addLog("info", fmt.Sprintf("Pulling runner image: %s", runnerImage), nil)
	if err := ce.pullImage(ctx, runnerImage); err != nil {
		return "", nil, fmt.Errorf("gha_job: pull runner image: %w", err)
	}

	// 5. Prepare workspace
	workspaceDir, err := os.MkdirTemp("", "fluxor-gha-ws-*")
	if err != nil {
		return "", nil, fmt.Errorf("gha_job: create workspace: %w", err)
	}
	if err := os.Chmod(workspaceDir, 0o700); err != nil {
		_ = os.RemoveAll(workspaceDir)
		return "", nil, fmt.Errorf("gha_job: chmod workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(workspaceDir) }()

	// Create /fluxor injection dir inside workspace
	fluxorDir := filepath.Join(workspaceDir, ".fluxor")
	if err := os.MkdirAll(fluxorDir, 0o700); err != nil {
		return "", nil, fmt.Errorf("gha_job: create fluxor dir: %w", err)
	}

	// Write steps.json for the runner binary to consume
	stepsJSON, err := json.MarshalIndent(steps, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("gha_job: marshal steps: %w", err)
	}
	if err := os.WriteFile(filepath.Join(fluxorDir, "steps.json"), stepsJSON, 0o600); err != nil {
		return "", nil, fmt.Errorf("gha_job: write steps.json: %w", err)
	}

	// Write context.json
	ctxJSON, err := json.MarshalIndent(ghaCtx, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("gha_job: marshal context: %w", err)
	}
	if err := os.WriteFile(filepath.Join(fluxorDir, "context.json"), ctxJSON, 0o600); err != nil {
		return "", nil, fmt.Errorf("gha_job: write context.json: %w", err)
	}

	// 6. Download artifact inputs
	if err := ce.downloadArtifacts(ctx, msg.ArtifactsIn, workspaceDir, addLog); err != nil {
		return "", nil, fmt.Errorf("gha_job: download artifacts: %w", err)
	}

	// 7. Build environment for the runner container
	env := buildGHAEnv(msg, ghaCtx, workspaceDir)

	addLog("info", fmt.Sprintf("Starting GHA runner: %d steps", len(steps)), map[string]any{
		"image":     runnerImage,
		"steps":     stepNames(steps),
		"workspace": workspaceDir,
	})

	// 8. Create runner container
	containerCfg := &container.Config{
		Image:      runnerImage,
		Env:        env,
		WorkingDir: "/workspace",
		// The runner entrypoint reads /fluxor/steps.json and executes them.
		// No Cmd override needed — the image entrypoint handles everything.
		Labels: map[string]string{
			"fluxor.task_exec_id":     msg.TaskExecID,
			"fluxor.workflow_exec_id": msg.WorkflowExecID,
			"fluxor.task_type":        "gha_job",
		},
	}

	// GHA jobs need resource limits but NOT the hardened security profile
	// used for regular tasks: they need to install packages, run git, etc.
	hostCfg := &container.HostConfig{
		Resources: container.Resources{
			Memory:    2 * 1024 * 1024 * 1024, // 2 GB default
			NanoCPUs:  2 * 1_000_000_000,      // 2 vCPU default
			PidsLimit: int64Ptr(256),          // GHA jobs may fork many processes
		},
		// /workspace is writable (bind-mount from host temp dir)
		// /tmp is writable
		// Root FS is NOT read-only — GHA runners need to install tools
		ReadonlyRootfs: false,
		// Docker-in-Docker: pass through the host Docker socket so steps
		// can build and run containers (e.g. docker/build-push-action)
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: workspaceDir,
				Target: "/workspace",
			},
			{
				Type:   mount.TypeBind,
				Source: filepath.Join(workspaceDir, ".fluxor"),
				Target: "/fluxor",
			},
			{
				// Docker socket for DinD — GHA jobs that build Docker images need this
				Type:   mount.TypeBind,
				Source: "/var/run/docker.sock",
				Target: "/var/run/docker.sock",
			},
		},
		RestartPolicy: container.RestartPolicy{Name: "no"},
	}

	// GHA jobs use bridge network (internet access needed for apt, npm, git clone, etc.)
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			GHATaskNetwork: {},
		},
	}

	createResp, err := ce.docker.ContainerCreate(ctx, containerCfg, hostCfg, netCfg, nil, "")
	if err != nil {
		return "", nil, fmt.Errorf("gha_job: create container: %w", err)
	}
	containerID := createResp.ID
	defer func() {
		detachedCtx := context.WithoutCancel(ctx)
		rmCtx, cancel := context.WithTimeout(detachedCtx, 30*time.Second)
		defer cancel()
		_ = ce.docker.ContainerRemove(rmCtx, containerID, container.RemoveOptions{Force: true})
	}()

	// 9. Start container
	if err := ce.docker.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return "", nil, fmt.Errorf("gha_job: start container: %w", err)
	}

	// 10. Stream logs in real time via attach
	var logBuf bytes.Buffer
	go ce.streamContainerLogs(ctx, containerID, &logBuf, addLog)

	// 11. Wait for completion
	statusCh, errCh := ce.docker.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	var exitCode int64
	select {
	case status := <-statusCh:
		exitCode = status.StatusCode
	case waitErr := <-errCh:
		return "", nil, fmt.Errorf("gha_job: wait: %w", waitErr)
	case <-ctx.Done():
		_ = ce.docker.ContainerKill(context.WithoutCancel(ctx), containerID, "KILL")
		return "", nil, ctx.Err()
	}

	// 12. Collect final logs
	finalLogs, _ := ce.collectLogs(ctx, containerID)
	stdout = finalLogs

	addLog("info", fmt.Sprintf("Runner exited with code %d", exitCode), map[string]any{
		"exit_code": exitCode,
	})

	if exitCode != 0 {
		return stdout, nil, fmt.Errorf("gha_job: runner exited with code %d\n%s",
			exitCode, truncate(finalLogs, 600))
	}

	// 13. Upload artifact outputs
	artifacts, err = ce.uploadArtifacts(ctx, msg, workspaceDir, addLog)
	if err != nil {
		return stdout, nil, fmt.Errorf("gha_job: upload artifacts: %w", err)
	}

	addLog("info", "GHA job completed", map[string]any{
		"steps":         len(steps),
		"artifacts_out": len(artifacts),
	})
	return stdout, artifacts, nil
}

// Helpers

// extractSteps deserialises the steps list from task Config.
func extractSteps(cfg map[string]any) ([]models.GHAStep, error) {
	raw, ok := cfg["steps"]
	if !ok {
		return nil, fmt.Errorf("config missing 'steps' key")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("marshal steps: %w", err)
	}
	var steps []models.GHAStep
	if err := json.Unmarshal(b, &steps); err != nil {
		return nil, fmt.Errorf("unmarshal steps: %w", err)
	}
	return steps, nil
}

// buildGHAContext creates a GHAContext from the task message and environment.
func buildGHAContext(msg *models.TaskMessage) models.GHAContext {
	ctx := models.GHAContext{
		RunID:      msg.WorkflowExecID[:8],
		RunNumber:  "1",
		Actor:      "fluxor",
		EventName:  "workflow_dispatch",
		Workspace:  "/workspace",
		Repository: "local/repo",
		Env:        make(map[string]string),
		Inputs:     make(map[string]string),
	}
	// Allow caller to provide richer context via TriggerPayload
	if cfg := msg.Config; cfg != nil {
		if sha, ok := cfg["github_sha"].(string); ok {
			ctx.SHA = sha
		}
		if ref, ok := cfg["github_ref"].(string); ok {
			ctx.Ref = ref
			parts := splitRef(ref)
			ctx.RefName = parts
		}
		if repo, ok := cfg["github_repository"].(string); ok {
			ctx.Repository = repo
		}
	}
	return ctx
}

func splitRef(ref string) string {
	parts := []string{ref}
	for _, prefix := range []string{"refs/heads/", "refs/tags/"} {
		if len(ref) > len(prefix) && ref[:len(prefix)] == prefix {
			return ref[len(prefix):]
		}
	}
	return parts[0]
}

// buildGHAEnv constructs the environment variables injected into the runner
// container, including all standard GITHUB_* variables and user-defined env.
func buildGHAEnv(msg *models.TaskMessage, ghaCtx models.GHAContext, workspaceDir string) []string {
	m := map[string]string{
		// Standard GitHub Actions environment variables
		"GITHUB_WORKSPACE":    "/workspace",
		"GITHUB_ENV":          "/fluxor/github_env",
		"GITHUB_OUTPUT":       "/fluxor/github_output",
		"GITHUB_PATH":         "/fluxor/github_path",
		"GITHUB_STEP_SUMMARY": "/fluxor/github_step_summary",
		"GITHUB_SHA":          ghaCtx.SHA,
		"GITHUB_REF":          ghaCtx.Ref,
		"GITHUB_REF_NAME":     ghaCtx.RefName,
		"GITHUB_REPOSITORY":   ghaCtx.Repository,
		"GITHUB_RUN_ID":       ghaCtx.RunID,
		"GITHUB_RUN_NUMBER":   ghaCtx.RunNumber,
		"GITHUB_ACTOR":        ghaCtx.Actor,
		"GITHUB_EVENT_NAME":   ghaCtx.EventName,
		"RUNNER_OS":           "Linux",
		"RUNNER_ARCH":         "X64",
		"RUNNER_TEMP":         "/tmp",
		"RUNNER_TOOL_CACHE":   "/opt/hostedtoolcache",
		"CI":                  "true",
		// Fluxor metadata
		"FLUXOR_TASK_EXEC_ID":     msg.TaskExecID,
		"FLUXOR_WORKFLOW_EXEC_ID": msg.WorkflowExecID,
		"FLUXOR_TASK_NAME":        msg.TaskName,
		"FLUXOR_STEPS_FILE":       "/fluxor/steps.json",
		"FLUXOR_CONTEXT_FILE":     "/fluxor/context.json",
		// Path
		"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/go/bin",
		"HOME": "/root",
	}
	// Merge user-defined env from task config
	if envMap, ok := msg.Config["env"].(map[string]string); ok {
		for k, v := range envMap {
			m[k] = v
		}
	}
	if envMap, ok := msg.Config["env"].(map[string]any); ok {
		for k, v := range envMap {
			if vs, ok := v.(string); ok {
				m[k] = vs
			}
		}
	}

	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// streamContainerLogs attaches to the container log stream and forwards each
// line to addLog for real-time WebSocket streaming.
func (ce *ContainerExecutor) streamContainerLogs(ctx context.Context, containerID string, buf *bytes.Buffer, addLog logFn) {
	reader, err := ce.docker.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: false,
	})
	if err != nil {
		return
	}
	defer func() { _ = reader.Close() }()

	var outBuf, errBuf bytes.Buffer
	if _, err := stdcopy.StdCopy(&outBuf, &errBuf, reader); err != nil {
		io.Copy(buf, reader) //nolint:errcheck
		return
	}

	combined := outBuf.String()
	if errBuf.Len() > 0 {
		combined += errBuf.String()
	}
	buf.WriteString(combined)

	// Forward each line individually so the terminal streams in real time
	for _, line := range splitLines(combined) {
		if line == "" {
			continue
		}
		level := "info"
		if isErrorLine(line) {
			level = "error"
		} else if isWarnLine(line) {
			level = "warn"
		}
		addLog(level, line, nil)
	}
}

// stepNames returns just the name of each step for logging.
func stepNames(steps []models.GHAStep) []string {
	names := make([]string, len(steps))
	for i, s := range steps {
		names[i] = s.Name
	}
	return names
}

func splitLines(s string) []string {
	return splitByNewline(s)
}

func splitByNewline(s string) []string {
	var out []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func isErrorLine(line string) bool {
	l := lowerASCII(line)
	return hasPrefix(l, "error") || hasPrefix(l, "fatal") || hasPrefix(l, "##[error]")
}

func isWarnLine(line string) bool {
	l := lowerASCII(line)
	return hasPrefix(l, "warning") || hasPrefix(l, "##[warning]")
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
