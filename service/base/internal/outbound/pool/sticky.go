package pool

import (
	"sync"
	"time"
)

// defaultSessionTTL is the idle lifetime of a session→member binding when the
// pool is not given an explicit TTL.
const defaultSessionTTL = 10 * time.Minute

// stickyState tracks two kinds of member affinity:
//
//   - stable buckets: a filter bucket key → member tag mapping that keeps all
//     traffic in the same bucket pinned to one long-lived member (anti-ban).
//   - sessions: a session key → member binding with an idle TTL, used for the
//     session strategy so a crawler's session keeps the same egress IP.
//
// Expiry is handled lazily: every access runs a sweep at most once per TTL so
// the maps cannot grow unbounded while there is traffic, without needing a
// dedicated cleanup goroutine (the pool can have many instances).
type stickyState struct {
	mu       sync.Mutex
	buckets  map[string]string
	sessions map[string]*sessionBinding
	ttl      time.Duration
	now      func() time.Time
}

type sessionBinding struct {
	tag       string
	lastSeen  time.Time
	expiresAt time.Time
}

func newStickyState(ttl time.Duration) *stickyState {
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	return &stickyState{
		buckets:  make(map[string]string),
		sessions: make(map[string]*sessionBinding),
		ttl:      ttl,
		now:      time.Now,
	}
}

// candidateByTag returns the candidate with the given tag, or nil if it is not
// in the current candidate set (unavailable / filtered out / blacklisted).
func candidateByTag(candidates []*memberState, tag string) *memberState {
	if tag == "" {
		return nil
	}
	for _, c := range candidates {
		if c.tag == tag {
			return c
		}
	}
	return nil
}

// pickStable returns the member pinned to the given bucket. If the pinned
// member is still a valid candidate it is reused; otherwise the bucket is
// promoted to the supplied fallback (best healthy candidate) and remembered.
func (s *stickyState) pickStable(bucketKey string, candidates []*memberState, fallback *memberState) *memberState {
	if fallback == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if tag, ok := s.buckets[bucketKey]; ok {
		if member := candidateByTag(candidates, tag); member != nil {
			return member
		}
	}
	// No valid pinned member: promote the fallback and remember it.
	s.buckets[bucketKey] = fallback.tag
	return fallback
}

// pickSession returns the member bound to the session key. If the bound member
// is still a valid candidate it is reused (and its TTL refreshed); otherwise
// the session is rebound to the fallback. An empty key is treated as "no
// stickiness" and the fallback is returned without being stored.
func (s *stickyState) pickSession(key string, ttl time.Duration, candidates []*memberState, fallback *memberState) *memberState {
	if fallback == nil {
		return nil
	}
	if key == "" {
		return fallback
	}
	if ttl <= 0 {
		ttl = s.ttl
	}
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	now := time.Now()
	if s != nil && s.now != nil {
		now = s.now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepExpiredLocked(now)

	if binding, ok := s.sessions[key]; ok {
		if !binding.expiresAt.IsZero() && !now.Before(binding.expiresAt) {
			delete(s.sessions, key)
		} else if member := candidateByTag(candidates, binding.tag); member != nil {
			binding.lastSeen = now
			binding.expiresAt = now.Add(ttl)
			return member
		}
	}
	s.sessions[key] = &sessionBinding{tag: fallback.tag, lastSeen: now, expiresAt: now.Add(ttl)}
	return fallback
}

// sweepExpiredLocked removes expired session bindings. Must hold s.mu.
func (s *stickyState) sweepExpiredLocked(now time.Time) {
	for key, binding := range s.sessions {
		if binding == nil {
			delete(s.sessions, key)
			continue
		}
		if !binding.expiresAt.IsZero() && !now.Before(binding.expiresAt) {
			delete(s.sessions, key)
		}
	}
}

// pruneTags drops stable/session bindings that point at members no longer
// present in the live tag set. Called when the pool's member set is reloaded
// (e.g. after a subscription refresh) so stale tags are not held forever.
func (s *stickyState) pruneTags(liveTags map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, tag := range s.buckets {
		if _, ok := liveTags[tag]; !ok {
			delete(s.buckets, key)
		}
	}
	for key, binding := range s.sessions {
		if _, ok := liveTags[binding.tag]; !ok {
			delete(s.sessions, key)
		}
	}
}

// StickySnapshot is an observability view of current affinity state.
type StickySnapshot struct {
	Buckets  map[string]string `json:"buckets"`
	Sessions map[string]string `json:"sessions"`
}

// stickySnapshot is the unexported alias retained for internal callers.
type stickySnapshot = StickySnapshot

func (s *stickyState) snapshot() stickySnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	buckets := make(map[string]string, len(s.buckets))
	for k, v := range s.buckets {
		buckets[k] = v
	}
	sessions := make(map[string]string, len(s.sessions))
	for k, v := range s.sessions {
		sessions[k] = v.tag
	}
	return stickySnapshot{Buckets: buckets, Sessions: sessions}
}
