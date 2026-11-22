package dispatch

import (
	"easy_proxies/internal/outbound/pool"
	"strings"
)

// ProtectedClient is an opt-in, server-owned single-node policy. Its password
// is a proxy credential, never a model-provider credential. NodeTag identifies
// a route, not a promise that the upstream public IP remains unchanged.
type ProtectedClient struct {
	Password string
	NodeTag  string
}

func (s *Server) authenticateProtectedClient(username, password string) (parsedProxyUsername, bool, error) {
	if s == nil {
		return parsedProxyUsername{}, false, nil
	}
	base, _ := splitBaseUsername(username)
	policy, found := s.cfg.ProtectedClients[base]
	if !found {
		return parsedProxyUsername{}, false, nil
	}
	// Dedicated identities have no client-controlled routing parameters. Reject
	// malformed/empty bindings and all suffixes instead of silently falling back.
	if username != base || policy.Password == "" || strings.TrimSpace(policy.NodeTag) == "" || !constantTimeEqual(password, policy.Password) {
		return parsedProxyUsername{}, true, errProxyAuthInvalid
	}
	return parsedProxyUsername{BaseUsername: base, protectedNode: policy.NodeTag}, true, nil
}

func protectedResolution(tag string) resolved {
	return resolved{directive: pool.SelectionDirective{RequiredTag: tag}, split: false}
}
