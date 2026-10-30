//go:build darwin

package main

import (
	"context"
	"fmt"
	"time"

	"provider-checker/checker"
	"provider-checker/history"
	"provider-checker/runner"
)

// The macOS front end is split in two: this file holds the controller logic
// (pure Go, unit-testable with a fake view) and cocoa_darwin.go implements
// macView with AppKit. Every controller method runs on the AppKit main thread;
// background goroutines hand results back through macView.Synchronize, the
// equivalent of walk's MainWindow.Synchronize on Windows.

// widgetID identifies an interactive control. The values are mirrored by the
// PC_W_* enum in cocoa_darwin.go (checked by cocoaWidgetIDsMatch).
type widgetID int

const (
	wProvider widgetID = iota
	wModel
	wListModels
	wBaseURL
	wAPIKey
	wPrompt
	wTimeout
	wEffort
	wMode
	wRun
	wStop
	wExport
	wClear
	wHistoryRefresh
	wHistoryClear
	wCheck0 // first capability checkbox; checkbox i is wCheck0+i
)

// maxChecks bounds the capability checkboxes the Cocoa view can host.
const maxChecks = 16

// wCount is the number of widget slots (all IDs are below it).
const wCount = wCheck0 + maxChecks

func checkID(i int) widgetID { return wCheck0 + widgetID(i) }

type eventKind int

const (
	evAction      eventKind = iota // button/checkbox click, popup selection
	evTextChanged                  // text field edited by the user
)

type tableID int

const (
	tableResults tableID = iota
	tableHistory
)

type alertStyle int

const (
	alertInfo alertStyle = iota
	alertWarning
	alertError
)

// tone is the semantic color of the summary strip.
type tone int

const (
	toneMuted tone = iota
	tonePass
	toneFail
	tonePending
)

// cellStyle is the semantic style of a table cell.
type cellStyle int

const (
	cellPlain cellStyle = iota
	cellPillPass
	cellPillFail
	cellPillSkip
	cellPillPending
	cellTextPass
	cellTextFail
	cellTextMuted
)

// macView is the widget surface the controller drives. Programmatic changes
// (SetText, SelectIndex, SetChecked) never emit events; only user input does.
type macView interface {
	Text(id widgetID) string
	SetText(id widgetID, s string)
	Enabled(id widgetID) bool
	SetEnabled(id widgetID, on bool)
	Checked(id widgetID) bool
	SetChecked(id widgetID, on bool)
	SelectedIndex(id widgetID) int
	SelectIndex(id widgetID, i int)
	// SetItems replaces popup items; for the editable model combo box the
	// current text is preserved.
	SetItems(id widgetID, items []string)
	ReloadTable(t tableID)
	AppendLog(line string)
	ClearLog()
	SetSummary(text string, t tone)
	SetStatus(text string)
	Alert(style alertStyle, title, message string)
	Confirm(title, message string) bool
	// SaveFile asks for a destination path; ok is false when cancelled.
	SaveFile(title, defaultName string) (path string, ok bool)
	// Synchronize runs fn on the main thread (asynchronously).
	Synchronize(fn func())
}

// macApp is the macOS controller. Its fields are owned by the main thread,
// except st, which is shared with run goroutines under st.mu.
type macApp struct {
	v  macView
	st *appState

	results []checker.FeatureResult // visible results table ("Clear display" empties it)
	records []history.Record        // visible history table, newest first
	models  []string                // latest model listing offered by the combo box

	openStore func() (*history.Store, error)
	encrypt   func(string) (string, error)
	decrypt   func(string) (string, error)
	now       func() time.Time
}

func newMacApp(v macView, st *appState, encrypt, decrypt func(string) (string, error)) *macApp {
	return &macApp{v: v, st: st, openStore: openHistoryStore, encrypt: encrypt, decrypt: decrypt, now: time.Now}
}

// start fills the widgets with their initial state and loads history in the
// background so the window can paint immediately.
func (a *macApp) start() {
	types := checker.ProviderTypes()
	labels := make([]string, len(types))
	for i, t := range types {
		labels[i] = checker.ProviderLabel(t)
	}
	a.v.SetItems(wProvider, labels)
	a.st.mu.Lock()
	pk := a.st.providerKey
	a.st.mu.Unlock()
	a.v.SelectIndex(wProvider, providerIndex(pk))
	a.v.SetItems(wEffort, reasoningEffortLabels())
	a.v.SelectIndex(wEffort, 0)
	a.v.SetItems(wMode, reasoningModeLabels())
	a.v.SelectIndex(wMode, 0)
	a.v.SetText(wTimeout, "60")
	a.v.SetEnabled(wStop, false)

	a.refreshChecks()
	a.syncChecks()
	a.refreshReasoning()
	a.refreshStatus()
	a.setSummary("", toneMuted)

	// The shared initialization publishes the store before the UI round-trip,
	// so a concurrent save waits for and reuses it (see storeInit).
	go func() {
		store := a.st.stores.ensure(a.openStore)
		if store == nil {
			return
		}
		recs := store.Records()
		a.v.Synchronize(func() { a.setRecords(recs) })
	}()
}

