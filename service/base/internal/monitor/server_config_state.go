package monitor

import (
	"reflect"
	"strings"
	"time"

	"easy_proxies/internal/config"
)

type persistedServerConfig struct {
	enabled        bool
	listen         string
	externalIP     string
	probeTarget    string
	probeTargets   []string
	password       string
	proxyUsername  string
	proxyPassword  string
	skipCertVerify bool
}

func snapshotPersistedServerConfig(cfg *config.Config) (persistedServerConfig, bool) {
	if cfg == nil {
		return persistedServerConfig{}, false
	}
	cfg.RLock()
	defer cfg.RUnlock()
	snapshot := persistedServerConfig{
		enabled:        cfg.ManagementEnabled(),
		listen:         cfg.Management.Listen,
		externalIP:     cfg.ExternalIP,
		probeTarget:    cfg.Management.ProbeTarget,
		probeTargets:   append([]string(nil), cfg.Management.ProbeTargets...),
		password:       cfg.Management.Password,
		skipCertVerify: cfg.SkipCertVerify,
	}
	if cfg.Mode == "hybrid" || cfg.Mode == "multi-port" {
		snapshot.proxyUsername = cfg.MultiPort.Username
		snapshot.proxyPassword = cfg.MultiPort.Password
	} else {
		snapshot.proxyUsername = cfg.Listener.Username
		snapshot.proxyPassword = cfg.Listener.Password
	}
	return snapshot, true
}

func applyPersistedServerConfig(dst *Config, snapshot persistedServerConfig) {
	dst.Enabled = snapshot.enabled
	dst.Listen = snapshot.listen
	dst.ExternalIP = snapshot.externalIP
	dst.ProbeTarget = snapshot.probeTarget
	dst.ProbeTargets = append([]string(nil), snapshot.probeTargets...)
	dst.Password = snapshot.password
	dst.ProxyUsername = snapshot.proxyUsername
	dst.ProxyPassword = snapshot.proxyPassword
	dst.SkipCertVerify = snapshot.skipCertVerify
}

func cloneRuntimeConfig(cfg Config) Config {
	cfg.ProbeTargets = append([]string(nil), cfg.ProbeTargets...)
	return cfg
}

func (s *Server) applyPreparedConfig(source *config.Config, snapshot persistedServerConfig) {
	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()
	s.applyPreparedConfigLocked(source, snapshot)
}

func (s *Server) applyPreparedConfigLocked(source *config.Config, snapshot persistedServerConfig) {
	s.cfgMu.Lock()
	s.cfgSrc = source
	applyPersistedServerConfig(&s.cfg, snapshot)
	s.cfgMu.Unlock()
	s.localServerReloadPending = false
}

func (s *Server) restorePreparedConfig(source *config.Config, runtimeCfg Config) {
	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()
	s.restorePreparedConfigLocked(source, runtimeCfg)
}

func (s *Server) restorePreparedConfigLocked(source *config.Config, runtimeCfg Config) {
	s.cfgMu.Lock()
	s.cfgSrc = source
	s.cfg = cloneRuntimeConfig(runtimeCfg)
	s.cfgMu.Unlock()
	s.localServerReloadPending = false
}

