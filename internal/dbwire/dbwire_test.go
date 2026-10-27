package dbwire

import "time"

// fixedTime returns a deterministic time.Time offset by nanos from a
// fixed epoch, used across protocol tests to compute exact latencies.
func fixedTime(nanos int64) time.Time {
	return time.Unix(0, nanos)
}
