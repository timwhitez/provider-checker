package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"provider-checker/checker"
	"provider-checker/runner"
)

type stagedChecker struct {
	entered, release chan struct{}
	prompts          []string
}

func (*stagedChecker) Type() string        { return "openai-chat" }
func (*stagedChecker) Supported() []string { return []string{"basic", "tools", "system"} }
func (c *stagedChecker) Test(ctx context.Context, cfg checker.Config, feature, prompt string) checker.FeatureResult {
	c.prompts = append(c.prompts, prompt)
	if feature == "tools" {
		close(c.entered)
		select {
		case <-c.release:
		case <-ctx.Done():
			return checker.FeatureResult{Status: checker.StatusFail}
		}
	}
	return checker.FeatureResult{Status: checker.StatusPass}
}

func TestRunRecordSurvivesViewAndFormChanges(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "cancelled"}[cancelled], func(t *testing.T) {
			features := []string{"basic", "tools", "system"}
			cfg := checker.Config{Model: "actual-model", APIKey: "never-store-plain", Timeout: time.Minute}
			record := newRunRecord("openai-chat", cfg, "Prompt A", features)
			c := &stagedChecker{entered: make(chan struct{}), release: make(chan struct{})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			// A view is deliberately disposable while the actual record is append-only.
			view := make(chan checker.FeatureResult, 3)
			go func() {
				runner.Run(ctx, c, record.cfg, record.features, record.prompt, func(ev runner.Event) {
					if ev.Type == "feature_done" {
						record.results = append(record.results, ev.Result)
						view <- ev.Result
					}
				})
				close(done)
			}()
			<-c.entered
			<-view // Clear the first visible result while second probe is blocked.
			cfg.Model = "edited-model"
			features[0] = "vision" // Load another config/selection.
			if cancelled {
				cancel()
			} else {
				close(c.release)
			}
			<-done
			rec := record.historyRecord(time.Now(), "encrypted-only")
			if rec.Prompt != "Prompt A" || rec.Model != "actual-model" || rec.Features[0] != "basic" || rec.APIKeyEnc != "encrypted-only" {
				t.Fatalf("history changed: %+v", rec)
			}
			if cancelled {
				if rec.Pass != 1 || rec.Skip != 2 || rec.Fail != 0 {
					t.Fatalf("cancel summary: %+v", rec)
				}
			} else if rec.Pass != 3 || rec.Fail != 0 || rec.Skip != 0 {
				t.Fatalf("complete summary: %+v", rec)
			}
			for _, prompt := range c.prompts {
				if prompt != rec.Prompt {
					t.Fatalf("sent %q saved %q", prompt, rec.Prompt)
				}
			}
			fresh := newRunRecord("openai-chat", cfg, "Prompt B", []string{"vision"})
			if len(fresh.results) != 0 {
				t.Fatal("new run inherited results")
			}
		})
	}
}

func TestModelListingDropsDelayedSuccessAndError(t *testing.T) {
	for _, change := range []string{"provider", "baseURL", "auth", "timeout", "history", "run", "close", "new-list"} {
		for _, fail := range []bool{false, true} {
			t.Run(change+map[bool]string{false: "-success", true: "-error"}[fail], func(t *testing.T) {
				var listing modelListing
				ctx, cancel := context.WithCancel(context.Background())
				generation := listing.begin(cancel)
				release := make(chan struct{})
				result := make(chan error, 1)
				// Fake lister intentionally completes despite cancellation, like a racing response.
				go func() {
					<-release
					if fail {
						result <- errors.New("stale provider error")
					} else {
						result <- nil
					}
				}()
				if change == "close" {
					listing.close()
				} else if change == "new-list" {
					listing.begin(func() {})
				} else {
					listing.invalidate()
				}
				if ctx.Err() != context.Canceled {
					t.Fatal("ownership change did not cancel request")
				}
				close(release)
				<-result
				if listing.current(generation) {
					t.Fatal("stale delivery could change form or display error")
				}
			})
		}
	}
}

func TestModelListingCurrentGeneration(t *testing.T) {
	var listing modelListing
	gen := listing.begin(func() {})
	// Model edits do not alter connection ownership; latest input is read at delivery.
	if !listing.current(gen) {
		t.Fatal("current listing rejected")
	}
	listing.close()
	if listing.current(gen) {
		t.Fatal("closed window accepted result")
	}
}
