//go:build windows

package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"sync"
	"time"

	"provider-checker/checker"
	"provider-checker/history"
	"provider-checker/runner"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

// Fonts used across the UI. Segoe UI is the modern Windows UI font.
var (
	fontUI      = Font{Family: "Segoe UI", PointSize: 9}
	fontUIBold  = Font{Family: "Segoe UI", PointSize: 9, Bold: true}
	fontHeader  = Font{Family: "Segoe UI", PointSize: 10, Bold: true}
	fontSummary = Font{Family: "Segoe UI", PointSize: 11, Bold: true}
	fontMono    = Font{Family: "Consolas", PointSize: 9}
)

// stringListModel implements walk.ListModel for a []string.
type stringListModel struct {
	walk.ListModelBase
	items []string
}

func (m *stringListModel) ItemCount() int { return len(m.items) }
func (m *stringListModel) Value(index int) interface{} {
	if index < 0 || index >= len(m.items) {
		return nil
	}
	return m.items[index]
}

// uiRefs bundles the widgets the run/load logic needs to touch.
type uiRefs struct {
	mw           *walk.MainWindow
	cbProvider   *walk.ComboBox
	leBaseURL    *walk.LineEdit
	leAPIKey     *walk.LineEdit
	cbModel      *walk.ComboBox
	modelModel   *stringListModel
	btnModels    *walk.PushButton
	lePrompt     *walk.LineEdit
	leTimeout    *walk.LineEdit
	cbEffort     *walk.ComboBox
	cbMode       *walk.ComboBox
	btnRun       *walk.PushButton
	btnStop      *walk.PushButton
	cbChecks     []*walk.CheckBox
	resultsModel *resultModel
	historyModel *historyModel
	teLog        *walk.TextEdit
	sbiStatus    *walk.StatusBarItem
	lblSummary   *walk.Label
	compSummary  *walk.Composite
}

// appState holds the mutable UI state shared between the UI goroutine and the run goroutine.
type appState struct {
	mu          sync.Mutex
	providerKey string // selected provider type key
	features    map[string]bool
	running     bool
	cancel      context.CancelFunc
	results     []checker.FeatureResult
	store       *history.Store
}

