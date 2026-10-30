//go:build windows

package main

import (
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
	if text, ok := resultCellText(m.results[row], col); ok {
		return text
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
	if text, ok := historyCellText(m.records[row], col); ok {
		return text
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
