package main

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"provider-checker/history"
)

// TestStoreInitSharesOneOpen reproduces the issue-#11 interleaving with a
// controlled barrier: the background open reads the old H0 snapshot and
// pauses; the first save arrives, waits for the shared initialization, and
// writes A; the background then "publishes" (its ensure returns the same
// store); a second save writes B. Both records and H0 survive: the late
// publish cannot replace the store the first save already wrote through.
func TestStoreInitSharesOneOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	seed, err := history.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Add(history.Record{Time: time.Now().Add(-time.Hour), Provider: "openai-chat", Model: "H0", Pass: 1}); err != nil {
		t.Fatal(err)
	}

	si := newStoreInit()
	started := make(chan struct{})
	release := make(chan struct{})
	var opens atomic.Int32
	open := func() (*history.Store, error) {
		if opens.Add(1) == 1 {
			close(started)
			<-release // the background read of H0 pauses here
		}
		return history.Open(path)
	}

	background := make(chan *history.Store, 1)
	go func() { background <- si.ensure(open) }()
	<-started // the first attempt is in flight, holding the old H0 snapshot

	saveA := make(chan *history.Store, 1)
	go func() { saveA <- si.ensure(open) }() // first save waits, no second Open
	close(release)

	bgStore := <-background
	aStore := <-saveA
	if bgStore == nil || aStore == nil {
		t.Fatal("initialization failed")
	}
	if bgStore != aStore {
		t.Fatal("two independent stores published; the double-Open race is back")
	}
	if opens.Load() != 1 {
		t.Fatalf("opens = %d, want exactly one shared initialization", opens.Load())
	}
	if err := aStore.Add(history.Record{Time: time.Now().Add(-time.Minute), Provider: "openai-chat", Model: "A", Pass: 1}); err != nil {
		t.Fatal(err)
	}

	// The late background "publish" is the same store; the second save B
	// must not lose A.
	late := si.ensure(open)
	if late != bgStore {
		t.Fatal("late ensure replaced the published store")
	}
	if err := late.Add(history.Record{Time: time.Now(), Provider: "openai-chat", Model: "B", Pass: 1}); err != nil {
		t.Fatal(err)
	}

	fresh, err := history.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var models []string
	for _, r := range fresh.Records() {
		models = append(models, r.Model)
	}
	if len(models) != 3 || models[0] != "B" || models[1] != "A" || models[2] != "H0" {
		t.Fatalf("records = %v, want [B A H0]: a successful save was lost", models)
	}
	if opens.Load() != 1 {
		t.Fatalf("opens = %d after all saves, want one", opens.Load())
	}
}

// TestStoreInitRetriesAfterFailure keeps failed initialization retryable:
// the first attempt errors, the next ensure opens again and succeeds.
func TestStoreInitRetriesAfterFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	var calls atomic.Int32
	open := func() (*history.Store, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("disk not ready")
		}
		return history.Open(path)
	}
	si := newStoreInit()
	if s := si.ensure(open); s != nil {
		t.Fatal("first failed attempt must not publish a store")
	}
	s := si.ensure(open)
	if s == nil {
		t.Fatal("retry did not succeed")
	}
	if calls.Load() != 2 {
		t.Fatalf("open calls = %d, want 2", calls.Load())
	}
	if again := si.ensure(open); again != s {
		t.Fatal("published store replaced by a later ensure")
	}
	if calls.Load() != 2 {
		t.Fatalf("open calls = %d after success, want 2 (no re-open)", calls.Load())
	}
}

// TestStoreInitConcurrentWaiters hammer-checks the coordinator: many waiters
// racing one in-flight attempt all observe the single published store.
func TestStoreInitConcurrentWaiters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	started := make(chan struct{})
	release := make(chan struct{})
	var opens atomic.Int32
	open := func() (*history.Store, error) {
		if opens.Add(1) == 1 {
			close(started)
			<-release
		}
		return history.Open(path)
	}
	si := newStoreInit()
	first := make(chan *history.Store, 1)
	go func() { first <- si.ensure(open) }()
	<-started

	const waiters = 16
	results := make(chan *history.Store, waiters)
	var wg sync.WaitGroup
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- si.ensure(open)
		}()
	}
	close(release)
	shared := <-first
	if shared == nil {
		t.Fatal("initialization failed")
	}
	wg.Wait()
	close(results)
	for s := range results {
		if s != shared {
			t.Fatal("waiter observed a different store")
		}
	}
	if opens.Load() != 1 {
		t.Fatalf("opens = %d, want 1", opens.Load())
	}
}

// TestOpenHistoryStoreUsesDefaultPath is a smoke check that the real open
// function resolves a path under the user config dir and opens successfully.
func TestOpenHistoryStoreUsesDefaultPath(t *testing.T) {
	s, err := openHistoryStore()
	if err != nil {
		t.Fatalf("openHistoryStore: %v", err)
	}
	if s == nil {
		t.Fatal("openHistoryStore returned nil store")
	}
	want, err := history.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(want) != "history.json" {
		t.Fatalf("default path base = %q", filepath.Base(want))
	}
}
