// Package history persists successful (and completed) provider-check runs so
// users can review past tests and reload their configuration with one click.
//
// Records are stored as a JSON array in the user's config directory. The store
// is cross-platform and has no GUI dependency so it can be unit-tested.
package history

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Record captures the configuration and outcome of one completed run.
// The API key is stored encrypted (opaque, platform-specific ciphertext in
// APIKeyEnc); the history package never sees the plaintext and treats the
// field as an opaque blob so it stays cross-platform and unit-testable.
type Record struct {
	Time            time.Time `json:"time"`
	Provider        string    `json:"provider"`       // provider type key, e.g. "openai-chat"
	ProviderLabel   string    `json:"provider_label"` // friendly label for display
	BaseURL         string    `json:"base_url"`
	Model           string    `json:"model"`
	APIKeyEnc       string    `json:"api_key_enc"` // encrypted API key (opaque, base64); empty if none
	Prompt          string    `json:"prompt"`
	TimeoutSec      int       `json:"timeout_sec"`
	ReasoningEffort string    `json:"reasoning_effort"`
	ReasoningMode   string    `json:"reasoning_mode"`
	Features        []string  `json:"features"` // unified feature keys tested
	Pass            int       `json:"pass"`
	Fail            int       `json:"fail"`
	Skip            int       `json:"skip"`
}

// Summary returns a compact "P/F/S" style string for display.
func (r Record) Summary() string {
	return itoa(r.Pass) + "/" + itoa(r.Fail) + "/" + itoa(r.Skip)
}

// maxRecords bounds the on-disk history to keep it small and fast.
const maxRecords = 200

// Store is a thread-safe, file-backed collection of Records.
type Store struct {
	mu      sync.Mutex
	path    string
	records []Record
}

// DefaultPath returns the standard history file location for this OS.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		// Fall back to the executable's directory / current dir.
		if home, herr := os.UserHomeDir(); herr == nil && home != "" {
			dir = home
		} else {
			dir = "."
		}
	}
	return filepath.Join(dir, "provider-checker", "history.json"), nil
}

// Open loads (or initializes) the store at path. A missing file is not an error.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.records = nil
			return nil
		}
		return err
	}
	if len(b) == 0 {
		s.records = nil
		return nil
	}
	var recs []Record
	if err := json.Unmarshal(b, &recs); err != nil {
		// Corrupt file: start fresh rather than blocking the app.
		s.records = nil
		return nil
	}
	s.records = recs
	return nil
}

// Records returns a newest-first copy of all stored records.
func (s *Store) Records() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, len(s.records))
	copy(out, s.records)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Time.After(out[j].Time)
	})
	return out
}

// Add prepends a record, trims to maxRecords, and persists to disk. The
// in-memory list is updated only after the on-disk replace succeeds, so a
// failed write leaves both memory and disk at the last committed state.
func (s *Store) Add(r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	// Newest first.
	candidate := append([]Record{r}, s.records...)
	if len(candidate) > maxRecords {
		candidate = candidate[:maxRecords]
	}
	if err := s.commitLocked(candidate); err != nil {
		return err
	}
	s.records = candidate
	return nil
}

// Clear removes all records and persists the empty store, again committing
// memory only after the on-disk replace succeeded.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.commitLocked(nil); err != nil {
		return err
	}
	s.records = nil
	return nil
}

// commitLocked persists candidate records through an exclusive temp file and
// an atomic rename. Each operation owns its temp file, so concurrent
// operations cannot clobber each other's candidate; on failure the temp file
// is removed and the last committed state remains on disk and in memory.
// Caller must hold s.mu.
func (s *Store) commitLocked(candidate []Record) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp-")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(b); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Replace the store file atomically; os.Rename over an existing file is
	// supported on both Unix and Windows (MoveFileEx REPLACE_EXISTING).
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	committed = true
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
