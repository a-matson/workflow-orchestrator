package worker

// ContainerExecutor runs a task inside an ephemeral Docker container.
//
// Security model:
//   - All Linux capabilities dropped (--cap-drop=ALL)
//   - No-new-privileges flag set
//   - Read-only root filesystem with a tmpfs /tmp
//   - No network at all (NetworkMode "none"): user code cannot reach postgres,
//     redis or sibling task containers
//   - CPU and memory hard limits from ContainerSpec
//   - Container removed immediately after exit (AutoRemove=false so we can
//     read logs, then we remove manually)
//
// Artifact flow:
//   Before start: artifacts_in keys are downloaded from MinIO and written
//                 to a per-task directory mounted at /workspace.
//   After exit:   artifacts_out paths are read from /workspace and uploaded
//                 to MinIO. Keys are: artifacts/{execID}/{taskDefID}/{path}

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockerclient "github.com/moby/moby/client"
	"github.com/moby/moby/client/pkg/versions"
	"github.com/rs/zerolog/log"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/storage"
)

const (
	// DefaultImage is used when ContainerSpec.Image is empty.
	DefaultImage = "alpine:3.22"

	// DefaultMemoryMB / DefaultCPUMillis applied when ContainerSpec omits them.
	DefaultMemoryMB  int64 = 256
	DefaultCPUMillis int64 = 500

	// WorkspaceDir is the in-container path where artifacts are mounted.
	WorkspaceDir = "/workspace"
)

// Workspace says where per-task workspace directories are created and how the
// Docker daemon reaches them.
//
// When the backend itself runs in a container and drives the host daemon through
// the socket, its paths do not exist on the host, so a bind mount cannot work.
// Volume then names a Docker volume mounted at Root in this process, and each
// task container mounts only its own subdirectory of it. With Volume empty, the
// directory is bind-mounted, which only works when this process and the daemon
// share a filesystem.
type Workspace struct {
	Root   string // parent directory; empty means os.TempDir()
	Volume string
}

// artifactStore is the part of the MinIO client the executor uses; tests fake it.
type artifactStore interface {
	Upload(ctx context.Context, key string, r io.Reader, size int64, contentType string) (models.ResolvedArtifact, error)
	Download(ctx context.Context, key string) (io.ReadCloser, int64, error)
}

// ContainerExecutor wraps the Docker client and MinIO client.
type ContainerExecutor struct {
	docker           *dockerclient.Client
	storage          artifactStore
	workspace        Workspace
	maxArtifactBytes int64
	limits           Limits
}

// DefaultMaxArtifactBytes caps one artifact when FLUXOR_MAX_ARTIFACT_BYTES is unset.
const DefaultMaxArtifactBytes int64 = 100 << 20

// maxArtifactBytesFromEnv reads FLUXOR_MAX_ARTIFACT_BYTES, the per-artifact size
// cap for both upload and download. A set but invalid value is an error so a
// typo cannot silently lift the cap.
func maxArtifactBytesFromEnv() (int64, error) {
	v := os.Getenv("FLUXOR_MAX_ARTIFACT_BYTES")
	if v == "" {
		return DefaultMaxArtifactBytes, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("FLUXOR_MAX_ARTIFACT_BYTES=%q: want a positive byte count", v)
	}
	return n, nil
}

