package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/egress"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

func countingServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		h(rw, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func runHTTPTask(t *testing.T, allow, url string) error {
	t.Helper()
	g, err := egress.New(allow)
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{httpClient: g.HTTPClient(5 * time.Second)}
	msg := &models.TaskMessage{TaskType: "http_request", Config: map[string]any{"url": url}}
	_, _, err = w.dispatch(context.Background(), msg, func(string, string, map[string]any) {})
	return err
}

func TestExecHTTP_BlocksLoopback(t *testing.T) {
	srv, hits := countingServer(t, func(http.ResponseWriter, *http.Request) {})

	err := runHTTPTask(t, "", srv.URL+"/secret?token=abc")

	if !errors.Is(err, egress.ErrEgressDenied) {
		t.Errorf("err = %v, want ErrEgressDenied", err)
	}
	if err != nil && strings.Contains(err.Error(), "token=abc") {
		t.Errorf("error leaks the URL query: %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("loopback server saw %d requests, want 0", n)
	}
}

func TestExecHTTP_BlocksRedirectToPrivate(t *testing.T) {
	internal, internalHits := countingServer(t, func(http.ResponseWriter, *http.Request) {})
	front, _ := countingServer(t, func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, internal.URL+"/", http.StatusFound)
	})

	// Only the first hop is allowlisted, so the redirect target must be denied.
	err := runHTTPTask(t, strings.TrimPrefix(front.URL, "http://"), front.URL)

	if !errors.Is(err, egress.ErrEgressDenied) {
		t.Errorf("err = %v, want ErrEgressDenied", err)
	}
	if n := internalHits.Load(); n != 0 {
		t.Errorf("redirect target saw %d requests, want 0", n)
	}
}

// The guard must check the resolved IP, not the URL host: a name that
// resolves to loopback is as internal as 127.0.0.1.
func TestExecHTTP_BlocksHostnameResolvingToLoopback(t *testing.T) {
	srv, hits := countingServer(t, func(http.ResponseWriter, *http.Request) {})
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	err = runHTTPTask(t, "", "http://localhost:"+u.Port()+"/")

	if !errors.Is(err, egress.ErrEgressDenied) {
		t.Errorf("err = %v, want ErrEgressDenied", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("loopback server saw %d requests, want 0", n)
	}
}

// A parse error from http.NewRequest also repeats the URL, secret included.
func TestExecHTTP_MalformedURLErrorOmitsURL(t *testing.T) {
	err := runHTTPTask(t, "", "https://hooks.example.com/%zz?token=abc")

	if err == nil || strings.Contains(err.Error(), "token=abc") {
		t.Errorf("err = %v, want an error without the URL", err)
	}
}
