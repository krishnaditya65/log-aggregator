package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Notifier delivers a NotifiedGroup to some external system (Slack,
// PagerDuty, a generic webhook, ...).
type Notifier interface {
	Notify(ctx context.Context, group NotifiedGroup) error
}

// RetryConfig controls the retry/backoff behavior shared by the built-in
// HTTP-based notifiers.
type RetryConfig struct {
	MaxAttempts int           // total attempts including the first, >=1
	BaseDelay   time.Duration // delay before the 2nd attempt; doubles each subsequent retry
}

func defaultRetryConfig() RetryConfig {
	return RetryConfig{MaxAttempts: 3, BaseDelay: 50 * time.Millisecond}
}

// doWithRetry runs fn, retrying with exponential backoff on error, up to
// cfg.MaxAttempts total attempts. It respects ctx cancellation between
// attempts.
func doWithRetry(ctx context.Context, cfg RetryConfig, fn func() error) error {
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}
	var lastErr error
	delay := cfg.BaseDelay
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if attempt == cfg.MaxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
	return fmt.Errorf("after %d attempts: %w", cfg.MaxAttempts, lastErr)
}

// ---------- Slack ----------

// SlackNotifier posts to a Slack incoming-webhook URL.
type SlackNotifier struct {
	WebhookURL string
	Client     *http.Client
	Retry      RetryConfig
}

// NewSlackNotifier builds a SlackNotifier with sane defaults.
func NewSlackNotifier(webhookURL string) *SlackNotifier {
	return &SlackNotifier{
		WebhookURL: webhookURL,
		Client:     http.DefaultClient,
		Retry:      defaultRetryConfig(),
	}
}

type slackPayload struct {
	Text   string       `json:"text"`
	Blocks []slackBlock `json:"blocks"`
}

type slackBlock struct {
	Type string         `json:"type"`
	Text *slackBlockTxt `json:"text,omitempty"`
}

type slackBlockTxt struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func buildSlackPayload(g NotifiedGroup) slackPayload {
	text := fmt.Sprintf("[%s] %s", g.Receiver, g.Summary())
	blocks := []slackBlock{
		{
			Type: "section",
			Text: &slackBlockTxt{Type: "mrkdwn", Text: "*" + g.Summary() + "*"},
		},
	}
	for _, a := range g.Alerts {
		line := fmt.Sprintf("• *%s* severity=%s value=%.4f", a.Name, a.Severity, a.Value)
		if cause, ok := a.Annotations["likely_cause_deploy"]; ok {
			line += "\n  _likely cause: " + cause + "_"
		}
		blocks = append(blocks, slackBlock{
			Type: "section",
			Text: &slackBlockTxt{Type: "mrkdwn", Text: line},
		})
	}
	return slackPayload{Text: text, Blocks: blocks}
}

// Notify implements Notifier.
func (s *SlackNotifier) Notify(ctx context.Context, g NotifiedGroup) error {
	payload := buildSlackPayload(g)
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	return doWithRetry(ctx, s.Retry, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.WebhookURL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("slack webhook returned status %d", resp.StatusCode)
		}
		return nil
	})
}

// ---------- PagerDuty ----------

// PagerDutyNotifier posts events to the PagerDuty Events API v2.
type PagerDutyNotifier struct {
	RoutingKey string
	Source     string
	Endpoint   string // defaults to https://events.pagerduty.com/v2/enqueue
	Client     *http.Client
	Retry      RetryConfig
}

// NewPagerDutyNotifier builds a PagerDutyNotifier with sane defaults.
func NewPagerDutyNotifier(routingKey, source string) *PagerDutyNotifier {
	return &PagerDutyNotifier{
		RoutingKey: routingKey,
		Source:     source,
		Endpoint:   "https://events.pagerduty.com/v2/enqueue",
		Client:     http.DefaultClient,
		Retry:      defaultRetryConfig(),
	}
}