// NewContainerExecutor connects to the Docker Engine named by DOCKER_HOST.
func NewContainerExecutor(storageClient *storage.Client, ws Workspace) (*ContainerExecutor, error) {
	// The client negotiates the API version on its first request.
	dc, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("docker: connect: %w", err)
	}

	maxArtifactBytes, err := maxArtifactBytesFromEnv()
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	if ws.Volume != "" {
		// Daemons older than API 1.45 drop VolumeOptions.Subpath without an
		// error and mount the whole volume, exposing every task's workspace.
		ping, err := dc.Ping(ctx, dockerclient.PingOptions{NegotiateAPIVersion: true})
		if err != nil {
			return nil, fmt.Errorf("docker: negotiate API version: %w", err)
		}
		// Without a version header the client assumes its own maximum, which would
		// pass the gate below for a daemon of unknown age.
		if ping.APIVersion == "" {
			return nil, fmt.Errorf("docker: daemon did not report an API version; cannot confirm volume subpath support")
		}
		if !subpathSupported(dc.ClientVersion()) {
			return nil, fmt.Errorf("docker: API %s lacks volume subpaths (need >= %s); task workspaces would not be isolated",
				dc.ClientVersion(), minSubpathAPI)
		}
	}
	if err := prepareWorkspaceRoot(ws); err != nil {
		return nil, err
	}

	ce := &ContainerExecutor{docker: dc, storage: storageClient, workspace: ws, maxArtifactBytes: maxArtifactBytes}
	ce.reapOrphans(ctx)
	return ce, nil
}

// stackLabel tells apart the task containers of stacks sharing one daemon (dev
// and e2e), so one stack's startup does not reap the other's running tasks.
const stackLabel = "fluxor.stack"

// stackID is the volume when there is one (compose gives each stack its own,
// while Root is the same in-container path in all of them), else the root.
func (w Workspace) stackID() string {
	if w.Volume != "" {
		return w.Volume
	}
	return w.Root
}

// reapOrphans force-removes this stack's task containers in any state. Workers
// run in-process, so at startup none can be legitimately running: they are
// leftovers of a previous process, and the recovery re-send would duplicate them.
// Failures are logged and never block startup.
func (ce *ContainerExecutor) reapOrphans(ctx context.Context) {
	list, err := ce.docker.ContainerList(ctx, dockerclient.ContainerListOptions{
		All:     true,
		Filters: make(dockerclient.Filters).Add("label", stackLabel+"="+ce.workspace.stackID()),
	})
	if err != nil {
		log.Error().Err(err).Msg("orphan task containers not listed")
		return
	}
	removed := 0
	for _, c := range list.Items {
		if _, err := ce.docker.ContainerRemove(ctx, c.ID, dockerclient.ContainerRemoveOptions{Force: true}); err != nil {
			log.Error().Err(err).Str("container_id", c.ID).Msg("orphan task container not removed")
			continue
		}
		removed++
	}
	log.Info().Int("removed", removed).Msg("orphan task containers reaped")
}

const minSubpathAPI = "1.45"

func subpathSupported(apiVersion string) bool {
	return !versions.LessThan(apiVersion, minSubpathAPI)
}

// prepareWorkspaceRoot checks the root is usable and removes workspaces left by
// a previous process. Workers run in-process, so at startup none is in use.
func prepareWorkspaceRoot(ws Workspace) error {
	if ws.Volume != "" && ws.Root == "" {
		return fmt.Errorf("workspace: a volume (%q) needs a root where it is mounted", ws.Volume)
	}
	if ws.Root == "" {
		// The shared temp dir may hold other programs' ws-* directories.
		return nil
	}
	leftovers, err := filepath.Glob(filepath.Join(ws.Root, "ws-*"))
	if err != nil {
		return fmt.Errorf("workspace: list leftovers: %w", err)
	}
	for _, dir := range leftovers {
		if err := removeWorkspace(dir); err != nil {
			return err
		}
	}
	probe, err := os.MkdirTemp(ws.Root, "ws-*")
	if err != nil {
		return fmt.Errorf("workspace: root %q is not writable: %w", ws.Root, err)
	}
	return removeWorkspace(probe)
}

