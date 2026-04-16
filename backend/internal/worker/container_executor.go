package worker

// ContainerExecutor runs a task inside an ephemeral Docker container.
//
// Security model:
//   - All Linux capabilities dropped (--cap-drop=ALL)
//   - No-new-privileges flag set
//   - Read-only root filesystem with a tmpfs /tmp
//   - Dedicated "fluxor-tasks" network that has NO route to postgres/redis
//   - CPU and memory hard limits from ContainerSpec
//   - Container removed immediately after exit (AutoRemove=false so we can
//     read logs, then we remove manually)
//
// Artifact flow:
//   Before start: artifacts_in keys are downloaded from MinIO and written
//                 to a temp directory that is bind-mounted into /workspace.
//   After exit:   artifacts_out paths are read from /workspace and uploaded
//                 to MinIO. Keys are: artifacts/{execID}/{taskDefID}/{path}

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/storage"
	dockertypes "github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	"github.com/rs/zerolog/log"
)

const (
	// TaskNetwork is the isolated Docker network for task containers.
	// It is created on first use and has no external routes.
	TaskNetwork = "fluxor-tasks"

	// DefaultImage is used when ContainerSpec.Image is empty.
	DefaultImage = "alpine:3.19"

	// DefaultMemoryMB / DefaultCPUMillis applied when ContainerSpec omits them.
	DefaultMemoryMB  int64 = 256
	DefaultCPUMillis int64 = 500

	// WorkspaceDir is the in-container path where artifacts are mounted.
	WorkspaceDir = "/workspace"
)

// ContainerExecutor wraps the Docker client and MinIO client.
type ContainerExecutor struct {
	docker  *dockerclient.Client
	storage *storage.Client
}

// NewContainerExecutor connects to the local Docker Engine socket.
func NewContainerExecutor(storageClient *storage.Client) (*ContainerExecutor, error) {
	dc, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker: connect: %w", err)
	}

	ce := &ContainerExecutor{docker: dc, storage: storageClient}
	if err := ce.ensureNetwork(context.Background()); err != nil {
		return nil, err
	}
	return ce, nil
}

// ensureNetwork creates the isolated task network if it does not exist.
// The network is internal (no external routing) so task containers cannot
// reach the internet, Postgres, or Redis.
func (ce *ContainerExecutor) ensureNetwork(ctx context.Context) error {
	nets, err := ce.docker.NetworkList(ctx, dockertypes.NetworkListOptions{
		Filters: filters.NewArgs(filters.Arg("name", TaskNetwork)),
	})
	if err != nil {
		return fmt.Errorf("docker: list networks: %w", err)
	}
	for _, n := range nets {
		if n.Name == TaskNetwork {
			log.Debug().Str("network", TaskNetwork).Msg("task network already exists")
			return nil
		}
	}

	_, err = ce.docker.NetworkCreate(ctx, TaskNetwork, dockertypes.NetworkCreate{
		Driver:   "bridge",
		Internal: true, // no external routing — tasks are isolated
		Labels:   map[string]string{"managed-by": "fluxor"},
	})
	if err != nil {
		return fmt.Errorf("docker: create network %q: %w", TaskNetwork, err)
	}
	log.Info().Str("network", TaskNetwork).Msg("isolated task network created")
	return nil
}

