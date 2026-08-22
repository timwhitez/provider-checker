package runner

import (
	"context"
	"testing"
	"time"

	"provider-checker/checker"
)

type stubChecker struct {
	delay time.Duration
	calls int
}

func (stubChecker) Type() string              { return "stub" }
func (stubChecker) Supported() []string       { return []string{"basic", "stream"} }
func (s *stubChecker) Test(ctx context.Context, cfg checker.Config, feature, prompt string) checker.FeatureResult {
	s.calls++
	if s.delay > 0 {
		select {
		case <-ctx.Done():
			return checker.FeatureResult{Name: feature, Status: checker.StatusFail, Error: ctx.Err().Error()}
		case <-time.After(s.delay):
		}
	}
	return checker.FeatureResult{Name: feature, Status: checker.StatusPass, Detail: "ok"}
}

func TestRunCancelSkipsRemaining(t *testing.T) {
	c := &stubChecker{delay: 50 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after the first feature starts.
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	var doneFeatures []string
	var statuses []checker.Status
	Run(ctx, c, checker.Config{Model: "m"}, []string{"basic", "stream"}, "", func(ev Event) {
		if ev.Type == "feature_done" {
			doneFeatures = append(doneFeatures, ev.Feature)
			statuses = append(statuses, ev.Result.Status)
		}
	})

	if len(doneFeatures) != 2 {
		t.Fatalf("want 2 feature_done events, got %d (%v)", len(doneFeatures), doneFeatures)
	}
	// First may pass or skip depending on timing; remaining after cancel must be SKIP.
	if statuses[1] != checker.StatusSkip {
		t.Fatalf("second feature status = %v, want SKIP", statuses[1])
	}
}

func TestSummary(t *testing.T) {
	p, f, s := Summary([]checker.FeatureResult{
		{Status: checker.StatusPass},
		{Status: checker.StatusPass},
		{Status: checker.StatusFail},
		{Status: checker.StatusSkip},
	})
	if p != 2 || f != 1 || s != 1 {
		t.Fatalf("Summary = %d/%d/%d, want 2/1/1", p, f, s)
	}
}