// handleEvent dispatches a user interaction.
func (a *macApp) handleEvent(id widgetID, ev eventKind) {
	if id >= wCheck0 && id < checkID(len(checker.UnifiedFeatures)) {
		if ev == evAction {
			key := checker.UnifiedFeatures[id-wCheck0].Key
			checked := a.v.Checked(id)
			a.st.mu.Lock()
			a.st.features[key] = checked
			a.st.mu.Unlock()
		}
		return
	}
	switch id {
	case wBaseURL, wAPIKey, wTimeout:
		if ev == evTextChanged {
			a.invalidateModels(true)
		}
		return
	}
	if ev != evAction {
		return
	}
	switch id {
	case wProvider:
		a.providerChanged()
	case wListModels:
		a.fetchModels()
	case wRun:
		a.startRun()
	case wStop:
		a.stop()
	case wExport:
		a.exportCSV()
	case wClear:
		a.clearDisplay()
	case wHistoryRefresh:
		a.reloadHistory()
	case wHistoryClear:
		a.clearHistory()
	}
}

// closing releases background work when the window closes or the app quits.
func (a *macApp) closing() {
	a.st.listing.close()
	a.st.mu.Lock()
	if a.st.cancel != nil {
		a.st.cancel()
	}
	a.st.mu.Unlock()
}

func (a *macApp) providerChanged() {
	idx := a.v.SelectedIndex(wProvider)
	types := checker.ProviderTypes()
	if idx < 0 || idx >= len(types) {
		return
	}
	a.st.mu.Lock()
	if a.st.providerKey == types[idx] {
		// Re-selecting the same item is not a change (NSPopUpButton still fires).
		a.st.mu.Unlock()
		return
	}
	a.st.providerKey = types[idx]
	a.st.mu.Unlock()
	a.refreshChecks()
	a.refreshReasoning()
	a.invalidateModels(true)
	a.refreshStatus()
}

// refreshChecks enables/disables checkboxes based on provider support and
// drops unsupported selections from the state.
func (a *macApp) refreshChecks() {
	a.st.mu.Lock()
	pk := a.st.providerKey
	a.st.mu.Unlock()
	c, ok := checker.AllCheckers[pk]
	if !ok {
		return
	}
	for i, f := range checker.UnifiedFeatures {
		supported := checker.Supports(c, f.Key)
		a.v.SetEnabled(checkID(i), supported)
		if !supported {
			a.v.SetChecked(checkID(i), false)
			a.st.mu.Lock()
			a.st.features[f.Key] = false
			a.st.mu.Unlock()
		}
	}
}

// syncChecks sets checkbox states from st.features (used by history load).
func (a *macApp) syncChecks() {
	for i, f := range checker.UnifiedFeatures {
		a.st.mu.Lock()
		want := a.st.features[f.Key]
		a.st.mu.Unlock()
		if a.v.Enabled(checkID(i)) {
			a.v.SetChecked(checkID(i), want)
		}
	}
}

func (a *macApp) refreshReasoning() {
	a.st.mu.Lock()
	pk := a.st.providerKey
	a.st.mu.Unlock()
	effortOK, modeOK := reasoningControls(pk)
	a.v.SetEnabled(wEffort, effortOK)
	a.v.SetEnabled(wMode, modeOK)
}

func (a *macApp) refreshStatus() {
	a.st.mu.Lock()
	pk, running := a.st.providerKey, a.st.running
	a.st.mu.Unlock()
	a.v.SetStatus(statusLine(pk, running))
}

func (a *macApp) setSummary(text string, t tone) {
	if text == "" {
		text = "尚未运行检测 / No run yet."
		t = toneMuted
	}
	a.v.SetSummary(text, t)
}

// log appends a timestamped line to the log tab.
func (a *macApp) log(line string) {
	a.v.AppendLog(a.now().Format("15:04:05") + "  " + line)
}

func (a *macApp) selectedReasoning() (effort, mode string) {
	if i := a.v.SelectedIndex(wEffort); i >= 0 && i < len(checker.ReasoningEfforts) {
		effort = checker.ReasoningEfforts[i]
	}
	if i := a.v.SelectedIndex(wMode); i >= 0 && i < len(checker.ReasoningModes) {
		mode = checker.ReasoningModes[i]
	}
	return effort, mode
}

