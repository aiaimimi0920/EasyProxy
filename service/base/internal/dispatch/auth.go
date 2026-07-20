package dispatch

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"easy_proxies/internal/outbound/pool"
	"easy_proxies/internal/profile"
	"easy_proxies/internal/routerule"
)

var (
	errProxyAuthRequired    = errors.New("proxy authentication required")
	errProxyAuthInvalid     = errors.New("proxy authentication failed")
	errProxyUsernameInvalid = errors.New("proxy username invalid")
)

// ProfileResolver provides the current profile snapshot used by local-server
// request routing.
type ProfileResolver interface {
	Credentials() profile.CredentialSnapshot
	Resolve(profile.RequestIdentity) profile.Resolution
	Observe(profile.Resolution, netip.Addr, time.Time)
}

type parsedProxyUsername struct {
	BaseUsername     string
	ExplicitDeviceID string
	Overlay          directiveOverlay
}

func splitBaseUsername(raw string) (base string, rawTokens []string) {
	parts := strings.Split(strings.TrimSpace(raw), "+")
	if len(parts) == 0 {
		return "", nil
	}
	base = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		rawTokens = parts[1:]
	}
	return base, rawTokens
}

func splitProxyUsername(raw string) (parsedProxyUsername, error) {
	base, rawTokens := splitBaseUsername(raw)
	parsed := parsedProxyUsername{BaseUsername: base}
	if base == "" && len(rawTokens) == 0 {
		return parsed, nil
	}

	var remaining []string
	for _, token := range rawTokens {
		trimmed := strings.TrimSpace(token)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(lower, "dev="):
			if parsed.ExplicitDeviceID != "" {
				return parsedProxyUsername{}, errProxyUsernameInvalid
			}
			deviceID := strings.TrimSpace(trimmed[4:])
			normalized, err := profile.NormalizeDeviceID(deviceID)
			if err != nil {
				return parsedProxyUsername{}, errProxyUsernameInvalid
			}
			parsed.ExplicitDeviceID = normalized
		default:
			remaining = append(remaining, trimmed)
		}
	}
	if overlay, ok := parseTokens(strings.Join(remaining, "+")); ok {
		parsed.Overlay = overlay
	}
	return parsed, nil
}

func (s *Server) authCredentials() (username, password string, required bool) {
	if s == nil {
		return "", "", false
	}
	if s.cfg.Profiles != nil {
		creds := s.cfg.Profiles.Credentials()
		return creds.Username, creds.Password, true
	}
	if s.cfg.Username != "" || s.cfg.Password != "" {
		return s.cfg.Username, s.cfg.Password, true
	}
	return "", "", false
}

func (s *Server) authenticateProxy(username, password string) (parsedProxyUsername, error) {
	expectedUser, expectedPass, required := s.authCredentials()
	if !required {
		return legacyProxyUsername(username), nil
	}

	suppliedUser, _ := splitBaseUsername(username)
	userOK := constantTimeEqual(suppliedUser, expectedUser)
	passOK := constantTimeEqual(password, expectedPass)
	if !userOK || !passOK {
		return parsedProxyUsername{}, errProxyAuthInvalid
	}
	return splitProxyUsername(username)
}

func legacyProxyUsername(raw string) parsedProxyUsername {
	overlay, _ := parseTokens(raw)
	return parsedProxyUsername{Overlay: overlay}
}

func (s *Server) authenticateHTTPRequest(req *http.Request) (parsedProxyUsername, error) {
	if s == nil {
		return parsedProxyUsername{}, errProxyAuthInvalid
	}
	_, _, required := s.authCredentials()
	user, pass, ok := proxyBasicAuth(req)
	if !ok {
		if required {
			return parsedProxyUsername{}, errProxyAuthRequired
		}
		return parsedProxyUsername{}, nil
	}
	return s.authenticateProxy(user, pass)
}

func constantTimeEqual(left, right string) bool {
	leftSum := sha256.Sum256([]byte(left))
	rightSum := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftSum[:], rightSum[:]) == 1
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func peerAddr(addr net.Addr) netip.Addr {
	if addr == nil {
		return netip.Addr{}
	}
	switch v := addr.(type) {
	case *net.TCPAddr:
		if v == nil {
			return netip.Addr{}
		}
		if ip, ok := netip.AddrFromSlice(v.IP); ok {
			return ip.Unmap()
		}
		return netip.Addr{}
	default:
		host, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			host = addr.String()
		}
		ip, err := netip.ParseAddr(host)
		if err != nil {
			return netip.Addr{}
		}
		return ip.Unmap()
	}
}

func directiveFromProfile(resolution profile.Resolution) pool.SelectionDirective {
	if resolution.Profile == nil {
		return pool.SelectionDirective{}
	}
	selection := resolution.Profile.Selection()
	return pool.SelectionDirective{
		ProfileID:       resolution.ProfileID,
		ProfileRevision: resolution.ProfileRevision,
		Strategy:        pool.NormalizeStrategy(selection.DefaultStrategy),
		SessionTTL:      selection.SessionTTL,
		Filter: pool.NodeFilter{
			Countries: append([]string(nil), selection.Filter.Countries...),
			Regions:   append([]string(nil), selection.Filter.Regions...),
			LongLived: cloneBool(selection.Filter.LongLived),
		},
		LongLived: pool.LongLivedPolicy{
			MinUptime:      selection.LongLivedMinUptime,
			MinSuccessRate: selection.LongLivedMinSuccessRate,
		},
	}
}

func policyForProfile(split bool, compiled *profile.CompiledProfile, host string) routerule.Policy {
	if !split || compiled == nil {
		return routerule.PolicyProxy
	}
	return compiled.Match(host)
}

func (s *Server) resolveLegacyRequest(auth parsedProxyUsername, requestOverlay directiveOverlay, host string, sessionFallback string) (resolved, routerule.Policy) {
	overlay := s.bound.merge(auth.Overlay).merge(requestOverlay)
	res := overlay.resolve(s.DefaultStrategy(), sessionFallback)
	return res, policyForSplit(res.split, s.currentEngine(), host)
}

func (s *Server) resolveProfileRequest(auth parsedProxyUsername, requestOverlay directiveOverlay, host string, remoteAddr net.Addr) (resolved, routerule.Policy, profile.Resolution, error) {
	if s == nil || s.cfg.Profiles == nil {
		res, policy := s.resolveLegacyRequest(auth, requestOverlay, host, peerAddr(remoteAddr).String())
		return res, policy, profile.Resolution{}, nil
	}

	peer := peerAddr(remoteAddr)
	resolution := s.cfg.Profiles.Resolve(profile.RequestIdentity{
		ExplicitDeviceID: auth.ExplicitDeviceID,
		PeerIP:           peer,
	})
	s.cfg.Profiles.Observe(resolution, peer, time.Now().UTC())

	base := directiveFromProfile(resolution)
	if resolution.Profile == nil || !resolution.Profile.Enabled() {
		return resolved{directive: base, split: true}, routerule.PolicyDirect, resolution, nil
	}

	overlay := s.bound.merge(auth.Overlay).merge(requestOverlay)
	final := overlay.applyTo(base, peer.String())
	return final, policyForProfile(final.split, resolution.Profile, host), resolution, nil
}
