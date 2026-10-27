package alerting

import (
	"context"
	"sync"
	"time"
)

// ruleBreachState tracks how long a rule has been continuously breached, to
// implement Prometheus-style "for" duration gating.
type ruleBreachState struct {
	firstBreach int64 // unix seconds; 0 means "not currently breached"
	firing      bool
}

// Engine wires together rules, anomaly detectors, the grouper, router,
// silencer and deploy-correlator into a single evaluation pipeline.
// Evaluate is the tick entrypoint: call it periodically (e.g. every 15s)
// with the current logical time.
type Engine struct {
	mu sync.Mutex

	rules      []*Rule
	ruleState  map[string]*ruleBreachState
	detectors  map[string]AnomalyDetector
	grouper    *Grouper
	router     *Router
	silencer   *Silencer
	correlator *DeployCorrelator
}

// NewEngine builds an Engine from its component parts. grouper, router,
// silencer and correlator may be nil, in which case grouping/routing,
// silencing, and deploy-correlation are skipped respectively (useful for
// testing pieces in isolation).
func NewEngine(grouper *Grouper, router *Router, silencer *Silencer, correlator *DeployCorrelator) *Engine {
	return &Engine{
		ruleState:  make(map[string]*ruleBreachState),
		detectors:  make(map[string]AnomalyDetector),
		grouper:    grouper,
		router:     router,
		silencer:   silencer,
		correlator: correlator,
	}
}

// AddRule registers a rule with the engine.
func (e *Engine) AddRule(r *Rule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = append(e.rules, r)
	if _, ok := e.ruleState[r.ID]; !ok {
		e.ruleState[r.ID] = &ruleBreachState{}
	}
}

// AddAnomalyDetector registers a named anomaly detector (e.g. per metric
// series) with the engine, so it can be driven via ObserveMetric.
func (e *Engine) AddAnomalyDetector(name string, d AnomalyDetector) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.detectors[name] = d
}

// ObserveMetric feeds a data point to the named anomaly detector, if
// registered, and reports whether it was flagged anomalous.
func (e *Engine) ObserveMetric(name string, t int64, v float64) (isAnomaly bool, score float64) {
	e.mu.Lock()
	d, ok := e.detectors[name]
	e.mu.Unlock()
	if !ok {
		return false, 0
	}
	return d.Observe(t, v)
}

// AddDeployment records a deployment event with the engine's correlator, if
// configured.
func (e *Engine) AddDeployment(evt DeploymentEvent) {
	if e.correlator != nil {
		e.correlator.AddDeployment(evt)
	}
}

// AddSilence registers a maintenance silence, if a silencer is configured.
func (e *Engine) AddSilence(s Silence) {
	if e.silencer != nil {
		e.silencer.Add(s)
	}
}

// Evaluate is the tick entrypoint. It evaluates every registered rule at
// logical time `now` (unix seconds), applies "for" duration gating,
// attaches deploy-correlation hints, filters out silenced alerts, enqueues
// the rest into the grouper, flushes any groups whose window/size trigger
// has been reached, and dispatches them via the router. It returns the
// groups that were flushed and dispatched on this tick.
func (e *Engine) Evaluate(now int64) []NotifiedGroup {
	e.mu.Lock()
	rules := make([]*Rule, len(e.rules))
	copy(rules, e.rules)
	e.mu.Unlock()

	nowT := time.Unix(now, 0)

	for _, rule := range rules {
		val, matched, err := rule.Evaluator(now)
		if err != nil {
			continue
		}

		e.mu.Lock()
		state, ok := e.ruleState[rule.ID]
		if !ok {
			state = &ruleBreachState{}
			e.ruleState[rule.ID] = state
		}
		e.mu.Unlock()

		if !matched {
			state.firstBreach = 0
			state.firing = false
			continue
		}

		if state.firstBreach == 0 {
			state.firstBreach = now
		}
		breachedFor := time.Duration(now-state.firstBreach) * time.Second
		if breachedFor < rule.For {
			continue // not breached long enough yet
		}
		state.firing = true

		alert := e.buildAlert(rule, val, nowT)

		if e.correlator != nil {
			e.correlator.Correlate(&alert, now)
		}

		if e.silencer != nil && e.silencer.IsSilenced(nowT, alert.Labels) {
			continue
		}

		if e.grouper != nil {
			receiver := ""
			if e.router != nil {
				recvs := e.router.MatchReceivers(alert.Labels)
				if len(recvs) > 0 {
					receiver = recvs[0]
				}
			}
			e.grouper.Add(alert, receiver)
		}
	}

	if e.grouper == nil {
		return nil
	}
	groups := e.grouper.Flush(nowT)

	if e.router != nil {
		ctx := context.Background()
		for _, g := range groups {
			_ = e.router.Dispatch(ctx, g)
		}
	}
	return groups
}

func (e *Engine) buildAlert(rule *Rule, val float64, startsAt time.Time) Alert {
	labels := make(map[string]string, len(rule.Labels))
	for k, v := range rule.Labels {
		labels[k] = v
	}
	annotations := make(map[string]string, len(rule.Annotations))
	for k, v := range rule.Annotations {
		annotations[k] = v
	}
	return Alert{
		RuleID:      rule.ID,
		Name:        rule.Name,
		Labels:      labels,
		Annotations: annotations,
		Severity:    rule.Severity,
		Value:       val,
		StartsAt:    startsAt,
	}
}

// IsFiring reports whether the rule with the given ID is currently past its
// "for" gate (i.e. would be generating alerts on this tick).
func (e *Engine) IsFiring(ruleID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.ruleState[ruleID]
	return ok && st.firing
}