func (a *macApp) timeout() time.Duration {
	timeoutSec := 60
	if n, err := parseIntDefault(a.v.Text(wTimeout), 60); err == nil {
		timeoutSec = n
	}
	return time.Duration(timeoutSec) * time.Second
}

func (a *macApp) setRunControls(running bool) {
	a.v.SetEnabled(wRun, !running)
	a.v.SetEnabled(wStop, running)
	a.v.SetEnabled(wListModels, !running && !a.st.listing.closed)
}

// startRun kicks off a provider check in a goroutine.
func (a *macApp) startRun() {
	a.st.mu.Lock()
	if a.st.running {
		a.st.mu.Unlock()
		return
	}
	pk := a.st.providerKey
	feats := selectedFeatureKeys(a.st.features)
	a.st.results = nil
	a.st.mu.Unlock()

	if len(feats) == 0 {
		a.v.Alert(alertWarning, "No features selected", "Please select at least one capability to check.")
		return
	}
	c, ok := checker.AllCheckers[pk]
	if !ok {
		a.v.Alert(alertError, "Unknown provider", "Selected provider type is not registered.")
		return
	}
	model := a.v.Text(wModel)
	if model == "" {
		a.v.Alert(alertWarning, "Model required", "Please enter a model name.")
		return
	}
	prompt := a.v.Text(wPrompt)
	effort, mode := a.selectedReasoning()
	effort, mode = effectiveReasoning(pk, effort, mode)
	cfg := checker.Config{
		BaseURL:         a.v.Text(wBaseURL),
		APIKey:          a.v.Text(wAPIKey),
		Model:           model,
		Timeout:         a.timeout(),
		ReasoningEffort: effort,
		ReasoningMode:   mode,
	}

	record := newRunRecord(pk, cfg, prompt, feats)
	a.invalidateModels(false)
	ctx, cancel := context.WithCancel(context.Background())
	a.st.mu.Lock()
	a.st.running = true
	a.st.cancel = cancel
	a.st.mu.Unlock()
	a.setRunControls(true)
	a.refreshStatus()
	a.setSummary("Running...  正在检测", tonePending)
	a.log(runStartLogLine(pk, model, feats, effort, mode))
	a.results = nil
	a.v.ReloadTable(tableResults)

	go a.run(ctx, c, record)
}

// run executes on its own goroutine. record is confined to it until "done";
// every view update is marshalled to the main thread.
func (a *macApp) run(ctx context.Context, c checker.Checker, record *runRecord) {
	runner.Run(ctx, c, record.cfg, record.features, record.prompt, func(ev runner.Event) {
		switch ev.Type {
		case "start":
			line := fmt.Sprintf("Run started: %d features", ev.Total)
			a.v.Synchronize(func() { a.log(line) })
		case "feature_start":
			line := featureStartLogLine(ev.Index, ev.Total, ev.Feature)
			a.v.Synchronize(func() { a.log(line) })
		case "feature_done":
			r := ev.Result
			r.Name = checker.FeatureName(ev.Feature)
			record.results = append(record.results, r)
			line := featureDoneLogLine(ev.Index, ev.Total, r)
			a.v.Synchronize(func() {
				a.results = append(a.results, r)
				a.v.ReloadTable(tableResults)
				a.log(line)
			})
		case "done":
			snap := append([]checker.FeatureResult(nil), record.results...)
			a.v.Synchronize(func() { a.finishRun(record, snap) })
		}
	})
}

func (a *macApp) finishRun(record *runRecord, snap []checker.FeatureResult) {
	a.st.mu.Lock()
	a.st.results = snap
	cancel := a.st.cancel
	a.st.running = false
	a.st.cancel = nil
	a.st.mu.Unlock()
	if cancel != nil {
		cancel() // release the run context
	}

	p, f, s := runner.Summary(snap)
	a.log(fmt.Sprintf("Run finished: PASS=%d FAIL=%d SKIP=%d", p, f, s))
	a.setRunControls(false)
	a.refreshStatus()

	style, t := alertInfo, tonePass
	switch outcomeOf(p, f) {
	case outcomeFail:
		style, t = alertError, toneFail
	case outcomeMixed:
		style, t = alertWarning, tonePending
	}
	a.setSummary(runDoneSummary(p, f, s, record.cfg.Model), t)

	// Persist to history (best effort). Cancelled runs are still recorded.
	a.saveHistory(record)
	a.v.Alert(style, "Check Complete", fmt.Sprintf("PASS=%d   FAIL=%d   SKIP=%d", p, f, s))
}

