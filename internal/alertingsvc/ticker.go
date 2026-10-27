package alertingsvc

import (
	"context"
	"log"
	"time"

	"custom-log-aggregator/internal/alerting"
)

// StartTicker runs the alerting.Engine's tick loop (rule "for"-gating,
// grouping, routing, silencing) once per interval, until ctx is done. This
// is the same loop that used to run as internal/platform/alert_ticker.go's
// startAlertTicker, moved here since rule evaluation now happens in
// cmd/alerting-service rather than inside cmd/aggregator.
func StartTicker(ctx context.Context, engine *alerting.Engine, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				groups := engine.Evaluate(now.Unix())
				for _, g := range groups {
					log.Printf("alert group notified: %s", g.Summary())
				}
			}
		}
	}()
}
