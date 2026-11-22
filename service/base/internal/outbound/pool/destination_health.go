package pool

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"easy_proxies/internal/monitor"

	N "github.com/sagernet/sing/common/network"
)

const (
	destinationCorrelatedWindow      = 3 * time.Minute
	destinationUnreachableHoldWindow = 5 * time.Minute
	memberFailureCooldownWindow      = 45 * time.Second
	singleDestVerifyTimeout          = 10 * time.Second
	maxTrackedDestinations           = 2048
)

type destinationFailureRecord struct {
	members        map[string]time.Time
	confirmedUntil time.Time
	updatedAt      time.Time
}

type destinationHealthTracker struct {
	mu      sync.Mutex
	records map[string]*destinationFailureRecord
}

func normalizeDestinationAddress(destination string) string {
	trimmed := strings.ToLower(strings.TrimSpace(destination))
	switch trimmed {
	case "", ":0", "0.0.0.0:0", "[::]:0":
		return ""
	default:
		return trimmed
	}
}

func normalizeDestinationKey(network, destination string) (string, string) {
	dest := normalizeDestinationAddress(destination)
	if dest == "" {
		return "", ""
	}
	netName := N.NetworkTCP
	if network == N.NetworkUDP {
		netName = N.NetworkUDP
	}
	return netName + "|" + dest, dest
}

func (t *destinationHealthTracker) observeFailure(
	now time.Time,
	network, destination, memberTag string,
) (correlated bool, newlyCorrelatedTags []string) {
	key, _ := normalizeDestinationKey(network, destination)
	memberTag = strings.TrimSpace(memberTag)
	if key == "" || memberTag == "" {
		return false, nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.records == nil {
		t.records = make(map[string]*destinationFailureRecord)
	}

	rec := t.records[key]
	if rec == nil {
		t.evictIfNeededLocked(now)
		rec = &destinationFailureRecord{
			members: make(map[string]time.Time, 2),
		}
		t.records[key] = rec
	}

	for tag, failedAt := range rec.members {
		if now.Sub(failedAt) > destinationCorrelatedWindow {
			delete(rec.members, tag)
		}
	}

	wasConfirmed := !rec.confirmedUntil.IsZero() && now.Before(rec.confirmedUntil)
	rec.members[memberTag] = now
	rec.updatedAt = now

	if wasConfirmed {
		rec.confirmedUntil = now.Add(destinationUnreachableHoldWindow)
		return true, nil
	}

	if len(rec.members) >= 2 {
		rec.confirmedUntil = now.Add(destinationUnreachableHoldWindow)
		newlyCorrelatedTags = make([]string, 0, len(rec.members))
		for tag := range rec.members {
			if tag != memberTag {
				newlyCorrelatedTags = append(newlyCorrelatedTags, tag)
			}
		}
		return true, newlyCorrelatedTags
	}

	return false, nil
}

func (t *destinationHealthTracker) markConfirmedUnreachable(now time.Time, network, destination string) {
	key, _ := normalizeDestinationKey(network, destination)
	if key == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.records == nil {
		t.records = make(map[string]*destinationFailureRecord)
	}
	rec := t.records[key]
	if rec == nil {
		t.evictIfNeededLocked(now)
		rec = &destinationFailureRecord{
			members: make(map[string]time.Time, 1),
		}
		t.records[key] = rec
	}
	rec.confirmedUntil = now.Add(destinationUnreachableHoldWindow)
	rec.updatedAt = now
}

func (t *destinationHealthTracker) clear(network, destination string) {
	key, _ := normalizeDestinationKey(network, destination)
	if key == "" {
		return
	}
	t.mu.Lock()
	if t.records != nil {
		delete(t.records, key)
	}
	t.mu.Unlock()
}

func (t *destinationHealthTracker) evictIfNeededLocked(now time.Time) {
	if len(t.records) < maxTrackedDestinations {
		return
	}
	var oldestKey string
	var oldestTime time.Time
	for k, rec := range t.records {
		expired := (rec.confirmedUntil.IsZero() || !now.Before(rec.confirmedUntil)) &&
			now.Sub(rec.updatedAt) > destinationCorrelatedWindow
		if expired {
			delete(t.records, k)
			continue
		}
		if oldestKey == "" || rec.updatedAt.Before(oldestTime) {
			oldestKey = k
			oldestTime = rec.updatedAt
		}
	}
	if len(t.records) >= maxTrackedDestinations && oldestKey != "" {
		delete(t.records, oldestKey)
	}
}

func (s *sharedMemberState) inFailureCooldown(now time.Time) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blacklisted || s.failures <= 0 || s.lastFailAt.IsZero() {
		return false
	}
	if now.Before(s.lastFailAt) {
		return true
	}
	return now.Sub(s.lastFailAt) <= memberFailureCooldownWindow
}

func (s *sharedMemberState) recordObservationOnly(cause error, destination string) {
	if s == nil {
		return
	}
	s.entryMu.Lock()
	defer s.entryMu.Unlock()
	if entry := s.entry.Load(); entry != nil {
		entry.RecordObservationFailure(cause, destination)
	}
}