func (a *macApp) stop() {
	a.st.mu.Lock()
	if a.st.cancel != nil {
		a.st.cancel()
	}
	a.st.mu.Unlock()
}

// clearDisplay clears the visible table, log and summary; run records and
// persisted history keep every result.
func (a *macApp) clearDisplay() {
	a.results = nil
	a.v.ReloadTable(tableResults)
	a.v.ClearLog()
	a.setSummary("", toneMuted)
}

// exportCSV writes the visible results to a CSV file chosen by the user.
func (a *macApp) exportCSV() {
	if len(a.results) == 0 {
		a.v.Alert(alertInfo, "Nothing to export", "Run a check first.")
		return
	}
	results := append([]checker.FeatureResult(nil), a.results...)
	path, ok := a.v.SaveFile("Save results as CSV", defaultCSVName(a.now()))
	if !ok || path == "" {
		return
	}
	if err := writeResultsCSV(path, results); err != nil {
		a.v.Alert(alertError, "Export failed", err.Error())
		return
	}
	a.v.Alert(alertInfo, "Exported", "Saved to "+path)
}

func (a *macApp) setRecords(recs []history.Record) {
	a.records = recs
	a.v.ReloadTable(tableHistory)
}

// saveHistory records the completed run through the shared store.
func (a *macApp) saveHistory(record *runRecord) {
	store := a.st.stores.ensure(a.openStore)
	if store == nil {
		a.log("history unavailable: store initialization failed; will retry on next save")
		return
	}
	rec := record.historyRecord(a.now(), a.encryptAPIKey(record.cfg.APIKey))
	if err := store.Add(rec); err != nil {
		a.log("history save failed: " + err.Error())
		return
	}
	a.setRecords(store.Records())
}

// encryptAPIKey encrypts an API key for at-rest storage, logging and returning
// an empty string on failure so a run is never blocked by a Keychain error.
func (a *macApp) encryptAPIKey(apiKey string) string {
	enc, err := a.encrypt(apiKey)
	if err != nil {
		a.log("API key encrypt failed (not stored): " + err.Error())
		return ""
	}
	return enc
}

func (a *macApp) reloadHistory() {
	store := a.st.stores.ensure(a.openStore)
	if store == nil {
		return
	}
	a.setRecords(store.Records())
}

func (a *macApp) clearHistory() {
	store := a.st.stores.ensure(a.openStore)
	if store == nil {
		return
	}
	if !a.v.Confirm("Clear History", "确定清空全部历史记录？/ Clear all history?") {
		return
	}
	if err := store.Clear(); err != nil {
		a.v.Alert(alertError, "Clear failed", err.Error())
		return
	}
	a.setRecords(store.Records())
}

// loadHistoryRow fills the form from the history record at row.
func (a *macApp) loadHistoryRow(row int) {
	if row < 0 || row >= len(a.records) {
		return
	}
	rec := a.records[row]
	idx := providerIndex(rec.Provider)
	if idx < 0 {
		return
	}
	a.invalidateModels(true)
	a.st.mu.Lock()
	a.st.providerKey = rec.Provider
	a.st.mu.Unlock()
	a.v.SelectIndex(wProvider, idx)

	a.v.SetText(wBaseURL, rec.BaseURL)
	a.v.SetText(wModel, rec.Model)
	plain, keyErr := a.decrypt(rec.APIKeyEnc)
	if keyErr == nil {
		a.v.SetText(wAPIKey, plain)
	} else {
		a.v.SetText(wAPIKey, "")
		a.log("API key decrypt failed: " + keyErr.Error())
	}
	a.v.SetText(wPrompt, rec.Prompt)
	if rec.TimeoutSec > 0 {
		a.v.SetText(wTimeout, fmt.Sprintf("%d", rec.TimeoutSec))
	}
	a.v.SelectIndex(wEffort, indexOfString(checker.ReasoningEfforts, rec.ReasoningEffort))
	a.v.SelectIndex(wMode, indexOfString(checker.ReasoningModes, rec.ReasoningMode))

	a.st.mu.Lock()
	for _, f := range checker.UnifiedFeatures {
		a.st.features[f.Key] = false
	}
	for _, k := range rec.Features {
		if c := checker.AllCheckers[rec.Provider]; c != nil && checker.Supports(c, k) {
			a.st.features[k] = true
		}
	}
	running := a.st.running
	a.st.mu.Unlock()
	a.refreshChecks()
	a.syncChecks()
	a.refreshReasoning()
	a.refreshStatus()

	a.log("Loaded config from history: " + rec.Time.Format("2006-01-02 15:04:05"))
	if !running {
		a.setSummary("已载入历史配置 / Loaded history config: "+rec.Model, tonePending)
	}
	switch {
	case rec.APIKeyEnc == "":
		a.v.Alert(alertInfo, "History Loaded", "已回填该次测试配置（该记录未保存 API Key）。\nConfig loaded (no API key was stored for this run).")
	case keyErr != nil:
		a.v.Alert(alertWarning, "History Loaded", "已回填该次测试配置，但 API Key 无法从钥匙串解密，请重新输入。\nConfig loaded, but the stored API key could not be decrypted; please re-enter it.")
	default:
		a.v.Alert(alertInfo, "History Loaded", "已回填该次测试配置（含加密保存的 API Key，已解密回填）。\nConfig loaded (API key was restored from encrypted storage).")
	}
}

