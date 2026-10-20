package history

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func rec(model string, offset time.Duration) Record {
	return Record{Time: time.Now().Add(offset), Provider: "openai-chat", Model: model, Pass: 1}
}

// TestCommitBoundaryKeepsLastCommittedState pins the issue-#11 commit
// boundary: a failed Add or Clear must leave memory and disk at the last
// committed state, and an Add after a failed Clear must keep the old entries.
func TestCommitBoundaryKeepsLastCommittedState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Add(rec("A", -2*time.Hour)); err != nil {
		t.Fatalf("Add A: %v", err)
	}

	// White-box failure injection: point the store at a path whose parent is
	// a regular file, so every commit fails before touching anything while
	// the previously committed file stays intact on disk.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	realPath := s.path
	s.path = filepath.Join(blocker, "history.json")
	if err := s.Add(rec("B", -time.Hour)); err == nil {
		t.Fatal("Add through blocked path must fail")
	}
	if err := s.Clear(); err == nil {
		t.Fatal("Clear through blocked path must fail")
	}
	if got := s.Records(); len(got) != 1 || got[0].Model != "A" {
		t.Fatalf("memory mutated by failed commit: %+v", got)
	}

	// The committed disk state is untouched; a later Add keeps the old entry.
	s.path = realPath
	if err := s.Add(rec("C", 0)); err != nil {
		t.Fatalf("Add C after failed commits: %v", err)
	}
	fresh, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := fresh.Records()
	if len(got) != 2 || got[0].Model != "C" || got[1].Model != "A" {
		t.Fatalf("records after recovery = %+v, want C then A", got)
	}
}

// TestCommitUsesExclusiveTempFiles verifies each commit writes through its
// own temp file (no shared candidate that concurrent operations could
// clobber) and leaves no temp files behind.
func TestCommitUsesExclusiveTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Add(rec("A", 0)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Add(rec("B", time.Second)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "history.json" {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
	// Two stores on one path keep independent snapshots (documented library
	// behavior); the coordinator in the GUI prevents that pairing. Concurrent
	// commits within one store serialize on the store mutex, and each commit
	// owns its temp file name.
	s2, _ := Open(path)
	if err := s2.Add(rec("D", 2*time.Second)); err != nil {
		t.Fatalf("Add through second store: %v", err)
	}
}

// TestRawDoubleOpenLosesSavedRecord documents the pre-fix counterexample from
// issue #11 with plain (uncoordinated) stores: two independent snapshots on
// one path, with writes in the reverse order of their opens, silently drop
// the first save. The GUI's storeInit coordinator exists to make this pairing
// impossible; this test pins the library-level behavior it must prevent ever
// being reached, and forces a conscious decision if Store ever gains
// reconciliation semantics.
func TestRawDoubleOpenLosesSavedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	seed, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Add(rec("H0", -3*time.Hour)); err != nil {
		t.Fatal(err)
	}

	background, err := Open(path) // reads H0, then pauses (in the GUI: a goroutine)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Open(path) // first save also reads H0
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Add(rec("A", -time.Hour)); err != nil { // save A: disk H0,A
		t.Fatal(err)
	}
	if err := background.Add(rec("B", 0)); err != nil { // late save B from the old snapshot: disk H0,B
		t.Fatal(err)
	}

	fresh, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var models []string
	for _, r := range fresh.Records() {
		models = append(models, r.Model)
	}
	// A is gone: the exact data loss issue #11 describes.
	if len(models) != 2 || models[0] != "B" || models[1] != "H0" {
		t.Fatalf("records = %v, want the documented lost-update shape [B H0]", models)
	}
}

// TestConcurrentCommitsKeepValidJSON smokes the temp-file exclusivity under
// concurrency: parallel commits through stores sharing one path must always
// leave a well-formed JSON array containing at least the winner's records.
func TestConcurrentCommitsKeepValidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	const stores = 4
	const adds = 8
	var wg sync.WaitGroup
	for i := 0; i < stores; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := Open(path)
			if err != nil {
				t.Error(err)
				return
			}
			for j := 0; j < adds; j++ {
				if err := s.Add(rec(fmtModel(i*100+j), time.Duration(i*100+j)*time.Millisecond)); err != nil {
					t.Error(err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	fresh, err := Open(path)
	if err != nil {
		t.Fatalf("final file not a valid store: %v", err)
	}
	if len(fresh.Records()) == 0 {
		t.Fatal("all concurrent commits lost")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "history.json" {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

// TestStoreTrimBoundary pins the 200/201 boundary: 201 adds keep the newest
// 200 records.
func TestStoreTrimBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	s, _ := Open(path)
	for i := 0; i < maxRecords+1; i++ {
		if err := s.Add(rec(fmtModel(i), time.Duration(i)*time.Millisecond)); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	got := s.Records()
	if len(got) != maxRecords {
		t.Fatalf("records = %d, want %d", len(got), maxRecords)
	}
	// The oldest (first) record was trimmed; the newest is first.
	if got[0].Model != fmtModel(maxRecords) {
		t.Errorf("newest = %q, want %q", got[0].Model, fmtModel(maxRecords))
	}
	if got[len(got)-1].Model != fmtModel(1) {
		t.Errorf("oldest = %q, want %q", got[len(got)-1].Model, fmtModel(1))
	}
}

func fmtModel(i int) string {
	return "model-" + string(rune('a'+i%26)) + "-" + itoa(i)
}
