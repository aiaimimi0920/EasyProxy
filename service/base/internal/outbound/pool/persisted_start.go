package pool

// hydrateFromMonitor reconciles durable monitor state before the pool starts
// serving traffic. Constructors attach the entry before boxmgr restores SQLite,
// so the entry can hold newer blacklist and byte totals than the shared state.
// Keep this out of selection's hot path and do not replace probe callbacks or
// treat cumulative failures as the consecutive runtime-failure threshold.
func (s *sharedMemberState) hydrateFromMonitor() {
	if s == nil {
		return
	}
	s.entryMu.Lock()
	defer s.entryMu.Unlock()
	entry := s.entry.Load()
	if entry == nil {
		return
	}
	snapshot := entry.Snapshot()
	s.mu.Lock()
	s.blacklisted = snapshot.Blacklisted
	s.blacklistedUntil = snapshot.BlacklistedUntil
	s.mu.Unlock()
	s.totalUpload.Store(snapshot.TotalUpload)
	s.totalDownload.Store(snapshot.TotalDownload)
}
