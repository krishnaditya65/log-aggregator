package tsdb

import "sync"

// rolledUpValue replaces high-cardinality label values once a label name's
// cardinality budget is exhausted.
const rolledUpValue = "_other_"

// CardinalityLimiter tracks, per label name, the set of distinct values
// observed so far and caps runaway cardinality (e.g. a session_id or
// request_id label that would otherwise create one series per value,
// blowing up the inverted index). Once a label name's distinct-value count
// reaches Threshold, any *new* value for that label is rewritten to
// "_other_" before being admitted to the write path; values already seen
// keep passing through unchanged so existing series continue to
// accumulate points rather than fragmenting further.
type CardinalityLimiter struct {
	mu        sync.Mutex
	threshold int
	seen      map[string]map[string]struct{} // label name -> known values
	rollups   map[string]int64               // label name -> rollup count (metric)
	total     int64                          // total rollups across all labels
}

// NewCardinalityLimiter creates a limiter that allows up to `threshold`
// distinct values per label name before rolling up new values.
func NewCardinalityLimiter(threshold int) *CardinalityLimiter {
	if threshold <= 0 {
		threshold = 1
	}
	return &CardinalityLimiter{
		threshold: threshold,
		seen:      make(map[string]map[string]struct{}),
		rollups:   make(map[string]int64),
	}
}

// Admit inspects labels and returns a (possibly modified) copy where any
// label whose name has exceeded the cardinality threshold has had its
// value generalized to "_other_" (unless the value was already known,
// i.e. it doesn't grow cardinality further). The input map is never
// mutated.
func (c *CardinalityLimiter) Admit(labels map[string]string) map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make(map[string]string, len(labels))
	for name, value := range labels {
		values, ok := c.seen[name]
		if !ok {
			values = make(map[string]struct{})
			c.seen[name] = values
		}

		if _, known := values[value]; known {
			out[name] = value
			continue
		}

		if len(values) >= c.threshold {
			// Cardinality budget exhausted for this label name:
			// generalize the new/unseen value instead of admitting
			// it, capping further index growth.
			out[name] = rolledUpValue
			c.rollups[name]++
			c.total++
			// Ensure the rolled-up sentinel itself is tracked so it
			// doesn't count against the budget repeatedly.
			values[rolledUpValue] = struct{}{}
			continue
		}

		values[value] = struct{}{}
		out[name] = value
	}
	return out
}

// Cardinality returns the number of distinct values currently tracked for
// a label name (including the "_other_" rollup bucket, if used).
func (c *CardinalityLimiter) Cardinality(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen[name])
}

// RollupCount returns how many values have been rolled up for a given
// label name.
func (c *CardinalityLimiter) RollupCount(name string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rollups[name]
}

// TotalRollups returns the total number of rollups performed across all
// label names, useful as a single health metric.
func (c *CardinalityLimiter) TotalRollups() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}
