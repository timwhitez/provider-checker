package main

import (
	"context"
	"time"

	"provider-checker/checker"
	"provider-checker/history"
	"provider-checker/runner"
)

// runRecord owns execution evidence independently of the editable form/view.
// It is confined to the runner goroutine until completion.
type runRecord struct {
	provider string
	cfg      checker.Config
	prompt   string
	features []string
	results  []checker.FeatureResult
}

func newRunRecord(provider string, cfg checker.Config, prompt string, features []string) *runRecord {
	return &runRecord{provider: provider, cfg: cfg, prompt: prompt, features: append([]string(nil), features...)}
}

func (r *runRecord) historyRecord(now time.Time, encryptedKey string) history.Record {
	p, f, s := runner.Summary(r.results)
	return history.Record{
		Time: now, Provider: r.provider, ProviderLabel: checker.ProviderLabel(r.provider),
		BaseURL: r.cfg.BaseURL, Model: r.cfg.Model, APIKeyEnc: encryptedKey, Prompt: r.prompt,
		TimeoutSec: int(r.cfg.Timeout / time.Second), ReasoningEffort: r.cfg.ReasoningEffort,
		ReasoningMode: r.cfg.ReasoningMode, Features: append([]string(nil), r.features...), Pass: p, Fail: f, Skip: s,
	}
}

// modelListing is a UI-thread-owned request generation. Configuration changes,
// history loads, runs and window disposal invalidate both successes and errors.
type modelListing struct {
	generation uint64
	cancel     context.CancelFunc
	closed     bool
}

func (l *modelListing) invalidate() {
	l.generation++
	if l.cancel != nil {
		l.cancel()
		l.cancel = nil
	}
}

func (l *modelListing) begin(cancel context.CancelFunc) uint64 {
	l.invalidate()
	l.cancel = cancel
	return l.generation
}

func (l *modelListing) current(generation uint64) bool {
	return !l.closed && generation == l.generation
}

func (l *modelListing) close() { l.closed = true; l.invalidate() }
