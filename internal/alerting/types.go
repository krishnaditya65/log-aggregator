// Package alerting implements a v2 alerting engine: dynamic rule evaluation
// with Prometheus-style "for" duration gating, statistical anomaly
// detection, Alertmanager-style grouping and routing (Slack, PagerDuty,
// generic webhook), maintenance silencing, and deployment-correlated
// root-cause hints.
//
// It is intentionally decoupled from internal/alerts and internal/domain:
// rule evaluation is injected via a closure so this package never needs to
// know about the query language or the rest of the aggregator.
package alerting

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Rule is a v2 alert rule. It can represent either a static threshold rule
// or a dynamic expression-based rule; both are expressed the same way here
// because evaluation itself is delegated to Evaluator.
type Rule struct {
	ID          string
	Name        string
	Expr        string
	For         time.Duration
	Labels      map[string]string
	Annotations map[string]string
	Severity    string

	// Evaluator is invoked on every tick with the current logical time
	// (unix seconds). It returns the observed value, whether the rule
	// condition currently matches (is "breached"), and an error if
	// evaluation failed. This indirection is what lets internal/alerting
	// stay independent of internal/querylang.
	Evaluator func(now int64) (value float64, matched bool, err error)
}

// Alert is a single firing alert instance produced by rule evaluation.
type Alert struct {
	RuleID      string
	Name        string
	Labels      map[string]string
	Annotations map[string]string
	Severity    string
	Value       float64
	StartsAt    time.Time
}

// Fingerprint returns a stable identity for the alert based on its labels,
// used as part of grouping/silence matching.
func (a Alert) Fingerprint() string {
	return labelsKey(a.Labels)
}

// DeploymentEvent represents a deployment of a given service/version at a
// point in time, fed in from a CI/CD or deployment tracking system.
type DeploymentEvent struct {
	Service string
	Time    int64 // unix seconds
	Version string
}

// Silence suppresses notifications for alerts whose labels match Matchers,
// for the duration [StartsAt, EndsAt).
type Silence struct {
	ID        string
	Matchers  map[string]string
	StartsAt  time.Time
	EndsAt    time.Time
	CreatedBy string
	Comment   string
}

// active reports whether the silence is in effect at time t.
func (s Silence) active(t time.Time) bool {
	return !t.Before(s.StartsAt) && t.Before(s.EndsAt)
}

// Route is a node in the Alertmanager-like routing tree. The root Route
// typically has empty Matchers (matches everything) and acts as the
// default route; Children are matched depth-first, and by default the
// deepest matching child "wins" (its receiver is used instead of the
// parent's), unless Continue is set on the child in which case matching
// continues to sibling routes as well.
type Route struct {
	Matchers map[string]string
	Receiver string
	Continue bool
	Children []*Route
}

// matches reports whether all of the route's matchers are satisfied by the
// given label set (matchers are exact-match; empty matcher map matches
// everything).
func (r *Route) matches(labels map[string]string) bool {
	for k, v := range r.Matchers {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// resolveReceivers walks the routing tree starting at r and returns the
// ordered, de-duplicated list of receiver names that should be notified for
// the given label set.
func (r *Route) resolveReceivers(labels map[string]string) []string {
	if !r.matches(labels) {
		return nil
	}
	var receivers []string
	matchedChild := false
	for _, c := range r.Children {
		childReceivers := c.resolveReceivers(labels)
		if len(childReceivers) == 0 {
			continue
		}
		receivers = append(receivers, childReceivers...)
		matchedChild = true
		if !c.Continue {
			break
		}
	}
	if !matchedChild && r.Receiver != "" {
		receivers = append(receivers, r.Receiver)
	}
	return dedupe(receivers)
}

// NotifiedGroup is a batch of alerts that share the same group labels and
// receiver, produced by the Grouper and dispatched as a single
// notification.
type NotifiedGroup struct {
	GroupKey string
	Receiver string
	Alerts   []Alert
	Reason   string // e.g. "size_threshold" or "window_elapsed"
}

// Summary returns a short human-readable summary of the group, e.g. for use
// as a Slack/PagerDuty message title.
func (g NotifiedGroup) Summary() string {
	if len(g.Alerts) == 0 {
		return "no alerts"
	}
	if len(g.Alerts) == 1 {
		return g.Alerts[0].Name
	}
	names := make(map[string]int)
	for _, a := range g.Alerts {
		names[a.Name]++
	}
	var parts []string
	for n, c := range names {
		if c > 1 {
			parts = append(parts, fmt.Sprintf("%s (x%d)", n, c))
		} else {
			parts = append(parts, n)
		}
	}
	sort.Strings(parts)
	return fmt.Sprintf("%d alerts: %s", len(g.Alerts), strings.Join(parts, ", "))
}

// labelsKey returns a stable, order-independent string key for a label set.
func labelsKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
		b.WriteByte(',')
	}
	return b.String()
}

func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
