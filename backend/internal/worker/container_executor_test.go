package worker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/docker/api/types/mount"
)

func TestSubpathSupported(t *testing.T) {
	for v, want := range map[string]bool{
		"1.43": false, "1.44": false, "1.45": true, "1.51": true, "1.100": true,
	} {
		if got := subpathSupported(v); got != want {
			t.Errorf("subpathSupported(%q) = %v, want %v", v, got, want)
		}
	}
}

// lockedWorkspace mimics a task that made a subdirectory unreadable.
func lockedWorkspace(t *testing.T, root string) string {
	t.Helper()
	dir, err := os.MkdirTemp(root, "ws-*")
	if err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(dir, "locked")
	if err := os.MkdirAll(filepath.Join(locked, "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "inner", "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRemoveWorkspace_RestoresPermissions(t *testing.T) {
	dir := lockedWorkspace(t, t.TempDir())
	if err := removeWorkspace(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("workspace still present: %v", err)
	}
}

func TestPrepareWorkspaceRoot_SweepsLeftovers(t *testing.T) {
	root := t.TempDir()
	leftover := lockedWorkspace(t, root)
	other := filepath.Join(root, "keep")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := prepareWorkspaceRoot(Workspace{Root: root, Volume: "v"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("leftover workspace still present: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("non-workspace entry removed: %v", err)
	}
}

func TestPrepareWorkspaceRoot_RejectsBadConfig(t *testing.T) {
	for name, ws := range map[string]Workspace{
		"volume without root": {Volume: "v"},
		"missing root":        {Root: filepath.Join(t.TempDir(), "absent"), Volume: "v"},
	} {
		if err := prepareWorkspaceRoot(ws); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestWorkspaceMount(t *testing.T) {
	dir := "/workspaces/ws-123"
	bind := (&ContainerExecutor{}).workspaceMount(dir)
	if bind.Type != mount.TypeBind || bind.Source != dir || bind.Target != WorkspaceDir {
		t.Errorf("bind mount = %+v", bind)
	}
	vol := (&ContainerExecutor{workspace: Workspace{Root: "/workspaces", Volume: "v"}}).workspaceMount(dir)
	if vol.Type != mount.TypeVolume || vol.Source != "v" || vol.Target != WorkspaceDir ||
		vol.VolumeOptions == nil || vol.VolumeOptions.Subpath != "ws-123" || !vol.VolumeOptions.NoCopy {
		t.Errorf("volume mount = %+v (%+v)", vol, vol.VolumeOptions)
	}
}
