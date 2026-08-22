package history

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreAddAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(s.Records()) != 0 {
		t.Fatalf("new store should be empty")
	}

	older := Record{Time: time.Now().Add(-time.Hour), Provider: "anthropic", Model: "claude", Pass: 1}
	newer := Record{Time: time.Now(), Provider: "openai-chat", Model: "gpt", Pass: 3, Fail: 1}
	if err := s.Add(older); err != nil {
		t.Fatalf("Add older: %v", err)
	}
	if err := s.Add(newer); err != nil {
		t.Fatalf("Add newer: %v", err)
	}

	// Reopen from disk to verify persistence.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	recs := s2.Records()
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}
	if recs[0].Model != "gpt" {
		t.Errorf("newest-first order broken: got %q", recs[0].Model)
	}
	if recs[0].Summary() != "3/1/0" {
		t.Errorf("Summary() = %q, want 3/1/0", recs[0].Summary())
	}
}

func TestStoreClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	s, _ := Open(path)
	_ = s.Add(Record{Model: "x", Pass: 1})
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if len(s.Records()) != 0 {
		t.Fatalf("store not cleared")
	}
	s2, _ := Open(path)
	if len(s2.Records()) != 0 {
		t.Fatalf("cleared state not persisted")
	}
}

func TestStoreTrim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	s, _ := Open(path)
	for i := 0; i < maxRecords+50; i++ {
		if err := s.Add(Record{Model: "m", Time: time.Now().Add(time.Duration(i) * time.Millisecond)}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	if got := len(s.Records()); got != maxRecords {
		t.Fatalf("want trimmed to %d, got %d", maxRecords, got)
	}
}

func TestMissingFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open missing: %v", err)
	}
	if len(s.Records()) != 0 {
		t.Fatalf("missing file should yield empty store")
	}
}
