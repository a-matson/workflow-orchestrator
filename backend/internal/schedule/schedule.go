// Package schedule computes when a workflow's cron schedule next fires.
// Expressions use the standard five fields (minute hour day-of-month month
// day-of-week) or a descriptor such as @hourly, evaluated in UTC.
package schedule

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// Next returns the first time after `after` that expr fires.
func Next(expr string, after time.Time) (time.Time, error) {
	s, err := cron.ParseStandard(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("schedule %q: %w", expr, err)
	}
	return s.Next(after.UTC()), nil
}
