//go:build darwin

package main

import (
	"context"
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"provider-checker/checker"
	"provider-checker/history"
)

// fakeView is an in-memory macView. Synchronize queues work that the test
// drains on its own goroutine, standing in for the AppKit main thread.
type fakeView struct {
	text     map[widgetID]string
	disabled map[widgetID]bool
	checked  map[widgetID]bool
	index    map[widgetID]int
	items    map[widgetID][]string
	log      []string
	summary  string
	tone     tone
	status   string
	alerts   []string
	confirm  bool
	savePath string
	reloads  map[tableID]int
	queue    chan func()
}

func newFakeView() *fakeView {
	return &fakeView{
		text: map[widgetID]string{}, disabled: map[widgetID]bool{}, checked: map[widgetID]bool{},
		index: map[widgetID]int{}, items: map[widgetID][]string{}, reloads: map[tableID]int{},
		queue: make(chan func(), 256),
	}
}

func (v *fakeView) Text(id widgetID) string        { return v.text[id] }
func (v *fakeView) SetText(id widgetID, s string)  { v.text[id] = s }
func (v *fakeView) Enabled(id widgetID) bool       { return !v.disabled[id] }
func (v *fakeView) SetEnabled(id widgetID, b bool) { v.disabled[id] = !b }
func (v *fakeView) Checked(id widgetID) bool       { return v.checked[id] }
func (v *fakeView) SetChecked(id widgetID, b bool) { v.checked[id] = b }
func (v *fakeView) SelectedIndex(id widgetID) int  { return v.index[id] }
func (v *fakeView) SelectIndex(id widgetID, i int) { v.index[id] = i }
func (v *fakeView) SetItems(id widgetID, items []string) {
	v.items[id] = append([]string(nil), items...) // text is untouched, like NSComboBox
}
func (v *fakeView) ReloadTable(t tableID)          { v.reloads[t]++ }
func (v *fakeView) AppendLog(line string)          { v.log = append(v.log, line) }
func (v *fakeView) ClearLog()                      { v.log = nil }
func (v *fakeView) SetSummary(text string, t tone) { v.summary, v.tone = text, t }
func (v *fakeView) SetStatus(text string)          { v.status = text }
func (v *fakeView) Alert(style alertStyle, title, message string) {
	v.alerts = append(v.alerts, title)
}
func (v *fakeView) Confirm(title, message string) bool {
	v.alerts = append(v.alerts, title)
	return v.confirm
}
func (v *fakeView) SaveFile(title, name string) (string, bool) { return v.savePath, v.savePath != "" }
func (v *fakeView) Synchronize(fn func())                      { v.queue <- fn }

// pump runs queued main-thread work until cond holds.
func (v *fakeView) pump(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !cond() {
		select {
		case fn := <-v.queue:
			fn()
		case <-deadline:
			t.Fatal("timed out waiting for main-thread work")
		}
	}
}

func (v *fakeView) lastAlert() string {
	if len(v.alerts) == 0 {
		return ""
	}
	return v.alerts[len(v.alerts)-1]
}

// scriptedChecker passes "basic", fails "tools" and blocks "stream" until cancelled.
type scriptedChecker struct{ entered chan struct{} }

func (scriptedChecker) Type() string { return "openai-chat" }
func (scriptedChecker) Supported() []string {
	return []string{"basic", "stream", "tools", "json", "max_tokens", "system", "vision"}
}
func (c scriptedChecker) Test(ctx context.Context, cfg checker.Config, feature, prompt string) checker.FeatureResult {
	switch feature {
	case "tools":
		return checker.FeatureResult{Status: checker.StatusFail, Error: "no tool call", Latency: time.Millisecond}
	case "stream":
		close(c.entered)
		<-ctx.Done()
		return checker.FeatureResult{Status: checker.StatusFail, Error: ctx.Err().Error()}
	}
	return checker.FeatureResult{Status: checker.StatusPass, Detail: "hello", UpstreamResponseModel: cfg.Model + "-2026", Latency: 2 * time.Millisecond}
}

func withChecker(t *testing.T, key string, c checker.Checker) {
	t.Helper()
	orig := checker.AllCheckers[key]
	checker.AllCheckers[key] = c
	t.Cleanup(func() { checker.AllCheckers[key] = orig })
}

