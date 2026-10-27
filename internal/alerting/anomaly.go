package alerting

import (
	"math"
	"sync"
)

// AnomalyDetector observes a time series one point at a time and reports
// whether the latest point looks anomalous, plus a numeric score whose
// magnitude indicates how far outside the normal range the point is
// (implementation-defined units, roughly "standard deviations").
type AnomalyDetector interface {
	Observe(t int64, v float64) (isAnomaly bool, score float64)
}

// ZScoreDetector flags points that fall more than Threshold standard
// deviations from the mean of a trailing sliding window of the last
// WindowSize observations. It requires at least MinSamples observations in
// the window before it will flag anything, to avoid noisy early alarms.
type ZScoreDetector struct {
	mu         sync.Mutex
	window     []float64
	windowSize int
	minSamples int
	threshold  float64
}

// NewZScoreDetector builds a rolling z-score detector over the given window
// size, flagging points beyond threshold standard deviations from the
// window mean. minSamples (clamped to >=2) is the minimum number of prior
// observations required before flagging begins.
func NewZScoreDetector(windowSize int, threshold float64) *ZScoreDetector {
	if windowSize < 2 {
		windowSize = 2
	}
	minSamples := windowSize
	if minSamples > 10 {
		minSamples = 10
	}
	if minSamples < 2 {
		minSamples = 2
	}
	return &ZScoreDetector{
		windowSize: windowSize,
		minSamples: minSamples,
		threshold:  threshold,
	}
}

// Observe implements AnomalyDetector. The z-score is computed against the
// window as it stood *before* v is added, so a single spike is measured
// against prior "normal" behavior rather than being partially absorbed into
// its own baseline.
func (z *ZScoreDetector) Observe(_ int64, v float64) (bool, float64) {
	z.mu.Lock()
	defer z.mu.Unlock()

	var isAnomaly bool
	var score float64
	if len(z.window) >= z.minSamples {
		mean, std := meanStd(z.window)
		if std > 0 {
			score = (v - mean) / std
		}
		isAnomaly = std > 0 && math.Abs(score) > z.threshold
	}

	z.window = append(z.window, v)
	if len(z.window) > z.windowSize {
		z.window = z.window[len(z.window)-z.windowSize:]
	}
	return isAnomaly, score
}

func meanStd(xs []float64) (mean, std float64) {
	n := float64(len(xs))
	if n == 0 {
		return 0, 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean = sum / n
	var sqSum float64
	for _, x := range xs {
		d := x - mean
		sqSum += d * d
	}
	std = math.Sqrt(sqSum / n)
	return mean, std
}

// EWMADetector flags points that deviate from an exponentially-weighted
// moving average by more than Threshold exponentially-weighted standard
// deviations. Unlike ZScoreDetector it needs no fixed window buffer -
// O(1) memory - and adapts its baseline over time, weighting recent points
// more heavily via Alpha (0,1].
type EWMADetector struct {
	mu          sync.Mutex
	alpha       float64
	threshold   float64
	ewma        float64
	ewmVariance float64
	initialized bool
	seen        int
	warmup      int
}

// NewEWMADetector builds an EWMA-based deviation detector. alpha controls
// how quickly the moving average adapts (higher = more reactive); threshold
// is the number of exponentially-weighted standard deviations beyond which
// a point is flagged anomalous. The detector requires a small warm-up
// period (a handful of observations) before it will flag anything, since
// its variance estimate is unreliable from a single sample - this mirrors
// ZScoreDetector's minSamples gate and avoids spurious alarms on cold
// start.
func NewEWMADetector(alpha, threshold float64) *EWMADetector {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.3
	}
	return &EWMADetector{alpha: alpha, threshold: threshold, warmup: 5}
}

// Observe implements AnomalyDetector.
func (e *EWMADetector) Observe(_ int64, v float64) (bool, float64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.initialized {
		e.ewma = v
		e.ewmVariance = 0
		e.initialized = true
		e.seen = 1
		return false, 0
	}

	diff := v - e.ewma
	e.seen++

	var score float64
	var isAnomaly bool
	if e.ewmVariance > 0 {
		std := math.Sqrt(e.ewmVariance)
		score = diff / std
		isAnomaly = e.seen > e.warmup && math.Abs(score) > e.threshold
	}

	// Update EWMA mean and EW variance (standard exponential smoothing
	// recurrence) using the *pre-update* diff, so the current point still
	// influences the baseline going forward.
	e.ewmVariance = (1 - e.alpha) * (e.ewmVariance + e.alpha*diff*diff)
	e.ewma += e.alpha * diff

	return isAnomaly, score
}
