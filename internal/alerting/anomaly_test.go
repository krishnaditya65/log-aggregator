package alerting

import "testing"

// syntheticSeriesWithSpike returns n baseline points hovering around
// `base` with small noise-like variation (deterministic, no randomness so
// the test is reproducible), plus one big spike injected at spikeIdx.
func syntheticSeriesWithSpike(n int, base, noiseAmplitude, spikeIdx, spikeValue float64) []float64 {
	series := make([]float64, n)
	for i := 0; i < n; i++ {
		// deterministic pseudo-noise via a simple oscillation
		offset := noiseAmplitude * sinApprox(float64(i))
		series[i] = base + offset
	}
	idx := int(spikeIdx)
	if idx >= 0 && idx < n {
		series[idx] = spikeValue
	}
	return series
}

// sinApprox is a tiny deterministic oscillator (not math.Sin to avoid
// importing math here beyond what's needed) producing values in [-1,1].
func sinApprox(x float64) float64 {
	// triangle wave period 8, amplitude 1 - deterministic "noise".
	period := 8.0
	p := mod(x, period)
	if p < period/2 {
		return -1 + 4*p/period
	}
	return 3 - 4*p/period
}

func mod(a, b float64) float64 {
	for a < 0 {
		a += b
	}
	for a >= b {
		a -= b
	}
	return a
}

func TestZScoreDetectorFlagsInjectedSpike(t *testing.T) {
	series := syntheticSeriesWithSpike(30, 100, 1.5, 20, 500) // spike far outside baseline
	det := NewZScoreDetector(10, 3.0)

	var flaggedAt = -1
	var flaggedScore float64
	for i, v := range series {
		isAnomaly, score := det.Observe(int64(i), v)
		if isAnomaly {
			flaggedAt = i
			flaggedScore = score
		}
	}

	if flaggedAt != 20 {
		t.Fatalf("expected spike at index 20 to be flagged, but flagged index was %d", flaggedAt)
	}
	if flaggedScore < 3.0 {
		t.Fatalf("expected flagged score >= threshold 3.0, got %f", flaggedScore)
	}
}

func TestZScoreDetectorNoFalsePositivesOnBaseline(t *testing.T) {
	series := syntheticSeriesWithSpike(40, 100, 1.5, -1, 0) // no spike
	det := NewZScoreDetector(10, 3.0)

	for i, v := range series {
		isAnomaly, _ := det.Observe(int64(i), v)
		if isAnomaly {
			t.Fatalf("unexpected anomaly flagged at index %d on pure baseline data", i)
		}
	}
}

func TestEWMADetectorFlagsInjectedSpike(t *testing.T) {
	series := syntheticSeriesWithSpike(30, 100, 1.0, 20, 400)
	det := NewEWMADetector(0.3, 3.0)

	var flaggedAt = -1
	for i, v := range series {
		isAnomaly, score := det.Observe(int64(i), v)
		if isAnomaly {
			flaggedAt = i
			if score < 3.0 && score > -3.0 {
				t.Fatalf("flagged anomaly but score %f does not exceed threshold", score)
			}
		}
	}

	if flaggedAt != 20 {
		t.Fatalf("expected spike at index 20 to be flagged, but flagged index was %d", flaggedAt)
	}
}

func TestEWMADetectorNoFalsePositivesOnBaseline(t *testing.T) {
	// Use a smaller, smoother noise amplitude here: EWMA's variance
	// estimate needs a couple of points to warm up, and a large abrupt
	// first step relative to a near-zero initial variance estimate would
	// otherwise look anomalous by construction, independent of the
	// detector's correctness.
	series := syntheticSeriesWithSpike(40, 100, 0.3, -1, 0)
	det := NewEWMADetector(0.3, 3.0)

	for i, v := range series {
		isAnomaly, _ := det.Observe(int64(i), v)
		if isAnomaly {
			t.Fatalf("unexpected anomaly flagged at index %d on pure baseline data", i)
		}
	}
}