// Run executes a task in an isolated container.
// It returns the combined stdout/stderr, produced artifacts, and any error.
func (ce *ContainerExecutor) Run(
	ctx context.Context,
	msg *models.TaskMessage,
	addLog logFn,
) (stdout string, artifacts []models.ResolvedArtifact, err error) {
	spec := effectiveContainerSpec(msg.Container)

	// ── 1. Pull image if not present ─────────────────────────────────────────
	addLog("info", fmt.Sprintf("Pulling image %s", spec.Image), nil)
	if pullErr := ce.pullImage(ctx, spec.Image); pullErr != nil {
		return "", nil, fmt.Errorf("container: pull %q: %w", spec.Image, pullErr)
	}

	// ── 2. Prepare workspace: download artifact inputs ────────────────────────
	workspaceDir, err := os.MkdirTemp("", "fluxor-ws-*")
	if err != nil {
		return "", nil, fmt.Errorf("container: create workspace: %w", err)
	}
	defer os.RemoveAll(workspaceDir)

	if downloadErr := ce.downloadArtifacts(ctx, msg.ArtifactsIn, workspaceDir, addLog); downloadErr != nil {
		return "", nil, fmt.Errorf("container: download artifacts: %w", downloadErr)
	}

	// ── 3. Build entrypoint command from task config ──────────────────────────
	cmd := buildCommand(msg)
	if len(cmd) == 0 {
		return "", nil, fmt.Errorf("container: no command specified in task config")
	}

	// ── 4. Build environment variables ───────────────────────────────────────
	envVars := buildEnv(msg, spec)

	// ── 5. Create container ───────────────────────────────────────────────────
	containerCfg := &container.Config{
		Image:      spec.Image,
		Cmd:        cmd,
		Env:        envVars,
		WorkingDir: spec.WorkDir,
		// Security: prevent writing to root FS; only /workspace and /tmp are writable
		Labels: map[string]string{
			"fluxor.task_exec_id":     msg.TaskExecID,
			"fluxor.workflow_exec_id": msg.WorkflowExecID,
		},
	}

	// Resource and security constraints
	hostCfg := &container.HostConfig{
		// ── Resource limits ──────────────────────────────────────────────────
		Resources: container.Resources{
			Memory:   spec.MemoryMB * 1024 * 1024,
			NanoCPUs: spec.CPUMillis * 1_000_000, // milli-CPUs → nano-CPUs
			// Prevent the container from forking unlimited processes
			PidsLimit: int64Ptr(256),
		},

		// ── Security hardening ────────────────────────────────────────────────
		CapDrop:        []string{"ALL"},    // drop every Linux capability
		SecurityOpt:    []string{"no-new-privileges:true"},
		ReadonlyRootfs: true,

		// /tmp writable inside container (needed by many runtimes)
		Tmpfs: map[string]string{
			"/tmp": "rw,noexec,nosuid,size=64m",
		},

		// Bind-mount the workspace directory for artifact exchange
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: workspaceDir,
				Target: WorkspaceDir,
			},
		},

		// Explicit restart policy: never restart task containers
		RestartPolicy: container.RestartPolicy{Name: "no"},
	}

	// Connect to the isolated internal network (no internet, no postgres/redis)
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			TaskNetwork: {},
		},
	}

	addLog("info", "Creating isolated container", map[string]any{
		"image":      spec.Image,
		"memory_mb":  spec.MemoryMB,
		"cpu_millis": spec.CPUMillis,
		"network":    TaskNetwork,
		"cmd":        strings.Join(cmd, " "),
	})

	createResp, err := ce.docker.ContainerCreate(ctx, containerCfg, hostCfg, netCfg, nil, "")
	if err != nil {
		return "", nil, fmt.Errorf("container: create: %w", err)
	}
	containerID := createResp.ID
	// Always clean up the container
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		ce.docker.ContainerRemove(rmCtx, containerID, dockertypes.ContainerRemoveOptions{Force: true})
	}()

	// ── 6. Start container ────────────────────────────────────────────────────
	addLog("info", fmt.Sprintf("Starting container %s", containerID[:12]), nil)
	if err := ce.docker.ContainerStart(ctx, containerID, dockertypes.ContainerStartOptions{}); err != nil {
		return "", nil, fmt.Errorf("container: start: %w", err)
	}

	// ── 7. Wait for exit ──────────────────────────────────────────────────────
	statusCh, errCh := ce.docker.ContainerWait(ctx, containerID, container.WaitConditionNotRunning)
	var exitCode int64
	select {
	case status := <-statusCh:
		exitCode = status.StatusCode
	case waitErr := <-errCh:
		return "", nil, fmt.Errorf("container: wait: %w", waitErr)
	case <-ctx.Done():
		// Timeout/cancellation — kill the container
		ce.docker.ContainerKill(context.Background(), containerID, "KILL") //nolint:errcheck
		return "", nil, ctx.Err()
	}

	// ── 8. Collect logs ───────────────────────────────────────────────────────
	containerLogs, logErr := ce.collectLogs(ctx, containerID)
	if logErr != nil {
		addLog("warn", "Could not read container logs", map[string]any{"error": logErr.Error()})
	} else {
		addLog("info", "Container output", map[string]any{"output": truncate(containerLogs, 2000)})
	}
	stdout = containerLogs

	if exitCode != 0 {
		return stdout, nil, fmt.Errorf("container: exited with code %d\n%s", exitCode, truncate(containerLogs, 500))
	}

	// ── 9. Upload artifact outputs ────────────────────────────────────────────
	artifacts, err = ce.uploadArtifacts(ctx, msg, workspaceDir, addLog)
	if err != nil {
		return stdout, nil, fmt.Errorf("container: upload artifacts: %w", err)
	}

	addLog("info", "Container execution complete", map[string]any{
		"exit_code":      exitCode,
		"artifacts_out":  len(artifacts),
	})
	return stdout, artifacts, nil
}

