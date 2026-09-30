package worker

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/moby/moby/api/pkg/stdcopy"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

const (
	// DefaultMaxTaskOutputBytes caps a task's output and any response body it fetches.
	DefaultMaxTaskOutputBytes int64 = 1 << 20
	// DefaultMaxTaskLogBytes caps the log entries of a single attempt.
	DefaultMaxTaskLogBytes int64 = 1 << 20

	// maxErrorBodyBytes bounds how much of a remote reply may reach a task error,
	// which is stored, shown in the UI and can be retried many times.
	maxErrorBodyBytes = 4 << 10

	truncationLine = "\n[output truncated]"
)

// Limits bound every buffer a task can fill. The zero value means the defaults.
type Limits struct {
	OutputBytes int64
	LogBytes    int64
}

// LimitsFromEnv reads the limits from the environment. A set but invalid value
// is an error so a typo cannot silently lift a cap.
func LimitsFromEnv() (Limits, error) {
	out, err := bytesFromEnv("FLUXOR_MAX_TASK_OUTPUT_BYTES", DefaultMaxTaskOutputBytes)
	if err != nil {
		return Limits{}, err
	}
	logs, err := bytesFromEnv("FLUXOR_MAX_TASK_LOG_BYTES", DefaultMaxTaskLogBytes)
	if err != nil {
		return Limits{}, err
	}
	return Limits{OutputBytes: out, LogBytes: logs}, nil
}

func bytesFromEnv(name string, def int64) (int64, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s=%q: want a positive byte count", name, v)
	}
	return n, nil
}

func (l Limits) output() int64 {
	if l.OutputBytes <= 0 {
		return DefaultMaxTaskOutputBytes
	}
	return l.OutputBytes
}

func (l Limits) logs() int64 {
	if l.LogBytes <= 0 {
		return DefaultMaxTaskLogBytes
	}
	return l.LogBytes
}

// readCapped reads at most max bytes. It asks the reader for one more byte so
// truncated is exact, and never reads the rest of an oversized body.
func readCapped(r io.Reader, max int64) (data []byte, truncated bool, err error) {
	data, err = io.ReadAll(io.LimitReader(r, max+1))
	if int64(len(data)) > max {
		return data[:max], true, err
	}
	return data, false, err
}

// remoteBody returns the start of a remote reply for use in an error message.
func remoteBody(r io.Reader) string {
	// Best effort: the status code already decides the outcome, and a read
	// error only shortens the detail.
	data, _, _ := readCapped(r, maxErrorBodyBytes)
	return string(data)
}

// boundedLogs collects one attempt's log entries up to a byte budget.
type boundedLogs struct {
	entries []models.LogEntry
	max     int64
	size    int64
	full    bool
}

func newBoundedLogs(max int64) *boundedLogs { return &boundedLogs{max: max} }

// add returns the entries to broadcast for e: none once the budget is spent.
// The notice that ends the log is stored beyond the budget, so a cap of N keeps
// N bytes of entries plus one short line.
func (b *boundedLogs) add(e models.LogEntry) []models.LogEntry {
	if b.full {
		return nil
	}
	// Estimate: the fields are stored as JSON, which is about as long as their print form.
	b.size += int64(len(e.Message) + len(fmt.Sprint(e.Fields)))
	if b.size > b.max {
		b.full = true
		notice := models.LogEntry{Timestamp: e.Timestamp, Level: "warn", Attempt: e.Attempt, Message: "task log truncated: attempt exceeded FLUXOR_MAX_TASK_LOG_BYTES"}
		b.entries = append(b.entries, notice)
		return []models.LogEntry{notice}
	}
	b.entries = append(b.entries, e)
	return []models.LogEntry{e}
}

var errLogCap = errors.New("log cap reached")

// sharedCapWriter writes into w until the budget shared by its siblings is spent.
type sharedCapWriter struct {
	w    *bytes.Buffer
	left *int64
}

func (c sharedCapWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > *c.left {
		n := int(*c.left)
		c.w.Write(p[:n])
		*c.left = 0
		return n, errLogCap
	}
	*c.left -= int64(len(p))
	return c.w.Write(p)
}

// readContainerLogs demultiplexes a Docker log stream into one string of at
// most max bytes, stopping the read once the budget is spent.
func readContainerLogs(r io.Reader, max int64) (string, error) {
	left := max - int64(len(truncationLine))
	var outBuf, errBuf bytes.Buffer
	_, err := stdcopy.StdCopy(sharedCapWriter{&outBuf, &left}, sharedCapWriter{&errBuf, &left}, r)
	truncated := errors.Is(err, errLogCap)
	// Any other error (e.g. a TTY stream without frame headers) keeps what was read.
	combined := outBuf.String()
	if errBuf.Len() > 0 {
		if combined != "" {
			combined += "\n"
		}
		combined += errBuf.String()
	}
	if truncated {
		combined += truncationLine
	}
	return combined, nil
}
