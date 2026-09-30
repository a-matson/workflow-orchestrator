package worker

import (
	"bytes"
	"io"

	"github.com/moby/moby/api/pkg/stdcopy"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

// Stubs: bounds are added in the fix commit.

// boundedLogs collects one attempt's log entries.
type boundedLogs struct {
	entries []models.LogEntry
}

func newBoundedLogs(max int64) *boundedLogs { return &boundedLogs{} }

// add returns the entries to broadcast for e.
func (b *boundedLogs) add(e models.LogEntry) []models.LogEntry {
	b.entries = append(b.entries, e)
	return []models.LogEntry{e}
}

// readContainerLogs demultiplexes a Docker log stream into one string.
func readContainerLogs(r io.Reader, max int64) (string, error) {
	var outBuf, errBuf bytes.Buffer
	if _, err := stdcopy.StdCopy(&outBuf, &errBuf, r); err != nil {
		return outBuf.String() + errBuf.String(), nil
	}
	combined := outBuf.String()
	if errBuf.Len() > 0 {
		if combined != "" {
			combined += "\n"
		}
		combined += errBuf.String()
	}
	return combined, nil
}
