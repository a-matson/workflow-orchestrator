//go:build integration

package worker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	dockerclient "github.com/moby/moby/client"
)

const testImage = "alpine:3.22"

func createSleeper(t *testing.T, dc *dockerclient.Client, labels map[string]string) string {
	t.Helper()
	ctx := context.Background()
	res, err := dc.ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Config: &container.Config{Image: testImage, Cmd: []string{"sleep", "300"}, Labels: labels},
		Name:   "fluxor-orphan-test-" + uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		_, _ = dc.ContainerRemove(context.Background(), res.ID, dockerclient.ContainerRemoveOptions{Force: true}) // test cleanup; already gone when the reap worked
	})
	if _, err := dc.ContainerStart(ctx, res.ID, dockerclient.ContainerStartOptions{}); err != nil {
		t.Fatalf("start: %v", err)
	}
	return res.ID
}

func exists(t *testing.T, dc *dockerclient.Client, id string) bool {
	t.Helper()
	_, err := dc.ContainerInspect(context.Background(), id, dockerclient.ContainerInspectOptions{})
	return err == nil
}

// REL-22: a restart kills the workers but not their containers, and the
// re-sent task would run a second time beside the orphan.
func TestNewContainerExecutor_ReapsOrphanTaskContainersOfItsStack(t *testing.T) {
	dc, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dc.Ping(context.Background(), dockerclient.PingOptions{}); err != nil {
		t.Skipf("no docker daemon: %v", err)
	}
	if _, err := dc.ImageInspect(context.Background(), testImage); err != nil {
		t.Skipf("%s not present locally: %v", testImage, err)
	}

	ws := Workspace{Root: t.TempDir()}
	stack := ws.Root
	orphan := createSleeper(t, dc, map[string]string{"fluxor.task_exec_id": uuid.NewString(), "fluxor.stack": stack})
	otherStack := createSleeper(t, dc, map[string]string{"fluxor.task_exec_id": uuid.NewString(), "fluxor.stack": "another-stack"})
	unrelated := createSleeper(t, dc, map[string]string{"app": "not-fluxor"})

	if _, err := NewContainerExecutor(nil, ws); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for exists(t, dc, orphan) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if exists(t, dc, orphan) {
		t.Error("orphaned task container of this stack survived executor startup")
	}
	if !exists(t, dc, otherStack) {
		t.Error("task container of another stack was reaped")
	}
	if !exists(t, dc, unrelated) {
		t.Error("container without fluxor labels was reaped")
	}
}
