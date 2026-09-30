package worker

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

type fakeStore struct {
	uploaded map[string][]byte
	objects  map[string]string
}

func (f *fakeStore) Upload(_ context.Context, key string, r io.Reader, _ int64, _ string) (models.ResolvedArtifact, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return models.ResolvedArtifact{}, err
	}
	if f.uploaded == nil {
		f.uploaded = map[string][]byte{}
	}
	f.uploaded[key] = b
	return models.ResolvedArtifact{MinioKey: key, Size: int64(len(b))}, nil
}

func (f *fakeStore) Download(_ context.Context, key string) (io.ReadCloser, int64, error) {
	body := f.objects[key]
	return io.NopCloser(strings.NewReader(body)), int64(len(body)), nil
}

func noLog(string, string, map[string]any) {}

// workspaceWithSecret returns a workspace dir whose parent holds secret.txt,
// the file an escaping artifact path would reach.
func workspaceWithSecret(t *testing.T) (ws, parent string) {
	t.Helper()
	parent = t.TempDir()
	ws = filepath.Join(parent, "ws")
	if err := os.Mkdir(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "secret.txt"), []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	return ws, parent
}

func uploadOne(t *testing.T, ce *ContainerExecutor, ws, path string) error {
	t.Helper()
	msg := &models.TaskMessage{WorkflowExecID: "wf", TaskDefinitionID: "t",
		ArtifactsOut: []models.ArtifactRef{{Path: path}}}
	_, err := ce.uploadArtifacts(context.Background(), msg, ws, noLog)
	return err
}

func TestUploadArtifacts_RejectsParentPath(t *testing.T) {
	ws, _ := workspaceWithSecret(t)
	store := &fakeStore{}
	ce := &ContainerExecutor{storage: store, maxArtifactBytes: DefaultMaxArtifactBytes}
	if err := uploadOne(t, ce, ws, "../secret.txt"); err == nil {
		t.Error("upload of ../secret.txt succeeded, want error")
	}
	if len(store.uploaded) != 0 {
		t.Errorf("uploaded %v, want nothing", store.uploaded)
	}
}

func TestUploadArtifacts_RejectsSymlink(t *testing.T) {
	ws, _ := workspaceWithSecret(t)
	if err := os.Symlink("../secret.txt", filepath.Join(ws, "link")); err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{}
	ce := &ContainerExecutor{storage: store, maxArtifactBytes: DefaultMaxArtifactBytes}
	if err := uploadOne(t, ce, ws, "link"); err == nil {
		t.Error("upload through a symlink succeeded, want error")
	}
	if len(store.uploaded) != 0 {
		t.Errorf("uploaded %v, want nothing", store.uploaded)
	}
}

func TestDownloadArtifacts_RejectsParentPath(t *testing.T) {
	ws, parent := workspaceWithSecret(t)
	ce := &ContainerExecutor{storage: &fakeStore{objects: map[string]string{"k": "pwned"}}, maxArtifactBytes: DefaultMaxArtifactBytes}
	err := ce.downloadArtifacts(context.Background(),
		[]models.ResolvedArtifact{{Path: "../pwned", MinioKey: "k"}}, ws, noLog)
	if err == nil {
		t.Error("download to ../pwned succeeded, want error")
	}
	if _, statErr := os.Lstat(filepath.Join(parent, "pwned")); !os.IsNotExist(statErr) {
		t.Errorf("file created outside the workspace: %v", statErr)
	}
}

func TestUploadArtifacts_EnforcesSizeCap(t *testing.T) {
	t.Setenv("FLUXOR_MAX_ARTIFACT_BYTES", "4")
	limit, err := maxArtifactBytesFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "big"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{}
	ce := &ContainerExecutor{storage: store, maxArtifactBytes: limit}
	if err := uploadOne(t, ce, ws, "big"); err == nil {
		t.Error("5-byte upload under a 4-byte cap succeeded, want error")
	}
	for k, v := range store.uploaded {
		if bytes.Equal(v, []byte("12345")) {
			t.Errorf("%s uploaded in full past the cap", k)
		}
	}
}

