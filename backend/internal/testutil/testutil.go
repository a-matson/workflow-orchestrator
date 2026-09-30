//go:build integration

// Package testutil gives integration tests an isolated Postgres database and
// Redis DB per test, plus polling and fixture helpers.
package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/orchestrator"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/migrations"
)

const (
	pollInterval = 10 * time.Millisecond
	drainTimeout = 5 * time.Second
	// Leases are released in t.Cleanup; the TTL only frees the index of a
	// process that crashed. It matches `go test`'s default 10m timeout so a
	// live test cannot outlast its own lease.
	leaseTTL = 10 * time.Minute
	leaseKey = "fluxor:testutil:db:%d"
)

var (
	// The broker uses fixed key names (persistence.TaskQueueKey, ...), so the
	// only way to keep test binaries apart is a separate Redis DB index.
	// `go test ./...` runs packages as parallel processes, so every Env call
	// leases a free index (SET NX in DB 0), FLUSHDBs it, and releases it when
	// the test ends. Env refuses concurrent use within a process so a process
	// never holds more than one lease.
	envMu sync.Mutex

	// Deletes the lease only if this test still owns it, so a lease that
	// expired and was taken by another process is left alone.
	releaseLease = redis.NewScript(`if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) end return 0`)

	rawMu sync.Mutex
	raw   = map[*persistence.RedisClient]*redis.Client{}
)

// Env returns a store on a fresh, migrated Postgres database and a Redis
// client on an empty DB, both torn down when t ends. Tests calling Env must
// not run in parallel with each other (call t.Parallel only in tests that do
// not use Env). Missing infrastructure skips the test, or fails it when
// FLUXOR_IT=1.
func Env(t *testing.T) (*persistence.Store, *persistence.RedisClient) {
	t.Helper()
	if !envMu.TryLock() {
		t.Fatal("testutil.Env: already in use by another test in this package; Env tests share one Redis DB and cannot run in parallel")
	}
	t.Cleanup(envMu.Unlock)

	return newStore(t), newRedis(t)
}

func unavailable(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("FLUXOR_IT") == "1" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

func newStore(t *testing.T) *persistence.Store {
	t.Helper()
	ctx := context.Background()
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		unavailable(t, "POSTGRES_URL not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse POSTGRES_URL: %v", err)
	}

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		unavailable(t, "postgres unavailable: %v", err)
	}
	name := "fluxor_it_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		if cerr := admin.Close(ctx); cerr != nil {
			t.Errorf("close admin connection: %v", cerr)
		}
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		if err := admin.Close(ctx); err != nil {
			t.Errorf("close admin connection: %v", err)
		}
	})

	u.Path = "/" + name
	store, err := persistence.NewStore(ctx, u.String())
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(store.Close)
	if err := persistence.Migrate(ctx, store.Pool(), migrations.FS); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return store
}

func newRedis(t *testing.T) *persistence.RedisClient {
	t.Helper()
	ctx := context.Background()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		unavailable(t, "REDIS_ADDR not set")
	}

	db := leaseDB(t, addr)
	rawClient := redis.NewClient(&redis.Options{Addr: addr, DB: db})
	t.Cleanup(func() {
		if err := rawClient.Close(); err != nil {
			t.Errorf("close raw redis client: %v", err)
		}
	})
	if err := rawClient.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush redis db %d: %v", db, err)
	}

	client := persistence.NewRedisClient(addr, "", db)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close redis client: %v", err)
		}
	})
	rawMu.Lock()
	raw[client] = rawClient
	rawMu.Unlock()
	t.Cleanup(func() {
		rawMu.Lock()
		delete(raw, client)
		rawMu.Unlock()
	})
	return client
}

// leaseDB leases a free Redis DB index for the rest of t.
func leaseDB(t *testing.T, addr string) int {
	t.Helper()
	ctx := context.Background()
	admin := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() {
		if err := admin.Close(); err != nil {
			t.Errorf("close redis lease client: %v", err)
		}
	})
	if err := admin.Ping(ctx).Err(); err != nil {
		unavailable(t, "redis unavailable: %v", err)
	}

	owner := uuid.NewString()
	// DB 0 holds the leases, so tests get 1..15 (Redis defaults to 16 DBs).
	for db := 1; db < 16; db++ {
		key := fmt.Sprintf(leaseKey, db)
		ok, err := admin.SetNX(ctx, key, owner, leaseTTL).Result()
		if err != nil {
			t.Fatalf("lease redis db: %v", err)
		}
		if ok {
			t.Cleanup(func() {
				if err := releaseLease.Run(ctx, admin, []string{key}, owner).Err(); err != nil {
					t.Errorf("release redis db lease %d: %v", db, err)
				}
			})
			return db
		}
	}
	t.Fatalf("no free redis db index: 15 leases held (a crashed process's lease expires after %s)", leaseTTL)
	return 0
}

