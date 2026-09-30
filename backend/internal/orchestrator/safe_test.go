package orchestrator

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func TestRunSafe_RecoversPanic(t *testing.T) {
	var buf bytes.Buffer
	saved := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = saved })

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic escaped runSafe: %v", r)
		}
		out := buf.String()
		// A runtime.Error marshals to {} through Interface, so the message
		// must be logged as a string.
		for _, want := range []string{"exec-1", "index out of range [3] with length 0", `"stack"`} {
			if !strings.Contains(out, want) {
				t.Errorf("log %q does not contain %q", out, want)
			}
		}
	}()
	var s []int
	i := 3
	runSafe("dispatch", "exec-1", func() { _ = s[i] })
}
