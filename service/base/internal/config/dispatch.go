package config

import (
	"net"
	"strconv"
	"strings"
)

func (c *Config) ManagementEnabled() bool {
	if c.Management.Enabled == nil {
		return true
	}
	return *c.Management.Enabled
}

// RoutingUseDefaultRules reports whether the built-in default rule set should be
// appended after user rules. Defaults to true when unset.
func (c *Config) RoutingUseDefaultRules() bool {
	if c == nil || c.Routing.UseDefaultRules == nil {
		return true
	}
	return *c.Routing.UseDefaultRules
}

// DispatchListen returns the host:port the smart dispatch entry should bind when
// routing is enabled. An explicit routing.listen wins; otherwise the dispatcher
// takes over the plain listener's host:port (route A — it becomes the default
// proxy entry). This is the single source of truth shared by the builder (to
// decide whether to drop the pool inbound) and app wiring (to start the server).
func (c *Config) DispatchListen() string {
	if c == nil {
		return ""
	}
	if c != nil && c.LocalServer.Enabled {
		if listen := strings.TrimSpace(c.LocalServer.Listen); listen != "" {
			return listen
		}
	}
	return c.legacyDispatchListen()
}

// RoutingTakesOverPoolInbound reports whether the smart dispatch entry binds the
// same host:port as the plain pool inbound. When true the builder must omit the
// pool inbound so the dispatcher can bind that port (the pool outbound is still
// built and dialed directly by the dispatcher). When false (routing disabled, or
// routing.listen points at a different port — route B) both entries coexist.
func (c *Config) RoutingTakesOverPoolInbound() bool {
	if c == nil || !c.Routing.Enabled {
		return false
	}
	poolInbound := net.JoinHostPort(normalizeHostForCompare(c.Listener.Address), strconv.Itoa(int(c.Listener.Port)))
	dispatch := c.legacyDispatchListen()
	if host, port, err := net.SplitHostPort(dispatch); err == nil {
		dispatch = net.JoinHostPort(normalizeHostForCompare(host), port)
	}
	return dispatch == poolInbound
}

func (c *Config) DispatchOwnsPrimaryInbound() bool {
	if c == nil {
		return false
	}
	if c.LocalServer.Enabled {
		return true
	}
	return c.RoutingTakesOverPoolInbound()
}

func (c *Config) DispatchEnabled() bool {
	return c != nil && (c.LocalServer.Enabled || c.Routing.Enabled)
}

func (c *Config) legacyDispatchListen() string {
	host := "0.0.0.0"
	port := uint16(22323)
	if c != nil {
		if listen := strings.TrimSpace(c.Routing.Listen); listen != "" {
			return listen
		}
		if addr := strings.TrimSpace(c.Listener.Address); addr != "" {
			host = addr
		}
		if c.Listener.Port != 0 {
			port = c.Listener.Port
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(int(port)))
}

// normalizeHostForCompare canonicalizes bind hosts so that equivalent forms
// ("", "0.0.0.0", "::") compare equal when deciding port takeover.
func normalizeHostForCompare(host string) string {
	h := strings.TrimSpace(host)
	switch h {
	case "", "0.0.0.0", "::", "[::]":
		return "0.0.0.0"
	default:
		return h
	}
}

// ConnectorRuntimeEnabled reports whether manifest connectors should be executed locally.
func (c *Config) ConnectorRuntimeEnabled() bool {
	if c == nil || c.SourceSync.ConnectorRuntime.Enabled == nil {
		return true
	}
	return *c.SourceSync.ConnectorRuntime.Enabled
}

// loadNodesFromFile reads a nodes file where each line is a proxy URI
// Lines starting with # are comments, empty lines are ignored