func newTestApp(t *testing.T) (*macApp, *fakeView) {
	t.Helper()
	v := newFakeView()
	enc := func(s string) (string, error) {
		if s == "" {
			return "", nil
		}
		return "enc:" + s, nil
	}
	dec := func(s string) (string, error) {
		if s == "" {
			return "", nil
		}
		if !strings.HasPrefix(s, "enc:") {
			return "", errors.New("bad ciphertext")
		}
		return strings.TrimPrefix(s, "enc:"), nil
	}
	a := newMacApp(v, newAppState(), enc, dec)
	path := filepath.Join(t.TempDir(), "history.json")
	a.openStore = func() (*history.Store, error) { return history.Open(path) }
	a.start()
	v.pump(t, func() bool { return v.reloads[tableHistory] > 0 })
	return a, v
}

func clickCheck(a *macApp, v *fakeView, key string, on bool) {
	for i, f := range checker.UnifiedFeatures {
		if f.Key == key {
			v.checked[checkID(i)] = on
			a.handleEvent(checkID(i), evAction)
		}
	}
}

func featureIndex(key string) int {
	for i, f := range checker.UnifiedFeatures {
		if f.Key == key {
			return i
		}
	}
	return -1
}

func TestMacStartInitialState(t *testing.T) {
	_, v := newTestApp(t)
	if len(v.items[wProvider]) != len(checker.ProviderTypes()) || v.index[wProvider] != 0 {
		t.Fatalf("provider popup = %v @%d", v.items[wProvider], v.index[wProvider])
	}
	if len(v.items[wEffort]) != len(checker.ReasoningEfforts) || len(v.items[wMode]) != len(checker.ReasoningModes) {
		t.Fatal("reasoning popups not filled")
	}
	if !v.checked[checkID(featureIndex("basic"))] || v.text[wTimeout] != "60" {
		t.Fatal("defaults not applied")
	}
	if v.Enabled(wStop) || !v.Enabled(wRun) || !v.Enabled(wEffort) || v.Enabled(wMode) {
		t.Fatal("initial enablement wrong for OpenAI Chat")
	}
	if !strings.Contains(v.status, "Ready") || !strings.Contains(v.summary, "No run yet") {
		t.Fatalf("status %q summary %q", v.status, v.summary)
	}
}

func TestMacCapabilitySelectionFollowsProvider(t *testing.T) {
	a, v := newTestApp(t)
	jsonID := checkID(featureIndex("json"))
	clickCheck(a, v, "json", true)
	if !a.st.features["json"] {
		t.Fatal("checkbox click did not update selection")
	}
	v.index[wProvider] = providerIndex("anthropic")
	a.handleEvent(wProvider, evAction)
	if v.Enabled(jsonID) || v.checked[jsonID] || a.st.features["json"] {
		t.Fatal("unsupported JSON remains selected")
	}
	if v.Enabled(wEffort) || v.Enabled(wMode) || !strings.Contains(v.status, "Anthropic") {
		t.Fatal("reasoning/status not refreshed for Anthropic")
	}
	v.index[wProvider] = providerIndex("openai-response")
	a.handleEvent(wProvider, evAction)
	if !v.Enabled(jsonID) || v.checked[jsonID] {
		t.Fatal("JSON revived after provider switch")
	}
	if !v.Enabled(wEffort) || !v.Enabled(wMode) {
		t.Fatal("Responses reasoning controls disabled")
	}
}

func TestMacModelListingKeepsLatestTextAndDropsStale(t *testing.T) {
	a, v := newTestApp(t)
	for _, latest := range []string{"edited-model", ""} {
		v.text[wModel] = "request-model"
		generation := a.st.listing.begin(func() {})
		v.text[wModel] = latest
		a.completeModelListing(generation, []string{"provider-model"}, nil)
		if v.text[wModel] != latest || len(v.items[wModel]) != 1 {
			t.Fatalf("latest %q overwritten by %q", latest, v.text[wModel])
		}
	}
	generation := a.st.listing.begin(func() {})
	v.text[wBaseURL] = "https://other.example"
	a.handleEvent(wBaseURL, evTextChanged) // connection change invalidates and clears the list
	v.text[wModel] = "new-model"
	alerts := len(v.alerts)
	a.completeModelListing(generation, []string{"old-model"}, nil)
	a.completeModelListing(generation, nil, errors.New("stale error must not open dialog"))
	if v.text[wModel] != "new-model" || len(v.items[wModel]) != 0 || len(v.alerts) != alerts {
		t.Fatal("stale delivery changed form")
	}
	a.handleEvent(wModel, evTextChanged) // editing the model keeps ownership
	if !a.st.listing.current(a.st.listing.generation) {
		t.Fatal("model edit invalidated listing")
	}
	a.closing()
	a.completeModelListing(a.st.listing.generation, nil, errors.New("closed window"))
	if len(v.alerts) != alerts {
		t.Fatal("closed window showed an alert")
	}
	a.invalidateModels(true)
	if v.Enabled(wListModels) {
		t.Fatal("List enabled after the window closed")
	}
}