func settingsChangeRequiresReload(c *config.Config, req allSettingsRequest) bool {
	if c == nil {
		return true
	}

	normalizedListenerProtocol := req.ListenerProtocol
	if p, err := config.NormalizeInboundProtocol(req.ListenerProtocol); err == nil {
		normalizedListenerProtocol = p
	}
	normalizedMultiPortProtocol := req.MultiPortProtocol
	if p, err := config.NormalizeInboundProtocol(req.MultiPortProtocol); err == nil {
		normalizedMultiPortProtocol = p
	}
	probeTarget := strings.TrimSpace(req.ManagementProbeTarget)
	sourceManifestURL := strings.TrimSpace(req.SourceSyncManifestURL)
	sourceManifestToken := strings.TrimSpace(req.SourceSyncManifestToken)
	defaultDirectProxyScheme := strings.TrimSpace(req.SourceSyncDefaultDirectProxyScheme)
	if defaultDirectProxyScheme == "" {
		defaultDirectProxyScheme = "http"
	}

	if c.Mode != req.Mode ||
		c.LogLevel != req.LogLevel ||
		c.SkipCertVerify != req.SkipCertVerify ||
		c.Listener.Address != req.ListenerAddress ||
		c.Listener.Port != req.ListenerPort ||
		c.Listener.Protocol != normalizedListenerProtocol ||
		c.Listener.Username != req.ListenerUsername ||
		c.Listener.Password != req.ListenerPassword ||
		c.MultiPort.Address != req.MultiPortAddress ||
		c.MultiPort.BasePort != req.MultiPortBasePort ||
		c.MultiPort.Protocol != normalizedMultiPortProtocol ||
		c.MultiPort.Username != req.MultiPortUsername ||
		c.MultiPort.Password != req.MultiPortPassword ||
		c.Pool.Mode != req.PoolMode ||
		c.Pool.FailureThreshold != req.PoolFailureThreshold ||
		c.Management.Listen != req.ManagementListen ||
		c.Management.Password != req.ManagementPassword ||
		c.SourceSync.Enabled != req.SourceSyncEnabled ||
		c.SourceSync.ManifestURL != sourceManifestURL ||
		c.SourceSync.ManifestToken != sourceManifestToken ||
		c.SourceSync.DefaultDirectProxyScheme != defaultDirectProxyScheme ||
		!reflect.DeepEqual(c.SourceSync.FallbackSubscriptions, req.SourceSyncFallbackSubscriptions) ||
		!reflect.DeepEqual(c.Subscriptions, req.Subscriptions) ||
		c.SubscriptionRefresh.Enabled != req.SubRefreshEnabled ||
		c.SubscriptionRefresh.MinAvailableNodes != req.SubRefreshMinAvailableNodes ||
		c.GeoIP.Enabled != req.GeoIPEnabled ||
		strings.TrimSpace(c.GeoIP.DatabasePath) != strings.TrimSpace(req.GeoIPDatabasePath) ||
		c.GeoIP.AutoUpdateEnabled != req.GeoIPAutoUpdateEnabled {
		return true
	}

	if req.ManagementEnabled != nil {
		currentEnabled := c.ManagementEnabled()
		if currentEnabled != *req.ManagementEnabled {
			return true
		}
	}

	if c.Management.ProbeTarget != probeTarget {
		return true
	}

	if d, err := time.ParseDuration(req.PoolBlacklistDuration); err == nil && d > 0 && c.Pool.BlacklistDuration != d {
		return true
	}
	if d, err := time.ParseDuration(req.SubRefreshDrainTimeout); err == nil && d > 0 && c.SubscriptionRefresh.DrainTimeout != d {
		return true
	}
	if d, err := time.ParseDuration(req.SubRefreshInterval); err == nil && d > 0 && c.SubscriptionRefresh.Interval != d {
		return true
	}
	if d, err := time.ParseDuration(req.SubRefreshTimeout); err == nil && d > 0 && c.SubscriptionRefresh.Timeout != d {
		return true
	}
	if d, err := time.ParseDuration(req.SubRefreshHealthCheckTimeout); err == nil && d > 0 && c.SubscriptionRefresh.HealthCheckTimeout != d {
		return true
	}
	if d, err := time.ParseDuration(req.SourceSyncRefreshInterval); err == nil && d > 0 && c.SourceSync.RefreshInterval != d {
		return true
	}
	if d, err := time.ParseDuration(req.SourceSyncRequestTimeout); err == nil && d > 0 && c.SourceSync.RequestTimeout != d {
		return true
	}
	if d, err := time.ParseDuration(req.GeoIPAutoUpdateInterval); err == nil && d > 0 && c.GeoIP.AutoUpdateInterval != d {
		return true
	}
	return false
}

// allSettingsResponse is the JSON structure for GET /api/settings.
