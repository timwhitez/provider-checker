package runner

import (
	"context"
	"time"

	"provider-checker/checker"
)

// Event is a UI-facing update emitted during a run.
type Event struct {
	Type      string // "start" | "feature_start" | "feature_done" | "done"
	Index     int    // feature index (0-based) within the run
	Total     int    // total features in the run
	Feature   string
	Result    checker.FeatureResult
	Provider  string
	Model     string
	StartedAt time.Time
}

// Run executes the selected features sequentially, emitting events to out.
// It respects ctx cancellation (used by the Stop button): remaining features
// are marked SKIP/cancelled and the run ends immediately.
func Run(ctx context.Context, c checker.Checker, cfg checker.Config, features []string, prompt string, out func(Event)) {
	total := len(features)
	started := time.Now()

	out(Event{Type: "start", Total: total, Provider: c.Type(), Model: cfg.Model, StartedAt: started})

	// Sequential execution keeps output readable and avoids rate-limit bursts.
	for i, f := range features {
		if err := ctx.Err(); err != nil {
			for j := i; j < total; j++ {
				out(Event{
					Type:     "feature_done",
					Index:    j,
					Total:    total,
					Feature:  features[j],
					Result:   checker.FeatureResult{Name: features[j], Status: checker.StatusSkip, Error: "cancelled"},
					Provider: c.Type(),
					Model:    cfg.Model,
					StartedAt: time.Now(),
				})
			}
			out(Event{Type: "done", Total: total, Provider: c.Type(), Model: cfg.Model, StartedAt: started})
			return
		}

		out(Event{Type: "feature_start", Index: i, Total: total, Feature: f, Provider: c.Type(), Model: cfg.Model, StartedAt: time.Now()})
		r := c.Test(ctx, cfg, f, prompt)
		// If cancelled mid-request, surface a consistent cancelled status.
		if ctx.Err() != nil && r.Status != checker.StatusPass {
			r = checker.FeatureResult{Name: f, Status: checker.StatusSkip, Error: "cancelled", Latency: r.Latency}
		}
		out(Event{Type: "feature_done", Index: i, Total: total, Feature: f, Result: r, Provider: c.Type(), Model: cfg.Model, StartedAt: time.Now()})
	}

	out(Event{Type: "done", Total: total, Provider: c.Type(), Model: cfg.Model, StartedAt: started})
}

// Summary returns counts of pass/fail/skip from a result slice.
func Summary(rs []checker.FeatureResult) (pass, fail, skip int) {
	for _, r := range rs {
		switch r.Status {
		case checker.StatusPass:
			pass++
		case checker.StatusFail:
			fail++
		default:
			skip++
		}
	}
	return
}
