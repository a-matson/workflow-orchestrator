package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dockerclient "github.com/moby/moby/client"
)

// The socket proxy cuts an idle /wait after sending its headers; the executor
// must wait again rather than fail a long-running task.
func TestWaitExit_RewaitsAfterProxyCut(t *testing.T) {
	var waits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/wait") {
			http.NotFound(w, r)
			return
		}
		if waits.Add(1) == 1 {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				// Why ignored: the point is an abrupt close; its error changes nothing.
				_ = conn.Close()
			}
			return
		}
		_, _ = w.Write([]byte(`{"StatusCode":3}`))
	}))
	defer srv.Close()

	dc, err := dockerclient.New(dockerclient.WithHost("tcp://"+srv.Listener.Addr().String()), dockerclient.WithAPIVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	ce := &ContainerExecutor{docker: dc}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	code, err := ce.waitExit(ctx, "abc123")
	if err != nil {
		t.Fatalf("waitExit: %v", err)
	}
	if code != 3 || waits.Load() != 2 {
		t.Fatalf("code=%d waits=%d, want 3 after 2 waits", code, waits.Load())
	}
}
