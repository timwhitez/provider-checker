package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"provider-checker/checker"
	"provider-checker/history"
)

// This file holds the platform-neutral pieces of the desktop front ends: the
// shared run state, form and label helpers, table cell text and CSV export.
// The Windows (lxn/walk) and macOS (AppKit) views differ only in widget
// plumbing, so behavior that users can observe lives here once.

// appState holds the mutable UI state shared between the UI thread and the run goroutine.
type appState struct {
	mu          sync.Mutex
	providerKey string // selected provider type key
	features    map[string]bool
	running     bool
	cancel      context.CancelFunc
	results     []checker.FeatureResult
	stores      *storeInit // shared, publish-once history store initialization
	listing     modelListing
}

// newAppState returns the initial state: the first provider type with Basic
// selected for convenience.
func newAppState() *appState {
	return &appState{
		providerKey: checker.ProviderTypes()[0],
		features:    map[string]bool{"basic": true},
		stores:      newStoreInit(),
	}
}

// selectedFeatureKeys returns the checked unified feature keys in display
// order. The caller must hold the lock protecting features.
func selectedFeatureKeys(features map[string]bool) []string {
	keys := []string{}
	for _, f := range checker.UnifiedFeatures {
		if features[f.Key] {
			keys = append(keys, f.Key)
		}
	}
	return keys
}

// reasoningControls reports which reasoning controls apply to a provider:
// effort for both OpenAI API styles, mode only for the Responses API.
func reasoningControls(providerKey string) (effort, mode bool) {
	effort = providerKey == "openai-chat" || providerKey == "openai-response"
	mode = providerKey == "openai-response"
	return effort, mode
}

// effectiveReasoning drops reasoning values the provider does not accept so
// Anthropic/Gemini never receive OpenAI reasoning fields.
func effectiveReasoning(providerKey, effort, mode string) (string, string) {
	effortOK, modeOK := reasoningControls(providerKey)
	if !effortOK {
		effort = ""
	}
	if !modeOK {
		mode = ""
	}
	return effort, mode
}

// statusLine formats the status bar text for the selected provider.
func statusLine(providerKey string, running bool) string {
	supCount := 0
	if c, ok := checker.AllCheckers[providerKey]; ok {
		supCount = len(c.Supported())
	}
	state := "Ready"
	if running {
		state = "Running..."
	}
	return fmt.Sprintf("  %s  \u00b7  Provider: %s  \u00b7  Capabilities: %d  ", state, checker.ProviderLabel(providerKey), supCount)
}

// runStartLogLine is the log line written when a run is started.
func runStartLogLine(providerKey, model string, features []string, effort, mode string) string {
	return fmt.Sprintf("Starting check: provider=%s model=%s features=%v reasoning=%s",
		checker.ProviderLabel(providerKey), model, features, reasoningLabel(effort, mode))
}

// featureStartLogLine is the log line written before a probe runs.
func featureStartLogLine(index, total int, feature string) string {
	return fmt.Sprintf("  -> [%d/%d] %s ...", index+1, total, checker.FeatureName(feature))
}

// featureDoneLogLine is the log line written when a probe completes.
func featureDoneLogLine(index, total int, r checker.FeatureResult) string {
	latency := ""
	if r.Latency > 0 {
		latency = fmt.Sprintf("%dms", r.Latency.Milliseconds())
	}
	line := fmt.Sprintf("  <- [%d/%d] %s: %s (%s)", index+1, total, r.Name, r.Status, latency)
	if r.Detail != "" {
		line += "  " + truncate(r.Detail, 80)
	}
	if r.UpstreamResponseModel != "" {
		line += "  upstream_model=" + truncate(r.UpstreamResponseModel, 120)
	}
	if r.Error != "" {
		line += "  err=" + truncate(r.Error, 120)
	}
	return line
}

// runOutcome classifies a finished run for summary coloring and dialog icons.
type runOutcome int

const (
	outcomePass  runOutcome = iota // no failures (including an all-SKIP run)
	outcomeMixed                   // some failures and some passes
	outcomeFail                    // failures and no passes
)

func outcomeOf(pass, fail int) runOutcome {
	switch {
	case fail > 0 && pass == 0:
		return outcomeFail
	case fail > 0:
		return outcomeMixed
	default:
		return outcomePass
	}
}