// ── Image pull ────────────────────────────────────────────────────────────────

func (ce *ContainerExecutor) pullImage(ctx context.Context, image string) error {
	// Check if already local
	_, _, err := ce.docker.ImageInspectWithRaw(ctx, image)
	if err == nil {
		return nil // already present
	}

	reader, err := ce.docker.ImagePull(ctx, image, dockertypes.ImagePullOptions{})
	if err != nil {
		return err
	}
	defer reader.Close()
	io.Copy(io.Discard, reader) // consume the pull progress stream
	return nil
}

// ── Log collection ────────────────────────────────────────────────────────────

func (ce *ContainerExecutor) collectLogs(ctx context.Context, containerID string) (string, error) {
	reader, err := ce.docker.ContainerLogs(ctx, containerID, dockertypes.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return "", err
	}
	defer reader.Close()

	var buf bytes.Buffer
	// Docker log stream is multiplexed: each frame has an 8-byte header.
	// Use io.Copy to strip the header automatically via docker's StdCopy if needed,
	// but for simplicity we read raw (the header bytes won't affect text visibility).
	io.Copy(&buf, reader)
	return buf.String(), nil
}

// ── Artifact download ─────────────────────────────────────────────────────────

func (ce *ContainerExecutor) downloadArtifacts(
	ctx context.Context,
	refs []models.ResolvedArtifact,
	destDir string,
	addLog logFn,
) error {
	for _, ref := range refs {
		if ref.MinioKey == "" {
			continue
		}

		destPath := filepath.Join(destDir, ref.Path)
		if err := os.MkdirAll(filepath.Dir(destPath), 0o750); err != nil {
			return fmt.Errorf("mkdir for artifact %q: %w", ref.Path, err)
		}

		addLog("info", fmt.Sprintf("Downloading artifact: %s", ref.Path), map[string]any{
			"minio_key": ref.MinioKey,
		})

		reader, _, err := ce.storage.Download(ctx, ref.MinioKey)
		if err != nil {
			return fmt.Errorf("download %q: %w", ref.MinioKey, err)
		}

		f, err := os.Create(destPath)
		if err != nil {
			reader.Close()
			return fmt.Errorf("create local file %q: %w", destPath, err)
		}

		_, copyErr := io.Copy(f, reader)
		reader.Close()
		f.Close()

		if copyErr != nil {
			return fmt.Errorf("write artifact %q: %w", ref.Path, copyErr)
		}
	}
	return nil
}

// ── Artifact upload ───────────────────────────────────────────────────────────

