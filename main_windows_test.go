//go:build windows

package main

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"

	"provider-checker/checker"
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
