package aiops

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Anomaly is a detected metric anomaly on a service.
type Anomaly struct {
	Service string
	Metric  string
	Time    int64 // unix seconds
	Score   float64
}

// Deployment records a service version rollout.
type Deployment struct {
	Service string
	Time    int64 // unix seconds
	Version string
}

// LogSpike records an elevated log-message rate on a service.
type LogSpike struct {
	Service        string
	Time           int64 // unix seconds
	Count          int
	SampleMessages []string
}

// RootCauseHypothesis is a proposed explanation for an anomaly, with a
// confidence score in [0,1] and human-readable supporting Evidence.
type RootCauseHypothesis struct {
	Service    string
	Time       int64
	Confidence float64
	Evidence   []string
}

// Correlate produces root-cause hypotheses for each anomaly by looking
// for the nearest preceding deployment on the same service within
// `window`, and any log spikes on the same (or a heuristically related)
// service within `window` of the anomaly. See the package doc comment for
// the scoring model.
//
// One RootCauseHypothesis is emitted per input Anomaly that has at least
// one piece of supporting evidence (a candidate deploy and/or a log
// spike); anomalies with no correlated signal within the window are
// omitted rather than emitted with zero confidence. Results are sorted by
// descending confidence, then by Service/Time for determinism.
func Correlate(anomalies []Anomaly, deploys []Deployment, spikes []LogSpike, window time.Duration) []RootCauseHypothesis {
	if window <= 0 {
		window = 15 * time.Minute
	}
	windowSecs := window.Seconds()

	var hypotheses []RootCauseHypothesis

	for _, an := range anomalies {
		var evidence []string
		var confidence float64

		evidence = append(evidence, fmt.Sprintf(
			"anomaly on service %q metric %q at %s (score=%.2f)",
			an.Service, an.Metric, formatTime(an.Time), an.Score,
		))

		if dep, dist, ok := nearestPriorDeploy(an, deploys, windowSecs); ok {
			decay := deployConfidenceDecay(dist, windowSecs)
			confidence += decay
			evidence = append(evidence, fmt.Sprintf(
				"deploy %s of %q preceded the anomaly by %s (confidence contribution %.2f)",
				dep.Version, dep.Service, formatDuration(dist), decay,
			))
		}

		spikeEvidence, spikeBoost := correlatedLogSpikes(an, spikes, windowSecs)
		if len(spikeEvidence) > 0 {
			confidence += spikeBoost
			evidence = append(evidence, spikeEvidence...)
		}

		if len(evidence) <= 1 {
			// Only the anomaly-itself line — no correlated signal found.
			continue
		}

		if confidence > 1 {
			confidence = 1
		}
		hypotheses = append(hypotheses, RootCauseHypothesis{
			Service:    an.Service,
			Time:       an.Time,
			Confidence: round2(confidence),
			Evidence:   evidence,
		})
	}

	sort.Slice(hypotheses, func(i, j int) bool {
		if hypotheses[i].Confidence != hypotheses[j].Confidence {
			return hypotheses[i].Confidence > hypotheses[j].Confidence
		}
		if hypotheses[i].Service != hypotheses[j].Service {
			return hypotheses[i].Service < hypotheses[j].Service
		}
		return hypotheses[i].Time < hypotheses[j].Time
	})

	return hypotheses
}

// nearestPriorDeploy finds the deployment on the same service as an with
// the smallest non-negative time distance (deploy.Time <= an.Time) within
// windowSecs. Returns ok=false if none qualifies.
func nearestPriorDeploy(an Anomaly, deploys []Deployment, windowSecs float64) (Deployment, float64, bool) {
	var best Deployment
	bestDist := math.MaxFloat64
	found := false

	for _, d := range deploys {
		if d.Service != an.Service {
			continue
		}
		dist := float64(an.Time - d.Time)
		if dist < 0 {
			continue // deploy happened after the anomaly; not a cause
		}
		if dist > windowSecs {
			continue
		}
		if dist < bestDist {
			bestDist = dist
			best = d
			found = true
		}
	}
	return best, bestDist, found
}

// deployConfidenceDecay maps a deploy->anomaly time distance to a
// confidence contribution in (0, 0.7], decaying exponentially with the
// fraction of the window elapsed: contributions are highest for deploys
// immediately preceding the anomaly and fall off the further back (within
// the window) the deploy was. Base weight 0.7 leaves room for log-spike
// evidence to push overall confidence toward 1.0 without a deploy alone
// ever claiming full certainty.
func deployConfidenceDecay(distSecs, windowSecs float64) float64 {
	const baseWeight = 0.7
	if windowSecs <= 0 {
		return 0
	}
	fraction := distSecs / windowSecs
	return baseWeight * math.Exp(-3*fraction)
}

// correlatedLogSpikes finds log spikes within windowSecs of the anomaly's
// time on the same or a heuristically-related service, returning
// human-readable evidence lines and a total confidence boost.
func correlatedLogSpikes(an Anomaly, spikes []LogSpike, windowSecs float64) ([]string, float64) {
	var evidence []string
	var boost float64

	for _, sp := range spikes {
		if !isRelatedService(an.Service, sp.Service) {
			continue
		}
		dist := math.Abs(float64(an.Time - sp.Time))
		if dist > windowSecs {
			continue
		}

		relation := "same service"
		if sp.Service != an.Service {
			relation = "related service"
		}
		weight := 0.3 * (1 - dist/windowSecs) // closer in time => stronger
		if weight < 0 {
			weight = 0
		}
		boost += weight

		sample := ""
		if len(sp.SampleMessages) > 0 {
			sample = fmt.Sprintf(", e.g. %q", sp.SampleMessages[0])
		}
		evidence = append(evidence, fmt.Sprintf(
			"log spike of %d messages on %s %q within %s of the anomaly (confidence contribution %.2f)%s",
			sp.Count, relation, sp.Service, formatDuration(dist), weight, sample,
		))
	}
	return evidence, boost
}

// isRelatedService reports whether b is the same service as a, or a
// heuristically "dependent" service. This package has no real service
// topology graph; as a documented, simple stand-in it treats services
// sharing a hyphen-delimited prefix (e.g. "payment-api" and
// "payment-db") as related.
func isRelatedService(a, b string) bool {
	if a == b {
		return true
	}
	aPrefix, aOK := prefixBeforeDash(a)
	bPrefix, bOK := prefixBeforeDash(b)
	return aOK && bOK && aPrefix == bPrefix
}

func prefixBeforeDash(s string) (string, bool) {
	i := strings.Index(s, "-")
	if i <= 0 {
		return "", false
	}
	return s[:i], true
}

func formatDuration(secs float64) string {
	return time.Duration(secs * float64(time.Second)).Round(time.Second).String()
}

func formatTime(unixSecs int64) string {
	return time.Unix(unixSecs, 0).UTC().Format(time.RFC3339)
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