// runDoneSummary is the summary strip text for a finished run.
func runDoneSummary(pass, fail, skip int, model string) string {
	return fmt.Sprintf("检测完成 / Done   PASS=%d   FAIL=%d   SKIP=%d   \u00b7   %s", pass, fail, skip, model)
}

// defaultCSVName is the suggested file name for a CSV export.
func defaultCSVName(now time.Time) string {
	return fmt.Sprintf("provider-check-%s.csv", now.Format("20060102-150405"))
}

// resultCellText returns the display text of a results table cell.
// Columns: Feature, Status, Latency, Upstream Model, Detail, Error.
func resultCellText(r checker.FeatureResult, col int) (string, bool) {
	switch col {
	case 0:
		return r.Name, true
	case 1:
		return r.Status.String(), true
	case 2:
		if r.Latency > 0 {
			return fmt.Sprintf("%dms", r.Latency.Milliseconds()), true
		}
		return "", true
	case 3:
		return r.UpstreamResponseModel, true
	case 4:
		return r.Detail, true
	case 5:
		return r.Error, true
	}
	return "", false
}

// historyCellText returns the display text of a history table cell.
// Columns: Time, Provider, Model, Features, P/F/S, Reasoning.
func historyCellText(r history.Record, col int) (string, bool) {
	switch col {
	case 0:
		return r.Time.Format("2006-01-02 15:04:05"), true
	case 1:
		return r.ProviderLabel, true
	case 2:
		return r.Model, true
	case 3:
		return featureNames(r.Features), true
	case 4:
		return r.Summary(), true
	case 5:
		return reasoningLabel(r.ReasoningEffort, r.ReasoningMode), true
	}
	return "", false
}

// providerIndex returns the dropdown index for a provider type key, or -1.
func providerIndex(key string) int {
	for i, t := range checker.ProviderTypes() {
		if t == key {
			return i
		}
	}
	return -1
}

// parseIntDefault parses s as int, returning def on error.
func parseIntDefault(s string, def int) (int, error) {
	if s == "" {
		return def, nil
	}
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil {
		return def, err
	}
	if n <= 0 {
		return def, nil
	}
	return n, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// writeResultsCSV encodes feature results as CSV at path.
func writeResultsCSV(path string, results []checker.FeatureResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	if err := w.Write([]string{"Feature", "Status", "LatencyMs", "UpstreamResponseModel", "Detail", "Error"}); err != nil {
		return err
	}
	for _, r := range results {
		lat := ""
		if r.Latency > 0 {
			lat = fmt.Sprintf("%d", r.Latency.Milliseconds())
		}
		if err := w.Write([]string{r.Name, r.Status.String(), lat, r.UpstreamResponseModel, r.Detail, r.Error}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// featureNames maps unified feature keys to a compact display string.
func featureNames(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		name := checker.FeatureName(k)
		// Prefer the English tail after "/" if present for compactness.
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = strings.TrimSpace(name[i+1:])
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ", ")
}

// reasoningEffortLabels returns friendly combo labels aligned by index with
// checker.ReasoningEfforts (index 0 == "" == provider default).
func reasoningEffortLabels() []string {
	out := make([]string, len(checker.ReasoningEfforts))
	for i, v := range checker.ReasoningEfforts {
		if v == "" {
			out[i] = "默认 / Default"
		} else {
			out[i] = v
		}
	}
	return out
}

// reasoningModeLabels returns friendly combo labels aligned by index with
// checker.ReasoningModes (index 0 == "" == provider default).
func reasoningModeLabels() []string {
	out := make([]string, len(checker.ReasoningModes))
	for i, v := range checker.ReasoningModes {
		switch v {
		case "":
			out[i] = "默认 / Default"
		case "pro":
			out[i] = "pro (专业模式)"
		default:
			out[i] = v
		}
	}
	return out
}

// indexOfString returns the position of v in list, or 0 if not found.
func indexOfString(list []string, v string) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return 0
}

// reasoningLabel formats reasoning effort/mode for display.
func reasoningLabel(effort, mode string) string {
	switch {
	case effort == "" && mode == "":
		return "-"
	case mode == "":
		return effort
	case effort == "":
		return "mode=" + mode
	default:
		return effort + " / " + mode
	}
}