type pagerDutyPayload struct {
	RoutingKey  string             `json:"routing_key"`
	EventAction string             `json:"event_action"`
	DedupKey    string             `json:"dedup_key,omitempty"`
	Payload     pagerDutyEventBody `json:"payload"`
}

type pagerDutyEventBody struct {
	Summary  string `json:"summary"`
	Severity string `json:"severity"`
	Source   string `json:"source"`
}

func pagerDutySeverity(sev string) string {
	switch sev {
	case "critical", "error", "warning", "info":
		return sev
	default:
		return "warning"
	}
}

func buildPagerDutyPayload(g NotifiedGroup, source string) pagerDutyPayload {
	severity := "warning"
	if len(g.Alerts) > 0 {
		severity = pagerDutySeverity(g.Alerts[0].Severity)
	}
	return pagerDutyPayload{
		EventAction: "trigger",
		DedupKey:    g.GroupKey,
		Payload: pagerDutyEventBody{
			Summary:  g.Summary(),
			Severity: severity,
			Source:   source,
		},
	}
}

// Notify implements Notifier.
func (p *PagerDutyNotifier) Notify(ctx context.Context, g NotifiedGroup) error {
	payload := buildPagerDutyPayload(g, p.Source)
	payload.RoutingKey = p.RoutingKey
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	client := p.Client
	if client == nil {
		client = http.DefaultClient
	}
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = "https://events.pagerduty.com/v2/enqueue"
	}
	return doWithRetry(ctx, p.Retry, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("pagerduty events api returned status %d", resp.StatusCode)
		}
		return nil
	})
}

// ---------- Generic webhook ----------

// WebhookNotifier posts the raw NotifiedGroup as JSON to an arbitrary URL.
type WebhookNotifier struct {
	URL    string
	Client *http.Client
	Retry  RetryConfig
}

// NewWebhookNotifier builds a WebhookNotifier with sane defaults.
func NewWebhookNotifier(url string) *WebhookNotifier {
	return &WebhookNotifier{URL: url, Client: http.DefaultClient, Retry: defaultRetryConfig()}
}

// Notify implements Notifier.
func (w *WebhookNotifier) Notify(ctx context.Context, g NotifiedGroup) error {
	body, err := json.Marshal(g)
	if err != nil {
		return err
	}
	client := w.Client
	if client == nil {
		client = http.DefaultClient
	}
	return doWithRetry(ctx, w.Retry, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("webhook returned status %d", resp.StatusCode)
		}
		return nil
	})
}

// ---------- Router ----------

// Router matches alerts to one or more receivers using an Alertmanager-like
// routing tree (a default root Route plus nested child Routes with
// matcher-based overrides), then dispatches NotifiedGroups to the
// corresponding Notifiers.
type Router struct {
	Root      *Route
	receivers map[string]Notifier
}

// NewRouter builds a Router with the given root route (the "default
// route" - typically has empty Matchers so it always matches) and receiver
// set.
func NewRouter(root *Route, receivers map[string]Notifier) *Router {
	return &Router{Root: root, receivers: receivers}
}

// MatchReceivers returns the receiver name(s) that the given label set
// routes to.
func (r *Router) MatchReceivers(labels map[string]string) []string {
	if r.Root == nil {
		return nil
	}
	return r.Root.resolveReceivers(labels)
}

// Dispatch routes the group's alerts (using the first alert's labels, since
// a group is already homogeneous on the grouping labels) and sends the
// notification to every matched receiver's Notifier. It returns the first
// error encountered, if any, but attempts delivery to all matched
// receivers regardless.
func (r *Router) Dispatch(ctx context.Context, g NotifiedGroup) error {
	var labels map[string]string
	if len(g.Alerts) > 0 {
		labels = g.Alerts[0].Labels
	}
	receiverNames := []string{g.Receiver}
	if g.Receiver == "" {
		receiverNames = r.MatchReceivers(labels)
	}

	var firstErr error
	for _, name := range receiverNames {
		notifier, ok := r.receivers[name]
		if !ok {
			continue
		}
		gg := g
		gg.Receiver = name
		if err := notifier.Notify(ctx, gg); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