func (s *sharedMemberState) forgiveSingleDestinationFailure(network, destination string) {
	if s == nil {
		return
	}
	dest := normalizeDestinationAddress(destination)
	if dest == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if network == N.NetworkUDP {
		if !s.udp.blacklisted && len(s.udp.failedDests) == 1 {
			if _, ok := s.udp.failedDests[dest]; ok {
				s.udp.failures = 0
				s.udp.lastFailAt = time.Time{}
				s.udp.failedDests = nil
			}
		}
		return
	}
	if !s.blacklisted && len(s.failedDests) == 1 {
		if _, ok := s.failedDests[dest]; ok {
			s.failures = 0
			s.lastFailAt = time.Time{}
			s.failedDests = nil
		}
	}
}

func (s *sharedMemberState) holdSingleDestinationFailureIfEligible(
	now time.Time,
	network string,
	cause error,
	threshold int,
	destination string,
) (failures int, held bool) {
	dest := normalizeDestinationAddress(destination)
	if s == nil || threshold < 2 || dest == "" {
		return 0, false
	}

	s.entryMu.Lock()
	defer s.entryMu.Unlock()

	s.mu.Lock()
	if network == N.NetworkUDP {
		if s.udp.blacklisted || !isSingleDestinationStreak(s.udp.failedDests, dest) || s.udp.failures+1 < threshold {
			s.mu.Unlock()
			return 0, false
		}
		if s.udp.failedDests == nil {
			s.udp.failedDests = map[string]struct{}{dest: {}}
		}
		s.udp.failures = threshold - 1
		s.udp.lastFailAt = now
		failures = s.udp.failures
		s.mu.Unlock()
	} else {
		if s.blacklisted || !isSingleDestinationStreak(s.failedDests, dest) || s.failures+1 < threshold {
			s.mu.Unlock()
			return 0, false
		}
		if s.failedDests == nil {
			s.failedDests = map[string]struct{}{dest: {}}
		}
		s.failures = threshold - 1
		s.lastFailAt = now
		failures = s.failures
		s.mu.Unlock()
	}

	if entry := s.entry.Load(); entry != nil {
		entry.RecordObservationFailure(cause, destination)
	}
	return failures, true
}

func isSingleDestinationStreak(failedDests map[string]struct{}, dest string) bool {
	if len(failedDests) == 0 {
		return true
	}
	if len(failedDests) > 1 {
		return false
	}
	_, ok := failedDests[dest]
	return ok
}

func (s *sharedMemberState) commitVerifiedNodeBlacklist(
	network string,
	cause error,
	duration time.Duration,
	destination string,
) bool {
	if s == nil {
		return false
	}
	s.entryMu.Lock()
	defer s.entryMu.Unlock()

	until := time.Now().Add(duration)
	s.mu.Lock()
	if network == N.NetworkUDP {
		if s.udp.blacklisted {
			s.mu.Unlock()
			return false
		}
		s.udp = transportFailureState{blacklisted: true, blacklistedUntil: until}
		s.mu.Unlock()
		if entry := s.entry.Load(); entry != nil {
			entry.RecordFailure(cause, destination)
		}
		return true
	}

	if s.blacklisted {
		s.mu.Unlock()
		return false
	}
	s.failures = 0
	s.lastFailAt = time.Time{}
	s.failedDests = nil
	s.blacklisted = true
	s.blacklistedUntil = until
	s.mu.Unlock()

	if entry := s.entry.Load(); entry != nil {
		entry.RecordFailure(cause, destination)
		entry.Blacklist(until)
	}
	return true
}

func (p *poolOutbound) canVerifyMemberProbe(member *memberState) bool {
	return p != nil && p.monitor != nil && member != nil && member.entry != nil && member.entry.HasProbe()
}

func (p *poolOutbound) verifySingleDestMemberHealth(
	registry *sharedStateRegistry,
	member *memberState,
	network string,
	cause error,
	destination string,
) {
	if member == nil || member.shared == nil || !p.canVerifyMemberProbe(member) {
		return
	}
	if !member.shared.probeVerifying.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer member.shared.probeVerifying.Store(false)

		parent := p.ctx
		if parent == nil {
			parent = context.Background()
		}
		probeCtx, cancel := context.WithTimeout(parent, singleDestVerifyTimeout)
		_, probeErr := p.monitor.Probe(probeCtx, member.tag)
		cancel()

		if probeErr == nil {
			if registry != nil {
				registry.destHealth.markConfirmedUnreachable(time.Now(), network, destination)
			}
			member.shared.forgiveSingleDestinationFailure(network, destination)
			p.logger.Info(
				"proxy ",
				member.tag,
				" passed health probe after repeated failures to ",
				destination,
				"; keeping node active",
			)
			return
		}

		if errors.Is(probeErr, monitor.ErrStaleGeneration) ||
			(p.ctx != nil && p.ctx.Err() != nil) ||
			strings.Contains(probeErr.Error(), "probe target not configured") ||
			strings.Contains(probeErr.Error(), "probe not available") {
			return
		}

		if member.shared.commitVerifiedNodeBlacklist(network, cause, p.options.BlacklistDuration, destination) {
			p.logger.Warn(
				"proxy ",
				member.tag,
				" blacklisted for ",
				p.options.BlacklistDuration,
				" after probe confirmed node failure: ",
				probeErr,
			)
		}
	}()
}
