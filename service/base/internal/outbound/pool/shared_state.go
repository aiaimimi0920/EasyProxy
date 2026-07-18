package pool

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"easy_proxies/internal/monitor"
)

// sharedMemberState holds failure/blacklist state shared across all pool instances.
// This enables hybrid mode where pool and multi-port modes share the same node state.
type sharedMemberState struct {
	mu               sync.Mutex
	failures         int
	blacklisted      bool
	blacklistedUntil time.Time
	entry            atomic.Pointer[monitor.EntryHandle]
	active           atomic.Int32
	totalUpload      atomic.Int64
	totalDownload    atomic.Int64
}

type sharedStateRegistry struct {
	states sync.Map // map[tag]*sharedMemberState
}

var sharedStateStore atomic.Pointer[sharedStateRegistry]

func init() {
	sharedStateStore.Store(&sharedStateRegistry{})
}

// SharedStateStoreSnapshot identifies one immutable registry generation. The
// member states inside the registry remain live so detached old connections can
// finish updating their own counters while a reload candidate is evaluated.
type SharedStateStoreSnapshot struct {
	registry *sharedStateRegistry
}

// SharedStateTransaction isolates a reload candidate from the last applied
// registry. Commit keeps the candidate registry; Rollback restores the exact
// old registry object and all of its failure/traffic/active state.
type SharedStateTransaction struct {
	mu        sync.Mutex
	old       *sharedStateRegistry
	candidate *sharedStateRegistry
	done      bool
}

func (r *sharedStateRegistry) detachEntries() {
	if r == nil {
		return
	}
	r.states.Range(func(_, value any) bool {
		value.(*sharedMemberState).entry.Store(nil)
		return true
	})
}

func currentSharedStateRegistry() *sharedStateRegistry {
	registry := sharedStateStore.Load()
	if registry != nil {
		return registry
	}
	registry = &sharedStateRegistry{}
	if sharedStateStore.CompareAndSwap(nil, registry) {
		return registry
	}
	return sharedStateStore.Load()
}

// acquireSharedState returns the shared state for a tag, creating if needed.
func acquireSharedState(tag string) *sharedMemberState {
	registry := currentSharedStateRegistry()
	if v, ok := registry.states.Load(tag); ok {
		return v.(*sharedMemberState)
	}
	state := &sharedMemberState{}
	actual, _ := registry.states.LoadOrStore(tag, state)
	return actual.(*sharedMemberState)
}

// lookupSharedState returns the shared state if it exists.
func lookupSharedState(tag string) (*sharedMemberState, bool) {
	v, ok := currentSharedStateRegistry().states.Load(tag)
	if !ok {
		return nil, false
	}
	return v.(*sharedMemberState), true
}

// ResetSharedStateStore clears all shared state (used during config reload).
func ResetSharedStateStore() {
	old := sharedStateStore.Swap(&sharedStateRegistry{})
	old.detachEntries()
}

// SnapshotSharedStateStore returns the identity of the active registry.
func SnapshotSharedStateStore() SharedStateStoreSnapshot {
	return SharedStateStoreSnapshot{registry: currentSharedStateRegistry()}
}

// BeginSharedStateTransaction atomically installs a fresh candidate registry
// while retaining the previous registry for an exact rollback.
func BeginSharedStateTransaction() *SharedStateTransaction {
	candidate := &sharedStateRegistry{}
	old := sharedStateStore.Swap(candidate)
	if old == nil {
		old = &sharedStateRegistry{}
	}
	// The old box is already being retired when a transaction starts. Its
	// connections may still finish asynchronously, but their callbacks must not
	// write through a monitor entry that the candidate will reuse.
	old.detachEntries()
	return &SharedStateTransaction{old: old, candidate: candidate}
}

// ResetCandidate discards state created by a rejected candidate retry without
// changing the registry retained for rollback.
func (t *SharedStateTransaction) ResetCandidate() error {
	if t == nil {
		return errors.New("shared state transaction is nil")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return errors.New("shared state transaction already completed")
	}
	next := &sharedStateRegistry{}
	if !sharedStateStore.CompareAndSwap(t.candidate, next) {
		return errors.New("shared state candidate registry is no longer active")
	}
	t.candidate.detachEntries()
	t.candidate = next
	return nil
}

