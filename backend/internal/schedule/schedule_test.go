package schedule

import (
	"testing"
	"time"
)

func TestNext(t *testing.T) {
	at := time.Date(2026, 10, 1, 1, 30, 0, 0, time.UTC)
	for expr, want := range map[string]time.Time{
		"0 2 * * *":    time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC),
		"*/15 * * * *": time.Date(2026, 10, 1, 1, 45, 0, 0, time.UTC),
		"@hourly":      time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC),
	} {
		got, err := Next(expr, at)
		if err != nil || !got.Equal(want) {
			t.Errorf("Next(%q) = %s, %v; want %s", expr, got, err, want)
		}
	}
	// A non-UTC input is evaluated in UTC.
	if got, _ := Next("0 2 * * *", at.In(time.FixedZone("x", 3600))); !got.Equal(time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)) {
		t.Errorf("Next in another zone = %s", got)
	}
	for _, bad := range []string{"", "every day", "61 * * * *", "* * * *"} {
		if _, err := Next(bad, at); err == nil {
			t.Errorf("Next(%q) accepted", bad)
		}
	}
}
