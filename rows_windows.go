//go:build windows

package main

import (
	"fmt"
	"strings"
	"sync"

	"provider-checker/checker"
	"provider-checker/history"

	"github.com/lxn/walk"
)

// Color palette (BGR-free: walk uses RGB via walk.RGB).
const (
	colBgPage     = 0xF7F8FA // light gray-blue page background
	colBgHeader   = 0x2C3E50 // dark slate for header bar
	colBgHeader2  = 0x1E2A38 // darker slate for gradient-ish depth
	colAccent     = 0x2563EB // blue accent (Run button)
	colAccentText = 0xFFFFFF
	colStatusPass = 0x16A34A // green
	colStatusFail = 0xDC2626 // red
	colStatusSkip = 0x9CA3AF // gray
	colStatusInit = 0xF59E0B // amber (running/pending)
	colTextMuted  = 0x6B7280
	colTextOnDark = 0xF3F4F6
	colAltRow     = 0xFFFFFF // alternating row handled via CellStyler
	colPassPill   = 0xDCFCE7 // light green bg for pass cell
	colFailPill   = 0xFEE2E2 // light red bg for fail cell
	colSkipPill   = 0xF3F4F6 // light gray bg for skip cell
	colCardBg     = 0xFFFFFF // card/group background
	colSummaryBg  = 0xEEF2FF // light indigo summary strip
)

// resultModel is a walk.TableModel that displays checker.FeatureResult rows.
type resultModel struct {
	walk.TableModelBase
	mu      sync.Mutex
	results []checker.FeatureResult
	cols    []string // column display names -> keys
}

func newResultModel() *resultModel {
	return &resultModel{
		cols: []string{"Name", "Status", "Latency", "Upstream Model", "Detail", "Error"},
	}
}

func (m *resultModel) RowCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.results)
}

func (m *resultModel) Value(row, col int) interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row < 0 || row >= len(m.results) {
		return nil
	}
	r := m.results[row]
	switch col {
	case 0:
		return r.Name
	case 1:
		return r.Status.String()
	case 2:
		if r.Latency > 0 {
			return fmt.Sprintf("%dms", r.Latency.Milliseconds())
		}
		return ""
	case 3:
		return r.UpstreamResponseModel
	case 4:
		return r.Detail
	case 5:
		return r.Error
	}
	return nil
}

// add appends a result and returns a snapshot for export.
func (m *resultModel) add(r checker.FeatureResult) {
	m.mu.Lock()
	m.results = append(m.results, r)
	m.mu.Unlock()
	m.PublishRowsReset()
}

// reset clears all rows.
func (m *resultModel) reset() {
	m.mu.Lock()
	m.results = nil
	m.mu.Unlock()
	m.PublishRowsReset()
}

// snapshot returns a copy of the current results (for CSV export).
func (m *resultModel) snapshot() []checker.FeatureResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]checker.FeatureResult, len(m.results))
	copy(out, m.results)
	return out
}

// statusCellColors returns (background, text) colors for a status string.
func statusCellColors(status string) (walk.Color, walk.Color) {
	switch status {
	case "PASS":
		return colStatusPass, 0xFFFFFF
	case "FAIL":
		return colStatusFail, 0xFFFFFF
	case "SKIP":
		return colStatusSkip, 0xFFFFFF
	default:
		return colStatusInit, 0xFFFFFF
	}
}

// historyModel is a walk.TableModel that displays saved history.Record rows.
type historyModel struct {
	walk.TableModelBase
	mu      sync.Mutex
	records []history.Record
}

func newHistoryModel() *historyModel { return &historyModel{} }

func (m *historyModel) RowCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.records)
}

func (m *historyModel) Value(row, col int) interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row < 0 || row >= len(m.records) {
		return nil
	}
	r := m.records[row]
	switch col {
	case 0:
		return r.Time.Format("2006-01-02 15:04:05")
	case 1:
		return r.ProviderLabel
	case 2:
		return r.Model
	case 3:
		return featureNames(r.Features)
	case 4:
		return r.Summary()
	case 5:
		return reasoningLabel(r.ReasoningEffort, r.ReasoningMode)
	}
	return nil
}

// setRecords replaces the model contents and refreshes the view.
func (m *historyModel) setRecords(recs []history.Record) {
	m.mu.Lock()
	m.records = recs
	m.mu.Unlock()
	m.PublishRowsReset()
}

// recordAt returns the record at the given row (or false if out of range).
func (m *historyModel) recordAt(row int) (history.Record, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if row < 0 || row >= len(m.records) {
		return history.Record{}, false
	}
	return m.records[row], true
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

// styleHistoryCell adds alternating-row shading and colors the summary column.
func styleHistoryCell(m *historyModel, style *walk.CellStyle) {
	row := style.Row()
	if row < 0 || row >= m.RowCount() {
		return
	}
	if row%2 == 1 {
		style.BackgroundColor = 0xEFF1F5
	}
	if style.Col() == 4 {
		if r, ok := m.recordAt(row); ok {
			switch {
			case r.Fail > 0:
				style.TextColor = colStatusFail
			case r.Pass > 0:
				style.TextColor = colStatusPass
			default:
				style.TextColor = colTextMuted
			}
			style.Font, _ = walk.NewFont("Segoe UI", 9, walk.FontBold)
		}
	}
}
