package main

import (
	"sync"

	"provider-checker/history"
)

// storeInit serializes history.Store initialization so exactly one successful
// store is published per process no matter how many callers race. This closes
// the GUI's double-Open race (issue #11): the background load used to publish
// its own snapshot unconditionally, so a first save that had already opened
// and written through a second Store could be overwritten by the late publish
// and lose the saved record on the next full-table write.
//
// A successful publish is final: later attempts return the same store instead
// of replacing it with an independent snapshot. Failed attempts are retryable
// by the next ensure call. The store is published (and waiters released)
// before ensure returns, so callers complete any UI round-trips only after
// the store is visible — a UI-thread save waiting for the background load can
// never deadlock against a Synchronize the background goroutine still owes.
type storeInit struct {
	mu          sync.Mutex
	store       *history.Store
	opening     bool
	attemptDone chan struct{} // closed when the in-flight attempt finished
}

func newStoreInit() *storeInit { return &storeInit{} }

// openHistoryStore resolves the default path and opens the store.
func openHistoryStore() (*history.Store, error) {
	path, err := history.DefaultPath()
	if err != nil {
		return nil, err
	}
	return history.Open(path)
}

// get returns the published store, or nil when initialization has not
// succeeded yet.
func (s *storeInit) get() *history.Store {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store
}

// ensure waits for any in-flight attempt and returns the published store,
// performing at most one open attempt itself when none has succeeded and none
// is running. It returns nil when the attempt it waited for (or made) failed;
// the next call will retry.
func (s *storeInit) ensure(open func() (*history.Store, error)) *history.Store {
	s.mu.Lock()
	if s.store != nil {
		store := s.store
		s.mu.Unlock()
		return store
	}
	if s.opening {
		done := s.attemptDone
		s.mu.Unlock()
		<-done
		return s.get()
	}
	s.opening = true
	done := make(chan struct{})
	s.attemptDone = done
	s.mu.Unlock()

	store, err := open()

	s.mu.Lock()
	s.opening = false
	// The successful attempt owns the publish; a late attempt can never
	// replace an already published store.
	if err == nil && store != nil && s.store == nil {
		s.store = store
	}
	close(done)
	s.mu.Unlock()
	if err != nil {
		return nil
	}
	return s.get()
}
