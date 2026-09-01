package app

import (
	"reflect"
	"strings"

	"easy_proxies/internal/boxmgr"
	"easy_proxies/internal/config"
	"easy_proxies/internal/routerule"
)

func cloneConfigSnapshot(cfg *config.Config) *config.Config {
	if cfg == nil {
		return nil
	}
	cfg.RLock()
	defer cfg.RUnlock()
	return cfg.Clone()
}

func cloneRoutingState(state boxmgr.ReloadState) boxmgr.ReloadState {
	return boxmgr.ReloadState{
		Config: cloneConfigSnapshot(state.Config),
		Idle:   state.Idle,
	}
}

type routingTopology struct {
	enabled     bool
	localServer bool
	listen      string
	username    string
	password    string
}

func routingEnabled(state boxmgr.ReloadState) bool {
	if state.Config == nil {
		return false
	}
	if state.Config.LocalServer.Enabled {
		return true
	}
	return state.Config.Routing.Enabled && !state.Idle
}

func routingTopologyFor(state boxmgr.ReloadState) routingTopology {
	topology := routingTopology{enabled: routingEnabled(state)}
	if !topology.enabled {
		return topology
	}
	topology.listen = state.Config.DispatchListen()
	if state.Config.LocalServer.Enabled {
		topology.localServer = true
		return topology
	}
	topology.username = state.Config.Listener.Username
	topology.password = state.Config.Listener.Password
	return topology
}

func routingRuntimeChanged(from, to *config.Config) bool {
	if from == nil || to == nil {
		return from != to
	}
	return !reflect.DeepEqual(from.Routing, to.Routing) || routingGeoChanged(from, to)
}

func routingHotApplyCompatible(from, to *config.Config) bool {
	if from == nil || to == nil {
		return from == to
	}
	fromComparable := cloneConfigSnapshot(from)
	toComparable := cloneConfigSnapshot(to)
	clearHotRoutingFields(fromComparable)
	clearHotRoutingFields(toComparable)
	return reflect.DeepEqual(fromComparable, toComparable)
}

func clearHotRoutingFields(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if cfg.LocalServer.Enabled {
		cfg.Routing.Enabled = false
		cfg.Routing.Session = config.SessionConfig{}
		cfg.Routing.NodeFilter = config.RoutingNodeFilterConfig{}
		cfg.LocalServer.Auth = config.LocalServerAuthConfig{}
		cfg.LocalServer.SharedRevision = 0
		cfg.LocalServer.CredentialGeneration = 0
		cfg.Listener.Username = ""
		cfg.Listener.Password = ""
		cfg.Management.Password = ""
	}
	cfg.Routing.DefaultStrategy = ""
	cfg.Routing.UseDefaultRules = nil
	cfg.Routing.FinalPolicy = ""
	cfg.Routing.Rules = nil
	cfg.Routing.RuleProviders = nil
	cfg.Routing.LongLived = config.LongLivedConfig{}
	if cfg.Routing.Enabled {
		cfg.GeoIP.Enabled = false
		cfg.GeoIP.DatabasePath = ""
	}
}

func routingGeoChanged(from, to *config.Config) bool {
	if from == nil || to == nil {
		return from != to
	}
	if from.GeoIP.Enabled != to.GeoIP.Enabled {
		return true
	}
	if !from.GeoIP.Enabled {
		return false
	}
	return strings.TrimSpace(from.GeoIP.DatabasePath) != strings.TrimSpace(to.GeoIP.DatabasePath) ||
		from.GeoIP.AutoUpdateEnabled != to.GeoIP.AutoUpdateEnabled ||
		from.GeoIP.AutoUpdateInterval != to.GeoIP.AutoUpdateInterval
}

func routingEngineInputsChanged(from, to *config.Config) bool {
	if from == nil || to == nil {
		return from != to
	}
	if from.RoutingUseDefaultRules() != to.RoutingUseDefaultRules() ||
		routerule.NormalizePolicy(from.Routing.FinalPolicy) != routerule.NormalizePolicy(to.Routing.FinalPolicy) ||
		!reflect.DeepEqual(from.Routing.Rules, to.Routing.Rules) ||
		!reflect.DeepEqual(from.Routing.RuleFiles, to.Routing.RuleFiles) ||
		!reflect.DeepEqual(from.Routing.RuleProviders, to.Routing.RuleProviders) {
		return true
	}
	// A referenced local file may change without its path changing. Any config
	// reload with local files present must rematerialize the rule layer.
	if len(from.Routing.RuleFiles) > 0 || len(to.Routing.RuleFiles) > 0 {
		return true
	}
	return false
}

func validateRuleProviders(providers []config.RuleProvider) error {
	return config.ValidateRuleProviders(providers)
}