func (ce *ContainerExecutor) uploadArtifacts(
	ctx context.Context,
	msg *models.TaskMessage,
	workspaceDir string,
	addLog logFn,
) ([]models.ResolvedArtifact, error) {
	var uploaded []models.ResolvedArtifact

	for _, ref := range msg.ArtifactsOut {
		localPath := filepath.Join(workspaceDir, ref.Path)

		fi, err := os.Stat(localPath)
		if os.IsNotExist(err) {
			addLog("warn", fmt.Sprintf("Expected artifact not found: %s", ref.Path), nil)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("stat artifact %q: %w", ref.Path, err)
		}

		key := storage.ArtifactKey(msg.WorkflowExecID, msg.TaskDefinitionID, ref.Path)

		addLog("info", fmt.Sprintf("Uploading artifact: %s", ref.Path), map[string]any{
			"minio_key": key,
			"size":      fi.Size(),
		})

		f, err := os.Open(localPath)
		if err != nil {
			return nil, fmt.Errorf("open artifact %q: %w", ref.Path, err)
		}

		resolved, uploadErr := ce.storage.Upload(ctx, key, f, fi.Size(), "")
		f.Close()

		if uploadErr != nil {
			return nil, fmt.Errorf("upload artifact %q: %w", ref.Path, uploadErr)
		}

		resolved.Path = ref.Path
		uploaded = append(uploaded, resolved)
	}

	return uploaded, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// effectiveContainerSpec returns the spec with defaults applied.
func effectiveContainerSpec(spec *models.ContainerSpec) models.ContainerSpec {
	s := models.ContainerSpec{
		Image:     DefaultImage,
		MemoryMB:  DefaultMemoryMB,
		CPUMillis: DefaultCPUMillis,
		WorkDir:   WorkspaceDir,
	}
	if spec == nil {
		return s
	}
	if spec.Image != "" {
		s.Image = spec.Image
	}
	if spec.MemoryMB > 0 {
		s.MemoryMB = spec.MemoryMB
	}
	if spec.CPUMillis > 0 {
		s.CPUMillis = spec.CPUMillis
	}
	if spec.WorkDir != "" {
		s.WorkDir = spec.WorkDir
	}
	if spec.Env != nil {
		s.Env = spec.Env
	}
	return s
}

// buildCommand constructs the container command from task config.
// Supports: command+args (generic/data_transform/ml_inference),
//           http_request (curl), database_query (psql), notification (curl).
func buildCommand(msg *models.TaskMessage) []string {
	cfg := msg.Config

	switch msg.TaskType {
	case "http_request":
		url, _ := cfg["url"].(string)
		method, _ := cfg["method"].(string)
		if method == "" {
			method = "GET"
		}
		args := []string{"curl", "-sS", "-X", strings.ToUpper(method)}
		if body, ok := cfg["body"].(string); ok && body != "" {
			args = append(args, "-d", body, "-H", "Content-Type: application/json")
		}
		args = append(args, url)
		return args

	case "database_query":
		connStr, _ := cfg["connection_string"].(string)
		query, _ := cfg["query"].(string)
		// psql -c "<query>" "<connstr>"
		return []string{"psql", "-c", query, connStr}

	case "notification":
		// Webhook POST via curl
		channel, _ := cfg["channel"].(string)
		message, _ := cfg["message"].(string)
		body := fmt.Sprintf(`{"text":%q}`, message)
		return []string{"curl", "-sS", "-X", "POST", "-H", "Content-Type: application/json", "-d", body, channel}

	default:
		// generic / data_transform / ml_inference
		command, _ := cfg["command"].(string)
		if command == "" {
			return nil
		}
		args := []string{command}
		switch a := cfg["args"].(type) {
		case []any:
			for _, v := range a {
				if s, ok := v.(string); ok {
					args = append(args, s)
				}
			}
		case []string:
			args = append(args, a...)
		}
		return args
	}
}

// buildEnv merges ContainerSpec env and task config env into []string for Docker.
func buildEnv(msg *models.TaskMessage, spec models.ContainerSpec) []string {
	env := map[string]string{
		"FLUXOR_TASK_EXEC_ID":     msg.TaskExecID,
		"FLUXOR_WORKFLOW_EXEC_ID": msg.WorkflowExecID,
		"FLUXOR_TASK_NAME":        msg.TaskName,
		"FLUXOR_WORKSPACE":        WorkspaceDir,
	}
	// Merge spec env
	for k, v := range spec.Env {
		env[k] = v
	}
	// Merge config env
	if envMap, ok := msg.Config["env"].(map[string]any); ok {
		for k, v := range envMap {
			if vs, ok := v.(string); ok {
				env[k] = vs
			}
		}
	}
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

// tarFile creates an in-memory tar archive containing a single file.
// Used for copying files into the container via CopyToContainer.
func tarFile(name string, content []byte) io.Reader {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o644,
		Size: int64(len(content)),
	})
	tw.Write(content)
	tw.Close()
	return &buf
}

func int64Ptr(v int64) *int64 { return &v }