// removeWorkspace deletes dir even when the task, which runs as our uid, has
// removed permissions from directories inside it.
func removeWorkspace(dir string) error {
	// The walk goes through an os.Root so a symlink the task left cannot point
	// the chmod outside the workspace. dir itself is ours, not the task's, but
	// the task may have locked it, and the root cannot open it until unlocked.
	walkErr := os.Chmod(dir, 0o700) // #nosec G302 -- a directory needs its execute bit to be entered; owner only
	if root, err := os.OpenRoot(dir); err != nil {
		walkErr = errors.Join(walkErr, err)
	} else {
		walkErr = errors.Join(walkErr, fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// Unreadable despite the chmod; RemoveAll reports it below.
				return nil
			}
			if d.IsDir() {
				return root.Chmod(path, 0o700)
			}
			return nil
		}), root.Close())
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("workspace: remove %q: %w", dir, errors.Join(err, walkErr))
	}
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
	workspaceDir, err := os.MkdirTemp(ce.workspace.Root, "ws-*")
	if err != nil {
		return "", nil, fmt.Errorf("container: create workspace: %w", withoutHostPath(err))
	}
	// MkdirTemp creates it 0700; the task container runs as this process's uid
	// so it can still write here.
	defer func() {
		if err := removeWorkspace(workspaceDir); err != nil {
			log.Error().Err(err).Str("path", workspaceDir).Msg("task workspace not removed")
		}
	}()

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
		// With every capability dropped, even root cannot write the 0700
		// workspace unless it owns it. A backend started as root (not the
		// compose image, which runs as 10001) therefore runs tasks as root too.
		User: fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		// Security: prevent writing to root FS; only /workspace and /tmp are writable
		Labels: map[string]string{
			"fluxor.task_exec_id":     msg.TaskExecID,
			"fluxor.workflow_exec_id": msg.WorkflowExecID,
			stackLabel:                ce.workspace.stackID(),
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
		CapDrop:        []string{"ALL"}, // drop every Linux capability
		SecurityOpt:    []string{"no-new-privileges:true"},
		ReadonlyRootfs: true,

		// /tmp writable inside container (needed by many runtimes)
		Tmpfs: map[string]string{
			"/tmp": "rw,noexec,nosuid,size=64m",
		},

		Mounts: []mount.Mount{ce.workspaceMount(workspaceDir)},

		// Tasks that need the network are http_request or notification, which
		// run in-process behind the egress guard. A shared bridge would let user
		// code reach sibling containers and whatever the bridge routes to.
		NetworkMode: "none",

		// Explicit restart policy: never restart task containers
		RestartPolicy: container.RestartPolicy{Name: "no"},
	}

	addLog("info", "Creating isolated container", map[string]any{
		"image":      spec.Image,
		"memory_mb":  spec.MemoryMB,
		"cpu_millis": spec.CPUMillis,
		"network":    "none",
		// Scripts are passed as argv and may embed credentials.
		"cmd": argv0(cmd),
	})

	createResp, err := ce.docker.ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Config:     containerCfg,
		HostConfig: hostCfg,
	})
	if err != nil {
		return "", nil, fmt.Errorf("container: create: %w", err)
	}
	containerID := createResp.ID
	// Always clean up the container
	defer func() {
		// Create a detached context for cleanup so it isn't interrupted
		detachedCtx := context.WithoutCancel(ctx)
		cleanupCtx, cancel := context.WithTimeout(detachedCtx, 15*time.Second)
		defer cancel()
		// Best effort: the container has usually exited already, so kill fails.
		_, _ = ce.docker.ContainerKill(cleanupCtx, containerID, dockerclient.ContainerKillOptions{Signal: "KILL"})
		if _, err := ce.docker.ContainerRemove(cleanupCtx, containerID, dockerclient.ContainerRemoveOptions{Force: true}); err != nil {
			log.Error().Err(err).Str("container_id", containerID).Msg("task container not removed")
		}
	}()

	// ── 6. Start container ────────────────────────────────────────────────────
	addLog("info", fmt.Sprintf("Starting container %s", containerID[:12]), nil)
	if _, err := ce.docker.ContainerStart(ctx, containerID, dockerclient.ContainerStartOptions{}); err != nil {
		return "", nil, fmt.Errorf("container: start: %w", err)
	}

	// ── 7. Wait for exit ──────────────────────────────────────────────────────
	exitCode, err := ce.waitExit(ctx, containerID)
	if err != nil {
		if ctx.Err() != nil {
			// Best effort: the deferred force-remove above stops it if this kill fails.
			_, _ = ce.docker.ContainerKill(context.WithoutCancel(ctx), containerID, dockerclient.ContainerKillOptions{Signal: "KILL"})
			return "", nil, ctx.Err()
		}
		return "", nil, fmt.Errorf("container: wait: %w", err)
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
		"exit_code":     exitCode,
		"artifacts_out": len(artifacts),
	})
	return stdout, artifacts, nil
}