// fetchModels queries the selected provider for its available models. The
// current model text is preserved so a manually typed name is not lost.
func (a *macApp) fetchModels() {
	a.st.mu.Lock()
	pk, running := a.st.providerKey, a.st.running
	a.st.mu.Unlock()
	if running {
		return
	}
	c, ok := checker.AllCheckers[pk]
	if !ok {
		a.v.Alert(alertError, "Unknown provider", "Selected provider type is not registered.")
		return
	}
	if !checker.SupportsListModels(c) {
		a.v.Alert(alertInfo, "Not supported", "该提供商暂不支持列出模型。\nThis provider does not support listing models.")
		return
	}
	cfg := checker.Config{BaseURL: a.v.Text(wBaseURL), APIKey: a.v.Text(wAPIKey), Timeout: a.timeout()}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	generation := a.st.listing.begin(cancel)
	a.v.SetEnabled(wListModels, false)
	a.log("Listing models for " + checker.ProviderLabel(pk) + " ...")

	go func() {
		defer cancel()
		models, err := checker.ListModels(ctx, c, cfg)
		a.v.Synchronize(func() { a.completeModelListing(generation, models, err) })
	}()
}

// completeModelListing delivers a result only while its request still owns the form.
func (a *macApp) completeModelListing(generation uint64, models []string, err error) {
	if !a.st.listing.current(generation) {
		return
	}
	a.st.listing.cancel = nil
	a.v.SetEnabled(wListModels, true)
	if err != nil {
		a.log("List models failed: " + err.Error())
		a.v.Alert(alertError, "List models failed", err.Error())
		return
	}
	if len(models) == 0 {
		a.log("List models: no models returned.")
		a.v.Alert(alertInfo, "No models", "服务商未返回任何模型。\nThe provider returned no models.")
		return
	}
	a.models = append([]string(nil), models...)
	a.v.SetItems(wModel, a.models) // keeps the latest typed text
	a.log(fmt.Sprintf("List models: %d models loaded.", len(models)))
	a.setSummary(fmt.Sprintf("已加载 %d 个模型 / Loaded %d models", len(models), len(models)), tonePending)
}

// invalidateModels is called on the main thread for any listing ownership change.
func (a *macApp) invalidateModels(clearList bool) {
	a.st.listing.invalidate()
	if clearList {
		a.models = nil
		a.v.SetItems(wModel, nil)
	}
	a.st.mu.Lock()
	running := a.st.running
	a.st.mu.Unlock()
	a.v.SetEnabled(wListModels, !running && !a.st.listing.closed)
}

// tableRows returns the row count of a table.
func (a *macApp) tableRows(t tableID) int {
	switch t {
	case tableResults:
		return len(a.results)
	case tableHistory:
		return len(a.records)
	}
	return 0
}

// tableCell returns the text and semantic style of a table cell.
func (a *macApp) tableCell(t tableID, row, col int) (string, cellStyle) {
	switch t {
	case tableResults:
		if row < 0 || row >= len(a.results) {
			return "", cellPlain
		}
		r := a.results[row]
		text, _ := resultCellText(r, col)
		if col != 1 {
			return text, cellPlain
		}
		switch r.Status {
		case checker.StatusPass:
			return text, cellPillPass
		case checker.StatusFail:
			return text, cellPillFail
		case checker.StatusSkip:
			return text, cellPillSkip
		}
		return text, cellPillPending
	case tableHistory:
		if row < 0 || row >= len(a.records) {
			return "", cellPlain
		}
		r := a.records[row]
		text, _ := historyCellText(r, col)
		if col != 4 {
			return text, cellPlain
		}
		switch {
		case r.Fail > 0:
			return text, cellTextFail
		case r.Pass > 0:
			return text, cellTextPass
		}
		return text, cellTextMuted
	}
	return "", cellPlain
}