func TestMacRunUpdatesViewSavesHistoryAndReloads(t *testing.T) {
	withChecker(t, "openai-chat", scriptedChecker{entered: make(chan struct{})})
	a, v := newTestApp(t)
	clickCheck(a, v, "tools", true)
	v.text[wModel] = "m1"
	v.text[wAPIKey] = "sk-1"
	v.text[wBaseURL] = "https://proxy.example"
	v.text[wPrompt] = "Say hi"
	v.index[wEffort] = indexOfString(checker.ReasoningEfforts, "high")

	a.handleEvent(wRun, evAction)
	if v.Enabled(wRun) || !v.Enabled(wStop) || v.Enabled(wListModels) || v.tone != tonePending {
		t.Fatal("running controls not applied")
	}
	a.handleEvent(wRun, evAction) // a second click while running is ignored
	v.pump(t, func() bool { return v.lastAlert() == "Check Complete" })

	if a.st.running || !v.Enabled(wRun) || v.Enabled(wStop) || !v.Enabled(wListModels) {
		t.Fatal("controls not restored after run")
	}
	if a.tableRows(tableResults) != 2 {
		t.Fatalf("result rows = %d", a.tableRows(tableResults))
	}
	if text, style := a.tableCell(tableResults, 0, 1); text != "PASS" || style != cellPillPass {
		t.Fatalf("status cell = %q/%d", text, style)
	}
	if text, _ := a.tableCell(tableResults, 0, 3); text != "m1-2026" {
		t.Fatalf("upstream cell = %q", text)
	}
	if _, style := a.tableCell(tableResults, 1, 1); style != cellPillFail {
		t.Fatal("FAIL pill missing")
	}
	if v.tone != tonePending || !strings.Contains(v.summary, "PASS=1   FAIL=1") {
		t.Fatalf("summary %q tone %d", v.summary, v.tone)
	}
	if a.tableRows(tableHistory) != 1 {
		t.Fatal("history not saved")
	}
	rec := a.records[0]
	if rec.APIKeyEnc != "enc:sk-1" || rec.Model != "m1" || rec.ReasoningEffort != "high" || rec.Pass != 1 || rec.Fail != 1 {
		t.Fatalf("history record %+v", rec)
	}
	if _, style := a.tableCell(tableHistory, 0, 4); style != cellTextFail {
		t.Fatal("history summary not styled as failure")
	}

	// Clear display keeps history; CSV export of an empty view is refused.
	a.handleEvent(wClear, evAction)
	if a.tableRows(tableResults) != 0 || len(v.log) != 0 || !strings.Contains(v.summary, "No run yet") {
		t.Fatal("display not cleared")
	}
	a.handleEvent(wExport, evAction)
	if v.lastAlert() != "Nothing to export" || a.tableRows(tableHistory) != 1 {
		t.Fatal("clear affected history or export")
	}

	// Change the form, then reload the history row.
	v.index[wProvider] = providerIndex("gemini")
	a.handleEvent(wProvider, evAction)
	v.text[wModel], v.text[wAPIKey], v.text[wPrompt] = "other", "", ""
	a.loadHistoryRow(0)
	if v.text[wModel] != "m1" || v.text[wAPIKey] != "sk-1" || v.text[wBaseURL] != "https://proxy.example" || v.text[wPrompt] != "Say hi" {
		t.Fatalf("form not restored: %v", v.text)
	}
	if v.index[wProvider] != providerIndex("openai-chat") || a.st.providerKey != "openai-chat" || !v.Enabled(wEffort) {
		t.Fatal("provider not restored")
	}
	if v.index[wEffort] != indexOfString(checker.ReasoningEfforts, "high") {
		t.Fatal("effort not restored")
	}
	if !v.checked[checkID(featureIndex("tools"))] || !a.st.features["basic"] || a.st.features["json"] {
		t.Fatal("features not restored")
	}
	if v.lastAlert() != "History Loaded" {
		t.Fatal("no confirmation shown")
	}

	// Clear history requires confirmation.
	a.handleEvent(wHistoryClear, evAction)
	if a.tableRows(tableHistory) != 1 {
		t.Fatal("history cleared without confirmation")
	}
	v.confirm = true
	a.handleEvent(wHistoryClear, evAction)
	if a.tableRows(tableHistory) != 0 {
		t.Fatal("history not cleared")
	}
}

