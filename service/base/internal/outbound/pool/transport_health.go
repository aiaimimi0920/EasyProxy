package pool

import (
	"time"

	N "github.com/sagernet/sing/common/network"
)

type transportFailureState struct {
	failures         int
	lastFailAt       time.Time
	failedDests      map[string]struct{}
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
	now := time.Now()
	s.udp.failures++
	s.udp.lastFailAt = now
	if dest := normalizeDestinationAddress(destination); dest != "" {
		if s.udp.failedDests == nil {
			s.udp.failedDests = make(map[string]struct{}, 2)
		}
		s.udp.failedDests[dest] = struct{}{}
	}
	count := s.udp.failures
	triggered := count >= threshold
	var until time.Time
	if triggered {
		until = now.Add(duration)
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
	s.udp.lastFailAt = time.Time{}
	s.udp.failedDests = nil
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
	s.lastFailAt = time.Time{}
	s.failedDests = nil
	s.blacklisted = false
	s.blacklistedUntil = time.Time{}
	s.mu.Unlock()
	if entry := s.entry.Load(); entry != nil {
		entry.ClearBlacklist()
	}
}