func TestArtifacts_RoundTripInSubdirectory(t *testing.T) {
	ws := t.TempDir()
	store := &fakeStore{objects: map[string]string{"k": "data"}}
	ce := &ContainerExecutor{storage: store, maxArtifactBytes: DefaultMaxArtifactBytes}
	if err := ce.downloadArtifacts(context.Background(),
		[]models.ResolvedArtifact{{Path: "in/data.txt", MinioKey: "k"}}, ws, noLog); err != nil {
		t.Fatal(err)
	}
	if err := uploadOne(t, ce, ws, "in/data.txt"); err != nil {
		t.Fatal(err)
	}
	if got := string(store.uploaded["artifacts/wf/t/in/data.txt"]); got != "data" {
		t.Errorf("uploaded %q, want %q", got, "data")
	}
}

func TestDownloadArtifacts_EnforcesSizeCap(t *testing.T) {
	ws := t.TempDir()
	ce := &ContainerExecutor{storage: &fakeStore{objects: map[string]string{"k": "12345"}}, maxArtifactBytes: 4}
	if err := ce.downloadArtifacts(context.Background(),
		[]models.ResolvedArtifact{{Path: "big", MinioKey: "k"}}, ws, noLog); err == nil {
		t.Error("5-byte download under a 4-byte cap succeeded, want error")
	}
}

// Only os.Root stops these: the path is local and its last element is not a
// symlink, but a parent directory is.
func TestUploadArtifacts_RejectsSymlinkedParent(t *testing.T) {
	ws, _ := workspaceWithSecret(t)
	if err := os.Symlink("..", filepath.Join(ws, "sub")); err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{}
	ce := &ContainerExecutor{storage: store, maxArtifactBytes: DefaultMaxArtifactBytes}
	if err := uploadOne(t, ce, ws, "sub/secret.txt"); err == nil || len(store.uploaded) != 0 {
		t.Errorf("upload via symlinked parent: err=%v uploaded=%v", err, store.uploaded)
	}
}

func TestDownloadArtifacts_RejectsSymlinkedParent(t *testing.T) {
	ws, parent := workspaceWithSecret(t)
	if err := os.Symlink("..", filepath.Join(ws, "sub")); err != nil {
		t.Fatal(err)
	}
	ce := &ContainerExecutor{storage: &fakeStore{objects: map[string]string{"k": "pwned"}}, maxArtifactBytes: DefaultMaxArtifactBytes}
	if err := ce.downloadArtifacts(context.Background(),
		[]models.ResolvedArtifact{{Path: "sub/pwned", MinioKey: "k"}}, ws, noLog); err == nil {
		t.Error("download via symlinked parent succeeded, want error")
	}
	if _, err := os.Lstat(filepath.Join(parent, "pwned")); !os.IsNotExist(err) {
		t.Errorf("file created outside the workspace: %v", err)
	}
}

// unknownSizeStore reports an unknown size, so only the streaming limit can
// catch an oversized object.
type unknownSizeStore struct{ body *strings.Reader }

func (u *unknownSizeStore) Upload(context.Context, string, io.Reader, int64, string) (models.ResolvedArtifact, error) {
	return models.ResolvedArtifact{}, nil
}

func (u *unknownSizeStore) Download(context.Context, string) (io.ReadCloser, int64, error) {
	return io.NopCloser(u.body), -1, nil
}

func TestDownloadArtifacts_EnforcesSizeCapWhileStreaming(t *testing.T) {
	ws := t.TempDir()
	store := &unknownSizeStore{body: strings.NewReader(strings.Repeat("x", 1<<20))}
	ce := &ContainerExecutor{storage: store, maxArtifactBytes: 4}
	if err := ce.downloadArtifacts(context.Background(),
		[]models.ResolvedArtifact{{Path: "big", MinioKey: "k"}}, ws, noLog); err == nil {
		t.Error("1 MiB stream under a 4-byte cap succeeded, want error")
	}
	if store.body.Len() == 0 {
		t.Error("the whole stream was read; the cap must stop the copy early")
	}
	if _, err := os.Lstat(filepath.Join(ws, "big")); !os.IsNotExist(err) {
		t.Errorf("oversized artifact left in the workspace: %v", err)
	}
}

// A FIFO would block the worker on open or read.
func TestUploadArtifacts_RejectsFIFO(t *testing.T) {
	ws := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(ws, "p"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{}
	ce := &ContainerExecutor{storage: store, maxArtifactBytes: DefaultMaxArtifactBytes}
	if err := uploadOne(t, ce, ws, "p"); err == nil || len(store.uploaded) != 0 {
		t.Errorf("upload of a FIFO: err=%v uploaded=%v", err, store.uploaded)
	}
}
