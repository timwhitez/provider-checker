package main

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"provider-checker/checker"
	"provider-checker/history"
)

func TestWriteResultsCSVIncludesUpstreamResponseModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.csv")
	results := []checker.FeatureResult{{
		Name:                  "Basic",
		Status:                checker.StatusPass,
		UpstreamResponseModel: "gpt-5.2-2026-01-15",
		Detail:                "hello",
	}}
	if err := writeResultsCSV(path, results); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("row count = %d, want 2", len(rows))
	}
	if got := rows[0][3]; got != "UpstreamResponseModel" {
		t.Fatalf("header = %q, want UpstreamResponseModel", got)
	}
	if got := rows[1][3]; got != "gpt-5.2-2026-01-15" {
		t.Fatalf("upstream response model = %q", got)
	}
}

func TestEffectiveReasoningPerProvider(t *testing.T) {
	cases := []struct {
		provider, effort, mode string
	}{
		{"openai-chat", "high", ""},
		{"openai-response", "high", "pro"},
		{"anthropic", "", ""},
		{"gemini", "", ""},
	}
	for _, c := range cases {
		effort, mode := effectiveReasoning(c.provider, "high", "pro")
		if effort != c.effort || mode != c.mode {
			t.Errorf("%s: got (%q, %q), want (%q, %q)", c.provider, effort, mode, c.effort, c.mode)
		}
		effortOK, modeOK := reasoningControls(c.provider)
		if effortOK != (c.effort != "") || modeOK != (c.mode != "") {
			t.Errorf("%s: controls (%v, %v) disagree with effective values", c.provider, effortOK, modeOK)
		}
	}
}

func TestOutcomeOf(t *testing.T) {
	cases := []struct {
		pass, fail int
		want       runOutcome
	}{
		{3, 0, outcomePass},
		{0, 0, outcomePass}, // all SKIP (e.g. cancelled before the first probe)
		{1, 2, outcomeMixed},
		{0, 2, outcomeFail},
	}
	for _, c := range cases {
		if got := outcomeOf(c.pass, c.fail); got != c.want {
			t.Errorf("outcomeOf(%d, %d) = %d, want %d", c.pass, c.fail, got, c.want)
		}
	}
}

func TestFeatureDoneLogLine(t *testing.T) {
	r := checker.FeatureResult{
		Name: "Basic", Status: checker.StatusFail, Latency: 1500 * time.Millisecond,
		Detail: "hi", UpstreamResponseModel: "m-2026", Error: strings.Repeat("e", 200),
	}
	line := featureDoneLogLine(0, 2, r)
	for _, want := range []string{"<- [1/2] Basic: FAIL (1500ms)", "  hi", "upstream_model=m-2026", "err=" + strings.Repeat("e", 120) + "..."} {
		if !strings.Contains(line, want) {
			t.Errorf("log line %q missing %q", line, want)
		}
	}
}

func TestSelectedFeatureKeysKeepsDisplayOrder(t *testing.T) {
	got := selectedFeatureKeys(map[string]bool{"system": true, "basic": true, "json": false, "tools": true})
	want := []string{"basic", "tools", "system"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCellText(t *testing.T) {
	r := checker.FeatureResult{Name: "Basic", Status: checker.StatusPass, Latency: 42 * time.Millisecond}
	if text, _ := resultCellText(r, 2); text != "42ms" {
		t.Fatalf("latency cell = %q", text)
	}
	if _, ok := resultCellText(r, 6); ok {
		t.Fatal("out-of-range result column accepted")
	}
	rec := history.Record{Features: []string{"basic", "json"}, Pass: 1, Fail: 1, ReasoningEffort: "low", ReasoningMode: "pro"}
	if text, _ := historyCellText(rec, 3); text != "Basic, JSON Mode" {
		t.Fatalf("features cell = %q", text)
	}
	if text, _ := historyCellText(rec, 4); text != "1/1/0" {
		t.Fatalf("summary cell = %q", text)
	}
	if text, _ := historyCellText(rec, 5); text != "low / pro" {
		t.Fatalf("reasoning cell = %q", text)
	}
}