func TestMacStopCancelsRunAndRecordsSkips(t *testing.T) {
	c := scriptedChecker{entered: make(chan struct{})}
	withChecker(t, "openai-chat", c)
	a, v := newTestApp(t)
	clickCheck(a, v, "stream", true)
	clickCheck(a, v, "system", true)
	v.text[wModel] = "m1"
	a.handleEvent(wRun, evAction)
	<-c.entered
	a.handleEvent(wStop, evAction)
	v.pump(t, func() bool { return v.lastAlert() == "Check Complete" })
	if a.tableRows(tableResults) != 3 {
		t.Fatalf("rows = %d", a.tableRows(tableResults))
	}
	for row, want := range []string{"PASS", "SKIP", "SKIP"} {
		if text, _ := a.tableCell(tableResults, row, 1); text != want {
			t.Fatalf("row %d status %q, want %q", row, text, want)
		}
	}
	rec := a.records[0]
	if rec.Pass != 1 || rec.Skip != 2 || rec.APIKeyEnc != "" {
		t.Fatalf("cancelled record %+v", rec)
	}
}

func TestMacExportCSV(t *testing.T) {
	withChecker(t, "openai-chat", scriptedChecker{entered: make(chan struct{})})
	a, v := newTestApp(t)
	clickCheck(a, v, "tools", true)
	v.text[wModel] = "m1"
	a.handleEvent(wRun, evAction)
	v.pump(t, func() bool { return v.lastAlert() == "Check Complete" })

	// Cancelling the save panel writes nothing and shows nothing.
	alerts := len(v.alerts)
	a.handleEvent(wExport, evAction)
	if len(v.alerts) != alerts {
		t.Fatal("cancelled save panel showed an alert")
	}

	v.savePath = filepath.Join(t.TempDir(), "out.csv")
	a.handleEvent(wExport, evAction)
	if v.lastAlert() != "Exported" {
		t.Fatalf("alert = %q", v.lastAlert())
	}
	data, err := os.ReadFile(v.savePath)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil || len(rows) != 3 || rows[1][1] != "PASS" || rows[2][1] != "FAIL" || rows[1][3] != "m1-2026" {
		t.Fatalf("csv rows %v, %v", rows, err)
	}

	v.savePath = filepath.Join(t.TempDir(), "missing-dir", "out.csv")
	a.handleEvent(wExport, evAction)
	if v.lastAlert() != "Export failed" {
		t.Fatalf("alert = %q", v.lastAlert())
	}
}

func TestMacRunValidation(t *testing.T) {
	a, v := newTestApp(t)
	a.handleEvent(wRun, evAction)
	if v.lastAlert() != "Model required" || a.st.running {
		t.Fatal("missing model not rejected")
	}
	clickCheck(a, v, "basic", false)
	v.text[wModel] = "m1"
	a.handleEvent(wRun, evAction)
	if v.lastAlert() != "No features selected" || a.st.running {
		t.Fatal("empty selection not rejected")
	}
}

func TestMacHistoryLoadReportsUndecryptableKey(t *testing.T) {
	a, v := newTestApp(t)
	a.setRecords([]history.Record{{Provider: "anthropic", Model: "claude", APIKeyEnc: "garbage", Features: []string{"basic", "json"}}})
	v.text[wAPIKey] = "typed"
	a.loadHistoryRow(0)
	if v.text[wAPIKey] != "" || v.lastAlert() != "History Loaded" || a.st.features["json"] {
		t.Fatal("bad history row handling")
	}
	found := false
	for _, l := range v.log {
		found = found || strings.Contains(l, "API key decrypt failed")
	}
	if !found {
		t.Fatal("decrypt failure not logged")
	}
	a.loadHistoryRow(5) // out of range is ignored
}
