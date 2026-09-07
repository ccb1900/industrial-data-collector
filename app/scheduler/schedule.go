package scheduler

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DailyAnchor returns the most recent HH:MM instant before or equal to now.
// The runtime scheduler extension anchors daily work on a fixed theoretical
// timeline, so this is the anchor whose next tick is the requested local time.
func DailyAnchor(clock string, now time.Time) (time.Time, error) {
	parts := strings.Split(clock, ":")
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("scheduler time must be HH:MM")
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return time.Time{}, fmt.Errorf("scheduler hour is invalid")
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return time.Time{}, fmt.Errorf("scheduler minute is invalid")
	}
	daily := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !now.Before(daily) {
		return daily, nil
	}
	return daily.AddDate(0, 0, -1), nil
}