func main() {
	st := &appState{
		providerKey: checker.ProviderTypes()[0],
		features:    map[string]bool{},
	}
	// Default-select Basic for convenience.
	st.features["basic"] = true

	providerTypes := checker.ProviderTypes()
	providerLabels := make([]string, len(providerTypes))
	for i, t := range providerTypes {
		providerLabels[i] = checker.ProviderLabel(t)
	}
	provModel := &stringListModel{items: providerLabels}
	effortModel := &stringListModel{items: reasoningEffortLabels()}
	modeModel := &stringListModel{items: reasoningModeLabels()}

	u := &uiRefs{
		resultsModel: newResultModel(),
		historyModel: newHistoryModel(),
		modelModel:   &stringListModel{items: []string{}},
	}

	var (
		btnExport *walk.PushButton
		btnClear  *walk.PushButton
		tvResults *walk.TableView
		tvHistory *walk.TableView
	)

	// helper: build a labeled field row (Label + input widget) inside a Grid.
	mkField := func(label string, w Widget) []Widget {
		return []Widget{
			Label{Text: label, TextColor: colTextMuted, Font: fontUI},
			w,
		}
	}

	mwCfg := MainWindow{
		AssignTo: &u.mw,
		Title:    "LLM Provider Checker",
		MinSize:  Size{Width: 960, Height: 640},
		Size:     Size{Width: 960, Height: 660},
		Font:     fontUI,
		StatusBarItems: []StatusBarItem{
			{AssignTo: &u.sbiStatus, Text: "Ready.", Width: 0},
		},
		Layout: VBox{Margins: Margins{Left: 0, Top: 0, Right: 0, Bottom: 0}, Spacing: 0},
		Children: []Widget{
			// ---- Slim title bar (no banner image) ----
			Composite{
				Background: solidBrush(colBgHeader),
				Layout:     HBox{Margins: Margins{Left: 16, Top: 7, Right: 16, Bottom: 7}, Spacing: 10},
				Children: []Widget{
					Label{Text: "LLM Provider Checker", Font: fontHeader, TextColor: colTextOnDark},
					Label{Text: "一键检测大模型服务可用性与能力", Font: fontUI, TextColor: 0xB6C2D1},
					HSpacer{},
				},
			},

			// ---- Body ----
			Composite{
				Background: solidBrush(colBgPage),
				Layout:     VBox{Margins: Margins{Left: 12, Top: 8, Right: 12, Bottom: 8}, Spacing: 8},
				Children: []Widget{
					// ---- Connection settings (two fields per row) ----
					GroupBox{
						Title:  "  Provider Settings  /  提供商设置  ",
						Font:   fontHeader,
						Layout: Grid{Columns: 4, Spacing: 7, Margins: Margins{Left: 10, Top: 14, Right: 10, Bottom: 8}},
						Children: concat(
							mkField("Type / 类型", ComboBox{
								AssignTo: &u.cbProvider,
								Model:    provModel,
								MinSize:  Size{Width: 180, Height: 0},
								OnCurrentIndexChanged: func() {
									if u.cbProvider != nil {
										idx := u.cbProvider.CurrentIndex()
										if idx >= 0 && idx < len(providerTypes) {
											st.mu.Lock()
											st.providerKey = providerTypes[idx]
											st.mu.Unlock()
											refreshChecks(u.cbChecks, st)
											refreshStatus(u.sbiStatus, st)
										}
									}
								},
							}),
							mkField("Model", Composite{
								Layout: HBox{MarginsZero: true, Spacing: 6},
								Children: []Widget{
									ComboBox{
										AssignTo:    &u.cbModel,
										Editable:    true,
										Model:       u.modelModel,
										ToolTipText: "输入模型名称，或点击“列出模型”从服务商拉取可用模型后下拉选择。",
									},
									PushButton{
										AssignTo:    &u.btnModels,
										Text:        "列出模型 / List",
										Font:        fontUI,
										MinSize:     Size{Width: 110, Height: 0},
										ToolTipText: "从服务商 /models 接口拉取可用模型列表",
										OnClicked:   func() { fetchModels(u, st) },
									},
								},
							}),
							mkField("Base URL", LineEdit{AssignTo: &u.leBaseURL, CueBanner: "https://api.openai.com  (empty = default)"}),
							mkField("API Key / Auth Token", LineEdit{
								AssignTo:     &u.leAPIKey,
								CueBanner:    "sk-... / token...",
								PasswordMode: true,
								ToolTipText:  "Anthropic 同时支持 API Key (x-api-key) 与 Auth Token (Bearer，401 时自动重试)。",
							}),
							mkField("Prompt (optional)", LineEdit{AssignTo: &u.lePrompt, CueBanner: "Say hello in one short sentence."}),
							mkField("Timeout (s)", LineEdit{AssignTo: &u.leTimeout, Text: "60"}),
							mkField("Reasoning Effort / 推理强度", ComboBox{
								AssignTo:     &u.cbEffort,
								Model:        effortModel,
								CurrentIndex: 0,
								MinSize:      Size{Width: 180, Height: 0},
								ToolTipText:  "OpenAI reasoning.effort（low/medium/high）。留空使用服务商默认（medium）。",
							}),
							mkField("Reasoning Mode / 推理模式", ComboBox{
								AssignTo:     &u.cbMode,
								Model:        modeModel,
								CurrentIndex: 0,
								MinSize:      Size{Width: 180, Height: 0},
								ToolTipText:  "Responses API reasoning.mode。选择 pro 启用专业模式（保持所选模型别名不变）。",
							}),
						),
					},

					// ---- Feature checkboxes ----
					GroupBox{
						Title:    "  Capabilities to Check  /  待检测能力 (multi-select)  ",
						Font:     fontHeader,
						Layout:   Grid{Columns: 4, Spacing: 6, Margins: Margins{Left: 10, Top: 14, Right: 10, Bottom: 8}},
						Children: buildCheckWidgets(&u.cbChecks, st),
					},

					// ---- Action buttons ----
					Composite{
						Layout: HBox{MarginsZero: true, Spacing: 8},
						Children: []Widget{
							PushButton{
								AssignTo: &u.btnRun, Text: "  \u25B6  Run Check  ", Font: fontUIBold,
								MinSize: Size{Width: 140, Height: 32},
								OnClicked: func() {
									startRun(u, st)
								},
							},
							PushButton{AssignTo: &u.btnStop, Text: "  \u25A0  Stop  ", Font: fontUI, Enabled: false, MinSize: Size{Width: 110, Height: 32},
								OnClicked: func() {
									st.mu.Lock()
									if st.cancel != nil {
										st.cancel()
									}
									st.mu.Unlock()
								}},
							PushButton{AssignTo: &btnExport, Text: "\U0001F4BE  Export CSV", Font: fontUI, MinSize: Size{Width: 120, Height: 32},
								OnClicked: func() { exportCSV(u.mw, u.resultsModel.snapshot()) }},
							PushButton{AssignTo: &btnClear, Text: "Clear", Font: fontUI, MinSize: Size{Width: 90, Height: 32},
								OnClicked: func() {
									u.resultsModel.reset()
									if u.teLog != nil {
										u.teLog.SetText("")
									}
									setSummary(u, "", colTextMuted)
								}},
							HSpacer{},
						},
					},

					// ---- Summary strip ----
					Composite{
						AssignTo:   &u.compSummary,
						Background: solidBrush(colSummaryBg),
						Layout:     HBox{Margins: Margins{Left: 14, Top: 8, Right: 14, Bottom: 8}, Spacing: 8},
						Children: []Widget{
							Label{AssignTo: &u.lblSummary, Text: "尚未运行检测 / No run yet.", Font: fontSummary, TextColor: colTextMuted},
							HSpacer{},
						},
					},

					// ---- Tabs: Results / Log / History ----
					TabWidget{
						MinSize: Size{Height: 260},
						Pages: []TabPage{
							{
								Title:  "Results / 结果",
								Layout: VBox{Margins: Margins{Left: 6, Top: 6, Right: 6, Bottom: 6}},
								Children: []Widget{
									TableView{
										AssignTo:         &tvResults,
										Model:            u.resultsModel,
										ColumnsOrderable: true,
										ColumnsSizable:   true,
										Columns: []TableViewColumn{
											{Title: "Feature", Width: 180},
											{Title: "Status", Width: 80, Alignment: AlignCenter},
											{Title: "Latency", Width: 90, Alignment: AlignCenter},
											{Title: "Upstream Model / 上游响应模型", Width: 220},
											{Title: "Detail", Width: 320},
											{Title: "Error", Width: 280},
										},
										StyleCell: func(style *walk.CellStyle) {
											styleCell(tvResults, u.resultsModel, style)
										},
									},
								},
							},
							{
								Title:  "Log / 日志",
								Layout: VBox{Margins: Margins{Left: 6, Top: 6, Right: 6, Bottom: 6}},
								Children: []Widget{
									TextEdit{AssignTo: &u.teLog, ReadOnly: true, Font: fontMono, VScroll: true},
								},
							},
							{
								Title:  "History / 历史",
								Layout: VBox{Margins: Margins{Left: 6, Top: 6, Right: 6, Bottom: 6}, Spacing: 6},
								Children: []Widget{
									Composite{
										Layout: HBox{MarginsZero: true, Spacing: 8},
										Children: []Widget{
											Label{Text: "双击历史记录可回填该次测试配置 / Double-click a row to load its config", Font: fontUI, TextColor: colTextMuted},
											HSpacer{},
											PushButton{Text: "Refresh / 刷新", Font: fontUI, MinSize: Size{Width: 90, Height: 26},
												OnClicked: func() { reloadHistory(u, st) }},
											PushButton{Text: "Clear History / 清空", Font: fontUI, MinSize: Size{Width: 110, Height: 26},
												OnClicked: func() { clearHistory(u, st) }},
										},
									},
									TableView{
										AssignTo:         &tvHistory,
										Model:            u.historyModel,
										ColumnsOrderable: true,
										ColumnsSizable:   true,
										Columns: []TableViewColumn{
											{Title: "Time", Width: 150},
											{Title: "Provider", Width: 200},
											{Title: "Model", Width: 180},
											{Title: "Features", Width: 220},
											{Title: "P/F/S", Width: 80, Alignment: AlignCenter},
											{Title: "Reasoning", Width: 130},
										},
										StyleCell: func(style *walk.CellStyle) {
											styleHistoryCell(u.historyModel, style)
										},
										OnItemActivated: func() {
											if tvHistory != nil {
												loadHistoryRow(u, st, tvHistory.CurrentIndex())
											}
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Create the window, then finish post-create wiring on the UI thread.
	if err := mwCfg.Create(); err != nil {
		fmt.Fprintln(os.Stderr, "GUI error:", err)
		os.Exit(1)
	}

	// Enable/disable capability checkboxes for the default provider and mark Basic checked.
	refreshChecks(u.cbChecks, st)
	if len(u.cbChecks) > 0 && u.cbChecks[0] != nil {
		u.cbChecks[0].SetChecked(true)
	}
	refreshStatus(u.sbiStatus, st)

	// Load history in the background so the window can paint immediately.
	go func() {
		path, err := history.DefaultPath()
		if err != nil {
			return
		}
		store, oerr := history.Open(path)
		if oerr != nil {
			return
		}
		st.mu.Lock()
		st.store = store
		st.mu.Unlock()
		if u.mw != nil {
			u.mw.Synchronize(func() {
				u.historyModel.setRecords(store.Records())
			})
		}
	}()

	u.mw.Run()
}

// solidBrush returns a solid-color brush suitable for Background fields.
func solidBrush(color walk.Color) Brush {
	return SolidColorBrush{Color: color}
}

// concat flattens multiple widget slices into one.
func concat(parts ...[]Widget) []Widget {
	var out []Widget
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// styleCell customizes cell appearance: alternating rows + colored status pill.
func styleCell(tv *walk.TableView, m *resultModel, style *walk.CellStyle) {
	row := style.Row()
	if row < 0 || row >= m.RowCount() {
		return
	}
	// Alternating row background for readability.
	if row%2 == 1 {
		style.BackgroundColor = 0xEFF1F5
	}
	// Status column coloring (col 1).
	if style.Col() == 1 {
		val, _ := m.Value(row, 1).(string)
		bg, fg := statusCellColors(val)
		style.BackgroundColor = bg
		style.TextColor = fg
		style.Font, _ = walk.NewFont("Segoe UI", 9, walk.FontBold)
	}
}

// buildCheckWidgets creates a checkbox per unified feature and wires it into state.
func buildCheckWidgets(cbChecks *[]*walk.CheckBox, st *appState) []Widget {
	*cbChecks = make([]*walk.CheckBox, 0, len(checker.UnifiedFeatures))
	widgets := make([]Widget, 0, len(checker.UnifiedFeatures))
	for _, f := range checker.UnifiedFeatures {
		key := f.Key
		name := f.Name
		var cb *walk.CheckBox
		w := CheckBox{
			AssignTo: &cb,
			Text:     name,
			Font:     fontUI,
			OnClicked: func() {
				if cb != nil {
					st.mu.Lock()
					st.features[key] = cb.Checked()
					st.mu.Unlock()
				}
			},
		}
		*cbChecks = append(*cbChecks, cb)
		widgets = append(widgets, w)
	}
	return widgets
}

// refreshChecks enables/disables checkboxes based on provider support.
func refreshChecks(cbChecks []*walk.CheckBox, st *appState) {
	st.mu.Lock()
	pk := st.providerKey
	st.mu.Unlock()
	c, ok := checker.AllCheckers[pk]
	if !ok {
		return
	}
	for i, cb := range cbChecks {
		if i >= len(checker.UnifiedFeatures) {
			break
		}
		key := checker.UnifiedFeatures[i].Key
		supported := checker.Supports(c, key)
		if cb != nil {
			cb.SetEnabled(supported)
			if !supported {
				cb.SetChecked(false)
				st.mu.Lock()
				st.features[key] = false
				st.mu.Unlock()
			}
		}
	}
}

// syncChecks sets checkbox checked-state from st.features (used by history load).
func syncChecks(cbChecks []*walk.CheckBox, st *appState) {
	for i, cb := range cbChecks {
		if i >= len(checker.UnifiedFeatures) {
			break
		}
		key := checker.UnifiedFeatures[i].Key
		st.mu.Lock()
		want := st.features[key]
		st.mu.Unlock()
		if cb != nil && cb.Enabled() {
			cb.SetChecked(want)
		}
	}
}

func refreshStatus(sbi *walk.StatusBarItem, st *appState) {
	st.mu.Lock()
	pk := st.providerKey
	running := st.running
	st.mu.Unlock()
	c, ok := checker.AllCheckers[pk]
	supCount := 0
	if ok {
		supCount = len(c.Supported())
	}
	state := "Ready"
	if running {
		state = "Running..."
	}
	msg := fmt.Sprintf("  %s  \u00b7  Provider: %s  \u00b7  Capabilities: %d  ", state, checker.ProviderLabel(pk), supCount)
	if sbi != nil {
		sbi.SetText(msg)
	}
}

// setSummary updates the summary strip text/color (UI thread only).
func setSummary(u *uiRefs, text string, color walk.Color) {
	if u.lblSummary == nil {
		return
	}
	if text == "" {
		text = "尚未运行检测 / No run yet."
		color = colTextMuted
	}
	u.lblSummary.SetText(text)
	u.lblSummary.SetTextColor(color)
}

// appendLog appends a timestamped line to the log textbox (must run on UI thread).
func appendLog(te *walk.TextEdit, line string) {
	if te == nil {
		return
	}
	ts := time.Now().Format("15:04:05")
	cur := te.Text()
	if cur != "" {
		cur += "\r\n"
	}
	te.SetText(cur + ts + "  " + line)
}

// selectedReasoning reads the current effort/mode values from the combos.
func selectedReasoning(u *uiRefs) (effort, mode string) {
	if u.cbEffort != nil {
		if i := u.cbEffort.CurrentIndex(); i >= 0 && i < len(checker.ReasoningEfforts) {
			effort = checker.ReasoningEfforts[i]
		}
	}
	if u.cbMode != nil {
		if i := u.cbMode.CurrentIndex(); i >= 0 && i < len(checker.ReasoningModes) {
			mode = checker.ReasoningModes[i]
		}
	}
	return
}

// startRun kicks off a provider check in a goroutine.
func startRun(u *uiRefs, st *appState) {
	st.mu.Lock()
	if st.running {
		st.mu.Unlock()
		return
	}
	pk := st.providerKey
	feats := []string{}
	for _, f := range checker.UnifiedFeatures {
		if st.features[f.Key] {
			feats = append(feats, f.Key)
		}
	}
	st.results = nil
	st.mu.Unlock()

	if len(feats) == 0 {
		walk.MsgBox(u.mw, "No features selected", "Please select at least one capability to check.", walk.MsgBoxIconWarning)
		return
	}

	c, ok := checker.AllCheckers[pk]
	if !ok {
		walk.MsgBox(u.mw, "Unknown provider", "Selected provider type is not registered.", walk.MsgBoxIconError)
		return
	}

	baseURL := u.leBaseURL.Text()
	apiKey := u.leAPIKey.Text()
	model := u.cbModel.Text()
	prompt := u.lePrompt.Text()
	timeoutSec := 60
	if n, err := parseIntDefault(u.leTimeout.Text(), 60); err == nil {
		timeoutSec = n
	}
	if model == "" {
		walk.MsgBox(u.mw, "Model required", "Please enter a model name.", walk.MsgBoxIconWarning)
		return
	}

	effort, mode := selectedReasoning(u)
	cfg := checker.Config{
		BaseURL:         baseURL,
		APIKey:          apiKey,
		Model:           model,
		Timeout:         time.Duration(timeoutSec) * time.Second,
		ReasoningEffort: effort,
		ReasoningMode:   mode,
	}

	ctx, cancel := context.WithCancel(context.Background())
	st.mu.Lock()
	st.running = true
	st.cancel = cancel
	st.mu.Unlock()
	u.btnRun.SetEnabled(false)
	u.btnStop.SetEnabled(true)
	refreshStatus(u.sbiStatus, st)
	setSummary(u, "Running...  正在检测", colStatusInit)

	appendLog(u.teLog, fmt.Sprintf("Starting check: provider=%s model=%s features=%v reasoning=%s", checker.ProviderLabel(pk), model, feats, reasoningLabel(effort, mode)))
	u.resultsModel.reset()

	go func() {
		runner.Run(ctx, c, cfg, feats, prompt, func(ev runner.Event) {
			switch ev.Type {
			case "start":
				u.mw.Synchronize(func() {
					appendLog(u.teLog, fmt.Sprintf("Run started: %d features", ev.Total))
				})
			case "feature_start":
				u.mw.Synchronize(func() {
					appendLog(u.teLog, fmt.Sprintf("  -> [%d/%d] %s ...", ev.Index+1, ev.Total, checker.FeatureName(ev.Feature)))
				})
			case "feature_done":
				r := ev.Result
				r.Name = checker.FeatureName(ev.Feature)
				u.mw.Synchronize(func() {
					u.resultsModel.add(r)
					latency := ""
					if r.Latency > 0 {
						latency = fmt.Sprintf("%dms", r.Latency.Milliseconds())
					}
					line := fmt.Sprintf("  <- [%d/%d] %s: %s (%s)", ev.Index+1, ev.Total, r.Name, r.Status, latency)
					if r.Detail != "" {
						line += "  " + truncate(r.Detail, 80)
					}
					if r.UpstreamResponseModel != "" {
						line += "  upstream_model=" + truncate(r.UpstreamResponseModel, 120)
					}
					if r.Error != "" {
						line += "  err=" + truncate(r.Error, 120)
					}
					appendLog(u.teLog, line)
				})
			case "done":
				u.mw.Synchronize(func() {
					snap := u.resultsModel.snapshot()
					st.mu.Lock()
					st.results = snap
					st.mu.Unlock()
					p, f, s := runner.Summary(snap)
					appendLog(u.teLog, fmt.Sprintf("Run finished: PASS=%d FAIL=%d SKIP=%d", p, f, s))
					u.btnRun.SetEnabled(true)
					u.btnStop.SetEnabled(false)
					st.mu.Lock()
					st.running = false
					st.cancel = nil
					st.mu.Unlock()
					refreshStatus(u.sbiStatus, st)

					// Summary strip color reflects outcome.
					sumColor := walk.Color(colStatusPass)
					if f > 0 && p == 0 {
						sumColor = colStatusFail
					} else if f > 0 {
						sumColor = colStatusInit
					}
					setSummary(u, fmt.Sprintf("检测完成 / Done   PASS=%d   FAIL=%d   SKIP=%d   \u00b7   %s", p, f, s, model), sumColor)

					// Persist to history (best effort). Cancelled runs still recorded.
					saveHistory(u, st, cfg, pk, feats, p, f, s)

					icon := walk.MsgBoxIconInformation
					if f > 0 && p == 0 {
						icon = walk.MsgBoxIconError
					} else if f > 0 {
						icon = walk.MsgBoxIconWarning
					}
					walk.MsgBox(u.mw, "Check Complete", fmt.Sprintf("PASS=%d   FAIL=%d   SKIP=%d", p, f, s), icon)
				})
			}
		})
	}()
}

// saveHistory records the completed run and refreshes the history table.
func saveHistory(u *uiRefs, st *appState, cfg checker.Config, providerKey string, feats []string, pass, fail, skip int) {
	st.mu.Lock()
	store := st.store
	st.mu.Unlock()
	if store == nil {
		// Late-open fallback if background load has not finished yet.
		if path, err := history.DefaultPath(); err == nil {
			if s, oerr := history.Open(path); oerr == nil {
				st.mu.Lock()
				st.store = s
				store = s
				st.mu.Unlock()
			}
		}
	}
	if store == nil {
		return
	}
	prompt := ""
	if u.lePrompt != nil {
		prompt = u.lePrompt.Text()
	}
	rec := history.Record{
		Time:            time.Now(),
		Provider:        providerKey,
		ProviderLabel:   checker.ProviderLabel(providerKey),
		BaseURL:         cfg.BaseURL,
		Model:           cfg.Model,
		APIKeyEnc:       encryptAPIKey(u, cfg.APIKey),
		Prompt:          prompt,
		TimeoutSec:      int(cfg.Timeout / time.Second),
		ReasoningEffort: cfg.ReasoningEffort,
		ReasoningMode:   cfg.ReasoningMode,
		Features:        append([]string(nil), feats...),
		Pass:            pass,
		Fail:            fail,
		Skip:            skip,
	}
	if err := store.Add(rec); err != nil {
		appendLog(u.teLog, "history save failed: "+err.Error())
		return
	}
	u.historyModel.setRecords(store.Records())
}

// reloadHistory reloads records from the store into the table.
func reloadHistory(u *uiRefs, st *appState) {
	st.mu.Lock()
	store := st.store
	st.mu.Unlock()
	if store == nil {
		return
	}
	u.historyModel.setRecords(store.Records())
}

// clearHistory empties the store after confirmation.
func clearHistory(u *uiRefs, st *appState) {
	st.mu.Lock()
	store := st.store
	st.mu.Unlock()
	if store == nil {
		return
	}
	if walk.MsgBox(u.mw, "Clear History", "确定清空全部历史记录？/ Clear all history?", walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}
	if err := store.Clear(); err != nil {
		walk.MsgBox(u.mw, "Clear failed", err.Error(), walk.MsgBoxIconError)
		return
	}
	u.historyModel.setRecords(store.Records())
}

// loadHistoryRow fills the form fields from the selected history record.
func loadHistoryRow(u *uiRefs, st *appState, row int) {
	rec, ok := u.historyModel.recordAt(row)
	if !ok {
		return
	}

	// Provider dropdown -> also updates st.providerKey via OnCurrentIndexChanged.
	if idx := providerIndex(rec.Provider); idx >= 0 && u.cbProvider != nil {
		u.cbProvider.SetCurrentIndex(idx)
	}

	u.leBaseURL.SetText(rec.BaseURL)
	u.cbModel.SetText(rec.Model)
	if plain, err := decryptSecret(rec.APIKeyEnc); err == nil {
		u.leAPIKey.SetText(plain)
	} else {
		u.leAPIKey.SetText("")
		appendLog(u.teLog, "API key decrypt failed: "+err.Error())
	}
	u.lePrompt.SetText(rec.Prompt)
	if rec.TimeoutSec > 0 {
		u.leTimeout.SetText(fmt.Sprintf("%d", rec.TimeoutSec))
	}

	// Reasoning combos.
	if u.cbEffort != nil {
		u.cbEffort.SetCurrentIndex(indexOfString(checker.ReasoningEfforts, rec.ReasoningEffort))
	}
	if u.cbMode != nil {
		u.cbMode.SetCurrentIndex(indexOfString(checker.ReasoningModes, rec.ReasoningMode))
	}

	// Feature checkboxes: reset then apply record's features.
	st.mu.Lock()
	for _, f := range checker.UnifiedFeatures {
		st.features[f.Key] = false
	}
	for _, k := range rec.Features {
		st.features[k] = true
	}
	st.mu.Unlock()
	syncChecks(u.cbChecks, st)

	appendLog(u.teLog, "Loaded config from history: "+rec.Time.Format("2006-01-02 15:04:05"))
	setSummary(u, "已载入历史配置 / Loaded history config: "+rec.Model, colStatusInit)
	walk.MsgBox(u.mw, "History Loaded", "已回填该次测试配置（含加密保存的 API Key，已解密回填）。\nConfig loaded (API key was restored from encrypted storage).", walk.MsgBoxIconInformation)
}

// encryptAPIKey encrypts an API key for at-rest storage, logging and returning
// an empty string on failure so a run is never blocked by a crypto error.
func encryptAPIKey(u *uiRefs, apiKey string) string {
	enc, err := encryptSecret(apiKey)
	if err != nil {
		appendLog(u.teLog, "API key encrypt failed (not stored): "+err.Error())
		return ""
	}
	return enc
}

// fetchModels queries the selected provider for its available models and
// populates the editable model ComboBox. The current text is preserved so a
// manually typed model name is not lost.
func fetchModels(u *uiRefs, st *appState) {
	st.mu.Lock()
	pk := st.providerKey
	running := st.running
	st.mu.Unlock()
	if running {
		return
	}

	c, ok := checker.AllCheckers[pk]
	if !ok {
		walk.MsgBox(u.mw, "Unknown provider", "Selected provider type is not registered.", walk.MsgBoxIconError)
		return
	}
	if !checker.SupportsListModels(c) {
		walk.MsgBox(u.mw, "Not supported", "该提供商暂不支持列出模型。\nThis provider does not support listing models.", walk.MsgBoxIconInformation)
		return
	}

	timeoutSec := 60
	if n, err := parseIntDefault(u.leTimeout.Text(), 60); err == nil {
		timeoutSec = n
	}
	cfg := checker.Config{
		BaseURL: u.leBaseURL.Text(),
		APIKey:  u.leAPIKey.Text(),
		Timeout: time.Duration(timeoutSec) * time.Second,
	}
	current := u.cbModel.Text()

	if u.btnModels != nil {
		u.btnModels.SetEnabled(false)
	}
	appendLog(u.teLog, "Listing models for "+checker.ProviderLabel(pk)+" ...")

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
		defer cancel()
		models, err := checker.ListModels(ctx, c, cfg)
		u.mw.Synchronize(func() {
			if u.btnModels != nil {
				u.btnModels.SetEnabled(true)
			}
			if err != nil {
				appendLog(u.teLog, "List models failed: "+err.Error())
				walk.MsgBox(u.mw, "List models failed", err.Error(), walk.MsgBoxIconError)
				return
			}
			if len(models) == 0 {
				appendLog(u.teLog, "List models: no models returned.")
				walk.MsgBox(u.mw, "No models", "服务商未返回任何模型。\nThe provider returned no models.", walk.MsgBoxIconInformation)
				return
			}
			u.modelModel.items = models
			u.cbModel.SetModel(u.modelModel)
			// Preserve any manually typed model name.
			if current != "" {
				u.cbModel.SetText(current)
			}
			appendLog(u.teLog, fmt.Sprintf("List models: %d models loaded.", len(models)))
			setSummary(u, fmt.Sprintf("已加载 %d 个模型 / Loaded %d models", len(models), len(models)), colStatusInit)
		})
	}()
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

// exportCSV writes the given results to a CSV file chosen by the user.
func exportCSV(mw *walk.MainWindow, results []checker.FeatureResult) {
	if len(results) == 0 {
		walk.MsgBox(mw, "Nothing to export", "Run a check first.", walk.MsgBoxIconInformation)
		return
	}
	dlg := walk.FileDialog{
		Filter:   "CSV files (*.csv)|*.csv|All files (*.*)|*.*",
		FilePath: fmt.Sprintf("provider-check-%s.csv", time.Now().Format("20060102-150405")),
		Title:    "Save results as CSV",
	}
	if ok, _ := dlg.ShowSave(mw); !ok {
		return
	}
	path := dlg.FilePath
	if path == "" {
		return
	}
	if err := writeResultsCSV(path, results); err != nil {
		walk.MsgBox(mw, "Export failed", err.Error(), walk.MsgBoxIconError)
		return
	}
	walk.MsgBox(mw, "Exported", "Saved to "+path, walk.MsgBoxIconInformation)
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
