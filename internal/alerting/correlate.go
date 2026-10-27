package alerting

import (
	"fmt"
	"sync"
	"time"
)

// DeployCorrelator maintains a feed of DeploymentEvents and, given a firing
// alert, finds the nearest prior deployment on the same service within a
// configurable lookback window - attaching it as a root-cause hint.
type DeployCorrelator struct {
	mu       sync.Mutex
	lookback time.Duration
	// events per service, kept sorted ascending by Time for efficient
	// nearest-prior lookup.
	events map[string][]DeploymentEvent
}

// NewDeployCorrelator builds a correlator that looks back up to `lookback`
// for deployments preceding an alert.
func NewDeployCorrelator(lookback time.Duration) *DeployCorrelator {
	return &DeployCorrelator{
		lookback: lookback,
		events:   make(map[string][]DeploymentEvent),
	}
}

// AddDeployment records a deployment event, keeping the per-service slice
// sorted by time.
func (c *DeployCorrelator) AddDeployment(e DeploymentEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	list := c.events[e.Service]
	// insertion sort - deployment volume is low, simplicity over
	// micro-optimizing.
	idx := len(list)
	for idx > 0 && list[idx-1].Time > e.Time {
		idx--
	}
	list = append(list, DeploymentEvent{})
	copy(list[idx+1:], list[idx:])
	list[idx] = e
	c.events[e.Service] = list
}

// NearestPriorDeploy returns the closest deployment for `service` at or
// before `atUnix` and within the lookback window, or ok=false if none
// qualifies.
func (c *DeployCorrelator) NearestPriorDeploy(service string, atUnix int64) (DeploymentEvent, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	list := c.events[service]
	minTime := atUnix - int64(c.lookback.Seconds())

	var best DeploymentEvent
	found := false
	for _, e := range list {
		if e.Time > atUnix {
			break // sorted ascending; no more candidates
		}
		if e.Time < minTime {
			continue // outside lookback window, but keep scanning for a later one
		}
		if !found || e.Time > best.Time {
			best = e
			found = true
		}
	}
	return best, found
}

// Correlate inspects the alert's "service" label and, if a qualifying prior
// deployment is found, attaches annotations["likely_cause_deploy"] to the
// alert describing it. It mutates alert.Annotations in place (creating the
// map if needed).
func (c *DeployCorrelator) Correlate(alert *Alert, now int64) {
	service := alert.Labels["service"]
	if service == "" {
		return
	}
	dep, ok := c.NearestPriorDeploy(service, now)
	if !ok {
		return
	}
	if alert.Annotations == nil {
		alert.Annotations = make(map[string]string)
	}
	alert.Annotations["likely_cause_deploy"] = fmt.Sprintf(
		"%s deployed version %s %ds before alert",
		dep.Service, dep.Version, now-dep.Time,
	)
}