// Commit retains the active candidate registry.
func (t *SharedStateTransaction) Commit() error {
	if t == nil {
		return errors.New("shared state transaction is nil")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return errors.New("shared state transaction already completed")
	}
	if sharedStateStore.Load() != t.candidate {
		return errors.New("shared state candidate registry is no longer active")
	}
	t.old.detachEntries()
	t.done = true
	return nil
}

// Rollback restores the exact registry that was active before the transaction.
func (t *SharedStateTransaction) Rollback() error {
	if t == nil {
		return errors.New("shared state transaction is nil")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return errors.New("shared state transaction already completed")
	}
	if !sharedStateStore.CompareAndSwap(t.candidate, t.old) {
		return errors.New("shared state candidate registry is no longer active")
	}
	t.candidate.detachEntries()
	t.done = true
	return nil
}

func (s *sharedMemberState) attachEntry(entry *monitor.EntryHandle) {
	if entry == nil {
		return
	}
	s.entry.Store(entry)
}

func (s *sharedMemberState) entryHandle() *monitor.EntryHandle {
	return s.entry.Load()
}

// recordFailure increments failure count and triggers blacklist if threshold reached.
// Returns: (current failures, blacklisted, blacklist until time)
func (s *sharedMemberState) recordFailure(cause error, threshold int, duration time.Duration, destination string) (int, bool, time.Time) {
	s.mu.Lock()
	s.failures++
	count := s.failures
	triggered := false
	var until time.Time
	if s.failures >= threshold {
		triggered = true
		until = time.Now().Add(duration)
		s.failures = 0
		s.blacklisted = true
		s.blacklistedUntil = until
	}
	s.mu.Unlock()

	if entry := s.entry.Load(); entry != nil {
		entry.RecordFailure(cause, destination)
		if triggered {
			entry.Blacklist(until)
		}
	}
	return count, triggered, until
}

func (s *sharedMemberState) recordSuccess(destination string) {
	s.mu.Lock()
	s.failures = 0
	s.mu.Unlock()

	if entry := s.entry.Load(); entry != nil {
		entry.RecordSuccess(destination)
	}
}

// isBlacklisted checks if the node is currently blacklisted, auto-clearing if expired.
func (s *sharedMemberState) isBlacklisted(now time.Time) bool {
	s.mu.Lock()
	expired := s.blacklisted && now.After(s.blacklistedUntil)
	if expired {
		s.blacklisted = false
		s.blacklistedUntil = time.Time{}
	}
	blacklisted := s.blacklisted
	s.mu.Unlock()

	if expired {
		if entry := s.entry.Load(); entry != nil {
			entry.ClearBlacklist()
		}
	}
	return blacklisted
}

func (s *sharedMemberState) forceRelease() {
	s.mu.Lock()
	s.failures = 0
	s.blacklisted = false
	s.blacklistedUntil = time.Time{}
	s.mu.Unlock()

	if entry := s.entry.Load(); entry != nil {
		entry.ClearBlacklist()
	}
}

func (s *sharedMemberState) incActive() {
	s.active.Add(1)
	if entry := s.entry.Load(); entry != nil {
		entry.IncActive()
	}
}

func (s *sharedMemberState) decActive() {
	s.active.Add(-1)
	if entry := s.entry.Load(); entry != nil {
		entry.DecActive()
	}
}

func (s *sharedMemberState) activeCount() int32 {
	return s.active.Load()
}

func (s *sharedMemberState) addTraffic(upload, download int64) {
	if upload > 0 {
		s.totalUpload.Add(upload)
	}
	if download > 0 {
		s.totalDownload.Add(download)
	}
	if entry := s.entry.Load(); entry != nil {
		entry.AddTraffic(upload, download)
	}
}

func releaseSharedState(state *sharedMemberState) func() {
	return func() {
		if state != nil {
			state.forceRelease()
		}
	}
}
