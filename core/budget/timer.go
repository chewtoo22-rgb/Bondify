package budget

import "time"

func newTimer(d time.Duration) *time.Timer {
	return time.NewTimer(d)
}
