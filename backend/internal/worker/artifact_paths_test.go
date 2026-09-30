package worker

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	ce := &ContainerExecutor{storage: store}
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
	ce := &ContainerExecutor{storage: store}
	if err := uploadOne(t, ce, ws, "link"); err == nil {
		t.Error("upload through a symlink succeeded, want error")
	}
	if len(store.uploaded) != 0 {
		t.Errorf("uploaded %v, want nothing", store.uploaded)
	}
}

func TestDownloadArtifacts_RejectsParentPath(t *testing.T) {
	ws, parent := workspaceWithSecret(t)
	ce := &ContainerExecutor{storage: &fakeStore{objects: map[string]string{"k": "pwned"}}}
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
