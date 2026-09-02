package pool

import (
	"time"

	N "github.com/sagernet/sing/common/network"
)

type transportFailureState struct {
	failures         int
	blacklisted      bool
	blacklistedUntil time.Time
}

func (s *sharedMemberState) recordTransportFailure(
	network string,
	cause error,
	threshold int,
	duration time.Duration,
	destination string,
) (int, bool, time.Time) {
	if network != N.NetworkUDP {
		return s.recordFailure(cause, threshold, duration, destination)
	}
	s.entryMu.Lock()
	defer s.entryMu.Unlock()
	s.mu.Lock()
	s.udp.failures++
	count := s.udp.failures
	triggered := count >= threshold
	var until time.Time
	if triggered {
		until = time.Now().Add(duration)
		s.udp = transportFailureState{blacklisted: true, blacklistedUntil: until}
	}
	s.mu.Unlock()
	if entry := s.entry.Load(); entry != nil {
		entry.RecordFailure(cause, destination)
	}
	return count, triggered, until
}

func (s *sharedMemberState) recordTransportSuccess(network, destination string) {
	if network != N.NetworkUDP {
		s.recordSuccess(destination)
		return
	}
	s.entryMu.Lock()
	defer s.entryMu.Unlock()
	s.mu.Lock()
	s.udp.failures = 0
	s.mu.Unlock()
	if entry := s.entry.Load(); entry != nil {
		entry.RecordSuccess(destination)
	}
}

func (s *sharedMemberState) isTransportBlacklisted(network string, now time.Time) bool {
	if network != N.NetworkUDP {
		return s.isBlacklisted(now)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.udp.blacklisted && now.After(s.udp.blacklistedUntil) {
		s.udp = transportFailureState{}
	}
	return s.udp.blacklisted
}

func (s *sharedMemberState) forceReleaseTransport(network string) {
	if network == N.NetworkUDP {
		s.mu.Lock()
		s.udp = transportFailureState{}
		s.mu.Unlock()
		return
	}
	s.entryMu.Lock()
	defer s.entryMu.Unlock()
	s.mu.Lock()
	s.failures = 0
	s.blacklisted = false
	s.blacklistedUntil = time.Time{}
	s.mu.Unlock()
	if entry := s.entry.Load(); entry != nil {
		entry.ClearBlacklist()
	}
}