// SaveDef persists def so executions can reference it.
func SaveDef(t *testing.T, s *persistence.Store, def *models.WorkflowDefinition) {
	t.Helper()
	if err := s.SaveWorkflowDefinition(context.Background(), def); err != nil {
		t.Fatalf("save workflow definition %s: %v", def.ID, err)
	}
}

// Drain dequeues exactly n task messages, failing t if they do not all
// arrive within 5s.
func Drain(t *testing.T, r *persistence.RedisClient, n int) []*models.TaskMessage {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(drainTimeout)
	msgs := make([]*models.TaskMessage, 0, n)
	for len(msgs) < n {
		if time.Now().After(deadline) {
			t.Fatalf("drained %d of %d task messages within %s", len(msgs), n, drainTimeout)
		}
		// One second is the smallest BRPOP timeout Redis supports.
		msg, err := r.DequeueTask(ctx, time.Second)
		if err != nil {
			t.Fatalf("dequeue task: %v", err)
		}
		if msg != nil {
			msgs = append(msgs, msg)
		}
	}
	return msgs
}

// Queued counts the task messages for execID waiting in the task queue.
func Queued(t *testing.T, r *persistence.RedisClient, execID string) int {
	t.Helper()
	rawMu.Lock()
	rawClient, ok := raw[r]
	rawMu.Unlock()
	if !ok {
		t.Fatal("testutil.Queued: client was not created by testutil.Env")
	}

	items, err := rawClient.LRange(context.Background(), persistence.TaskQueueKey, 0, -1).Result()
	if err != nil {
		t.Fatalf("list task queue: %v", err)
	}
	n := 0
	for _, item := range items {
		var msg models.TaskMessage
		if err := json.Unmarshal([]byte(item), &msg); err != nil {
			t.Fatalf("decode queued task: %v", err)
		}
		if msg.WorkflowExecID == execID {
			n++
		}
	}
	return n
}

// Eventually fails t unless cond returns true within d.
func Eventually(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s", d)
		}
		time.Sleep(pollInterval)
	}
}

// Never fails t if cond returns true at any point during d.
func Never(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			t.Fatalf("condition became true within %s", d)
		}
		time.Sleep(pollInterval)
	}
}

// TaskRow returns the task execution of execID for task definition defID.
func TaskRow(t *testing.T, s *persistence.Store, execID, defID string) *models.TaskExecution {
	t.Helper()
	tasks, err := s.ListTaskExecutions(context.Background(), execID)
	if err != nil {
		t.Fatalf("list task executions of %s: %v", execID, err)
	}
	for _, task := range tasks {
		if task.TaskDefinitionID == defID {
			return task
		}
	}
	t.Fatalf("no task execution for definition %q in execution %s", defID, execID)
	return nil
}

// Ok is the successful result a worker would publish for m.
func Ok(m *models.TaskMessage) *models.TaskResult {
	return result(m, true, "")
}

// Fail is the failed result a worker would publish for m.
func Fail(m *models.TaskMessage, msg string) *models.TaskResult {
	return result(m, false, msg)
}

func result(m *models.TaskMessage, success bool, errMsg string) *models.TaskResult {
	now := time.Now()
	return &models.TaskResult{
		TaskExecID:     m.TaskExecID,
		WorkflowExecID: m.WorkflowExecID,
		WorkerID:       "testutil",
		RetryCount:     &m.RetryCount,
		Success:        success,
		Error:          errMsg,
		StartedAt:      now,
		CompletedAt:    now,
	}
}

// Recorder is an orchestrator.EventBroadcaster that keeps every event.
// The zero value is ready to use.
type Recorder struct {
	mu     sync.Mutex
	events []models.WebSocketEvent
}

var _ orchestrator.EventBroadcaster = (*Recorder)(nil)

// Broadcast records event.
func (r *Recorder) Broadcast(event models.WebSocketEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

// Events returns a copy of the events recorded so far, oldest first.
func (r *Recorder) Events() []models.WebSocketEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]models.WebSocketEvent(nil), r.events...)
}