// workspaceMount exposes dir, and nothing else of the workspace root, at /workspace.
func (ce *ContainerExecutor) workspaceMount(dir string) mount.Mount {
	if ce.workspace.Volume == "" {
		return mount.Mount{Type: mount.TypeBind, Source: dir, Target: WorkspaceDir}
	}
	return mount.Mount{
		Type:   mount.TypeVolume,
		Source: ce.workspace.Volume,
		Target: WorkspaceDir,
		// NoCopy: otherwise the daemon copies the image's /workspace (root-owned,
		// 0755, created for WorkingDir) onto the empty subpath and takes away
		// our ownership of it.
		VolumeOptions: &mount.VolumeOptions{Subpath: filepath.Base(dir), NoCopy: true},
	}
}

const waitRetryPause = time.Second

// waitExit returns the container's exit code once it stops running.
func (ce *ContainerExecutor) waitExit(ctx context.Context, containerID string) (int64, error) {
	for {
		wait := ce.docker.ContainerWait(ctx, containerID, dockerclient.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
		select {
		case status := <-wait.Result:
			return status.StatusCode, nil
		case err := <-wait.Error:
			// The socket proxy (HAProxy) closes a response idle for 10 minutes, and
			// /wait sends nothing until the container exits. Waiting again is safe:
			// not-running answers at once for a container that already exited.
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				return 0, err
			}
			// The pause bounds the request rate if something cuts every wait at once.
			select {
			case <-time.After(waitRetryPause):
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

// ── Image pull ────────────────────────────────────────────────────────────────

func (ce *ContainerExecutor) pullImage(ctx context.Context, img string) error {
	// Check if already local
	if _, err := ce.docker.ImageInspect(ctx, img); err == nil {
		return nil // already present
	}

	reader, err := ce.docker.ImagePull(ctx, img, dockerclient.ImagePullOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	_, _ = io.Copy(io.Discard, reader) // consume the pull progress stream
	return nil
}

// ── Log collection ────────────────────────────────────────────────────────────

func (ce *ContainerExecutor) collectLogs(ctx context.Context, containerID string) (string, error) {
	reader, err := ce.docker.ContainerLogs(ctx, containerID, dockerclient.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = reader.Close() }()

	return readContainerLogs(reader, ce.limits.output())
}

// ── Artifact download ─────────────────────────────────────────────────────────

// Artifact paths come from the workflow definition and files in the workspace
// come from the task, so both are untrusted: every file is opened through an
// os.Root on the workspace, and errors name the artifact path, never a host path.

func openWorkspaceRoot(dir string) (*os.Root, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open task workspace: %w", withoutHostPath(err))
	}
	return root, nil
}

// withoutHostPath drops the path from a PathError; task errors reach the UI.
func withoutHostPath(err error) error {
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		return pe.Err
	}
	return err
}

func (ce *ContainerExecutor) downloadArtifacts(
	ctx context.Context,
	refs []models.ResolvedArtifact,
	destDir string,
	addLog logFn,
) error {
	root, err := openWorkspaceRoot(destDir)
	if err != nil {
		return err
	}
	// Read-only directory handle: Close has nothing to flush.
	defer func() { _ = root.Close() }()

	for _, ref := range refs {
		if ref.MinioKey == "" {
			continue
		}
		if err := ce.downloadArtifact(ctx, root, ref, addLog); err != nil {
			return err
		}
	}
	return nil
}

func (ce *ContainerExecutor) downloadArtifact(ctx context.Context, root *os.Root, ref models.ResolvedArtifact, addLog logFn) error {
	if !filepath.IsLocal(ref.Path) {
		return fmt.Errorf("artifact %q: path must be relative and stay inside the workspace", ref.Path)
	}
	if err := root.MkdirAll(filepath.Dir(ref.Path), 0o700); err != nil {
		return fmt.Errorf("mkdir for artifact %q: %w", ref.Path, err)
	}

	addLog("info", fmt.Sprintf("Downloading artifact: %s", ref.Path), map[string]any{
		"minio_key": ref.MinioKey,
	})

	reader, size, err := ce.storage.Download(ctx, ref.MinioKey)
	if err != nil {
		return fmt.Errorf("download %q: %w", ref.MinioKey, err)
	}
	// Download-only stream: a Close error cannot lose data we kept.
	defer func() { _ = reader.Close() }()
	if size > ce.maxArtifactBytes {
		return fmt.Errorf("artifact %q is %d bytes, over the %d-byte limit", ref.Path, size, ce.maxArtifactBytes)
	}

	f, err := root.OpenFile(ref.Path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create artifact %q: %w", ref.Path, err)
	}
	// Read one byte past the cap so an object larger than its reported size is caught.
	n, copyErr := io.Copy(f, io.LimitReader(reader, ce.maxArtifactBytes+1))
	if err := errors.Join(copyErr, f.Close()); err != nil {
		return fmt.Errorf("write artifact %q: %w", ref.Path, err)
	}
	if n > ce.maxArtifactBytes {
		if err := root.Remove(ref.Path); err != nil {
			log.Warn().Err(err).Str("artifact", ref.Path).Msg("oversized artifact not removed; workspace cleanup will")
		}
		return fmt.Errorf("artifact %q exceeds the %d-byte limit", ref.Path, ce.maxArtifactBytes)
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
	root, err := openWorkspaceRoot(workspaceDir)
	if err != nil {
		return nil, err
	}
	// Read-only directory handle: Close has nothing to flush.
	defer func() { _ = root.Close() }()

	var uploaded []models.ResolvedArtifact
	for _, ref := range msg.ArtifactsOut {
		resolved, found, err := ce.uploadArtifact(ctx, root, msg, ref.Path, addLog)
		if err != nil {
			return nil, err
		}
		if found {
			uploaded = append(uploaded, resolved)
		}
	}
	return uploaded, nil
}

func (ce *ContainerExecutor) uploadArtifact(
	ctx context.Context,
	root *os.Root,
	msg *models.TaskMessage,
	path string,
	addLog logFn,
) (models.ResolvedArtifact, bool, error) {
	if !filepath.IsLocal(path) {
		return models.ResolvedArtifact{}, false, fmt.Errorf("artifact %q: path must be relative and stay inside the workspace", path)
	}
	fi, err := root.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		addLog("warn", fmt.Sprintf("Expected artifact not found: %s", path), nil)
		return models.ResolvedArtifact{}, false, nil
	}
	if err != nil {
		return models.ResolvedArtifact{}, false, fmt.Errorf("stat artifact %q: %w", path, err)
	}
	// Regular files only: a FIFO or device would block or stream forever, and
	// one rule for every non-regular file is simpler than resolving symlinks
	// (os.Root already stops those escaping). A symlinked output such as
	// latest -> run-3.csv therefore fails the task.
	// The container has exited, so nothing can swap the file after this check.
	if !fi.Mode().IsRegular() {
		return models.ResolvedArtifact{}, false, fmt.Errorf("artifact %q is not a regular file (%s)", path, fi.Mode().Type())
	}
	if fi.Size() > ce.maxArtifactBytes {
		return models.ResolvedArtifact{}, false, fmt.Errorf("artifact %q is %d bytes, over the %d-byte limit", path, fi.Size(), ce.maxArtifactBytes)
	}

	key := storage.ArtifactKey(msg.WorkflowExecID, msg.TaskDefinitionID, path)
	addLog("info", fmt.Sprintf("Uploading artifact: %s", path), map[string]any{
		"minio_key": key,
		"size":      fi.Size(),
	})

	f, err := root.Open(path)
	if err != nil {
		return models.ResolvedArtifact{}, false, fmt.Errorf("open artifact %q: %w", path, err)
	}
	// Opened read-only: Close has nothing to flush.
	defer func() { _ = f.Close() }()

	// The limit keeps the stream at the size MinIO was promised, so a file that
	// grew after Lstat cannot push past the cap.
	resolved, err := ce.storage.Upload(ctx, key, io.LimitReader(f, fi.Size()), fi.Size(), "")
	if err != nil {
		return models.ResolvedArtifact{}, false, fmt.Errorf("upload artifact %q: %w", path, err)
	}
	resolved.Path = path
	return resolved, true, nil
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
//
//	http_request (curl), database_query (psql), notification (curl).
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
			script, _ := cfg["script"].(string)
			if script != "" {
				return []string{"sh", "-c", script}
			}
			if model, _ := cfg["model_name"].(string); model != "" && msg.TaskType == "ml_inference" {
				return mlInferenceCommand(model, cfg)
			}
			return nil
		}
		// An explicit command wins over script: it is the older, more specific
		// key, and args only make sense alongside it.
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
// FLUXOR_* keys always come from the executor: a workflow that could override
// FLUXOR_WORKSPACE or FLUXOR_TASK_EXEC_ID would mislead the scripts and SDKs
// that trust them. Malformed keys are dropped because Docker splits on the
// first '=' and C strings end at NUL.
func buildEnv(msg *models.TaskMessage, spec models.ContainerSpec) []string {
	user := map[string]string{}
	for k, v := range spec.Env {
		user[k] = v
	}
	if envMap, ok := msg.Config["env"].(map[string]any); ok {
		for k, v := range envMap {
			if vs, ok := v.(string); ok {
				user[k] = vs
			}
		}
	}
	env := map[string]string{}
	var dropped []string
	for k, v := range user {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.HasPrefix(strings.ToUpper(k), "FLUXOR_") {
			dropped = append(dropped, k)
			continue
		}
		env[k] = v
	}
	if len(dropped) > 0 {
		sort.Strings(dropped)
		// Key names only: values may be secrets.
		log.Warn().Str("task_exec_id", msg.TaskExecID).Strs("keys", dropped).
			Msg("container: ignored reserved or invalid task env keys")
	}
	env["FLUXOR_TASK_EXEC_ID"] = msg.TaskExecID
	env["FLUXOR_WORKFLOW_EXEC_ID"] = msg.WorkflowExecID
	env["FLUXOR_TASK_NAME"] = msg.TaskName
	env["FLUXOR_WORKSPACE"] = WorkspaceDir
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result
}

func int64Ptr(v int64) *int64 { return &v }

// mlInferenceCommand keeps the flags the removed in-process executor passed, so
// saved ml_inference definitions invoke their model the same way.
func mlInferenceCommand(model string, cfg map[string]any) []string {
	batchSize := 32
	if bs, ok := cfg["batch_size"].(float64); ok && bs > 0 {
		batchSize = int(bs)
	}
	args := []string{model, "--batch-size", strconv.Itoa(batchSize)}
	if in, _ := cfg["input_path"].(string); in != "" {
		args = append(args, "--input", in)
	}
	if out, _ := cfg["output_path"].(string); out != "" {
		args = append(args, "--output", out)
	}
	return args
}

// argv0 is all of a command that is safe to log: the rest is usually a script body.
func argv0(cmd []string) string {
	if len(cmd) == 0 {
		return ""
	}
	return cmd[0]
}
