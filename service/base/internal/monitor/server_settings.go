package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"easy_proxies/internal/config"
)

type allSettingsResponse struct {
	// Global
	Mode           string `json:"mode"`
	LogLevel       string `json:"log_level"`
	ExternalIP     string `json:"external_ip"`
	SkipCertVerify bool   `json:"skip_cert_verify"`

	// Listener
	ListenerAddress  string `json:"listener_address"`
	ListenerPort     uint16 `json:"listener_port"`
	ListenerProtocol string `json:"listener_protocol"`
	ListenerUsername string `json:"listener_username"`
	ListenerPassword string `json:"listener_password"`

	// Multi-port
	MultiPortAddress  string `json:"multi_port_address"`
	MultiPortBasePort uint16 `json:"multi_port_base_port"`
	MultiPortProtocol string `json:"multi_port_protocol"`
	MultiPortUsername string `json:"multi_port_username"`
	MultiPortPassword string `json:"multi_port_password"`

	// Pool
	PoolMode              string `json:"pool_mode"`
	PoolFailureThreshold  int    `json:"pool_failure_threshold"`
	PoolBlacklistDuration string `json:"pool_blacklist_duration"`

	// Management
	ManagementEnabled             bool   `json:"management_enabled"`
	ManagementListen              string `json:"management_listen"`
	ManagementProbeTarget         string `json:"management_probe_target"`
	ManagementPassword            string `json:"management_password"`
	ManagementHealthCheckInterval string `json:"management_health_check_interval"`

	// Subscription refresh
	SubRefreshEnabled            bool   `json:"sub_refresh_enabled"`
	SubRefreshInterval           string `json:"sub_refresh_interval"`
	SubRefreshTimeout            string `json:"sub_refresh_timeout"`
	SubRefreshHealthCheckTimeout string `json:"sub_refresh_health_check_timeout"`
	SubRefreshDrainTimeout       string `json:"sub_refresh_drain_timeout"`
	SubRefreshMinAvailableNodes  int    `json:"sub_refresh_min_available_nodes"`

	// Source sync
	SourceSyncEnabled                  bool     `json:"source_sync_enabled"`
	SourceSyncManifestURL              string   `json:"source_sync_manifest_url"`
	SourceSyncManifestToken            string   `json:"source_sync_manifest_token"`
	SourceSyncRefreshInterval          string   `json:"source_sync_refresh_interval"`
	SourceSyncRequestTimeout           string   `json:"source_sync_request_timeout"`
	SourceSyncFallbackSubscriptions    []string `json:"source_sync_fallback_subscriptions"`
	SourceSyncDefaultDirectProxyScheme string   `json:"source_sync_default_direct_proxy_scheme"`

	// GeoIP
	GeoIPEnabled            bool   `json:"geoip_enabled"`
	GeoIPDatabasePath       string `json:"geoip_database_path"`
	GeoIPAutoUpdateEnabled  bool   `json:"geoip_auto_update_enabled"`
	GeoIPAutoUpdateInterval string `json:"geoip_auto_update_interval"`

	// Subscriptions
	Subscriptions []string `json:"subscriptions"`
}

// allSettingsRequest is the JSON structure for PUT /api/settings.
type allSettingsRequest struct {
	// Global
	Mode           string `json:"mode"`
	LogLevel       string `json:"log_level"`
	ExternalIP     string `json:"external_ip"`
	SkipCertVerify bool   `json:"skip_cert_verify"`

	// Listener
	ListenerAddress  string `json:"listener_address"`
	ListenerPort     uint16 `json:"listener_port"`
	ListenerProtocol string `json:"listener_protocol"`
	ListenerUsername string `json:"listener_username"`
	ListenerPassword string `json:"listener_password"`

	// Multi-port
	MultiPortAddress  string `json:"multi_port_address"`
	MultiPortBasePort uint16 `json:"multi_port_base_port"`
	MultiPortProtocol string `json:"multi_port_protocol"`
	MultiPortUsername string `json:"multi_port_username"`
	MultiPortPassword string `json:"multi_port_password"`

	// Pool
	PoolMode              string `json:"pool_mode"`
	PoolFailureThreshold  int    `json:"pool_failure_threshold"`
	PoolBlacklistDuration string `json:"pool_blacklist_duration"`

	// Management
	ManagementEnabled             *bool  `json:"management_enabled"`
	ManagementListen              string `json:"management_listen"`
	ManagementProbeTarget         string `json:"management_probe_target"`
	ManagementPassword            string `json:"management_password"`
	ManagementHealthCheckInterval string `json:"management_health_check_interval"`

	// Subscription refresh
	SubRefreshEnabled            bool   `json:"sub_refresh_enabled"`
	SubRefreshInterval           string `json:"sub_refresh_interval"`
	SubRefreshTimeout            string `json:"sub_refresh_timeout"`
	SubRefreshHealthCheckTimeout string `json:"sub_refresh_health_check_timeout"`
	SubRefreshDrainTimeout       string `json:"sub_refresh_drain_timeout"`
	SubRefreshMinAvailableNodes  int    `json:"sub_refresh_min_available_nodes"`

	// Source sync
	SourceSyncEnabled                  bool     `json:"source_sync_enabled"`
	SourceSyncManifestURL              string   `json:"source_sync_manifest_url"`
	SourceSyncManifestToken            string   `json:"source_sync_manifest_token"`
	SourceSyncRefreshInterval          string   `json:"source_sync_refresh_interval"`
	SourceSyncRequestTimeout           string   `json:"source_sync_request_timeout"`
	SourceSyncFallbackSubscriptions    []string `json:"source_sync_fallback_subscriptions"`
	SourceSyncDefaultDirectProxyScheme string   `json:"source_sync_default_direct_proxy_scheme"`

	// GeoIP
	GeoIPEnabled            bool   `json:"geoip_enabled"`
	GeoIPDatabasePath       string `json:"geoip_database_path"`
	GeoIPAutoUpdateEnabled  bool   `json:"geoip_auto_update_enabled"`
	GeoIPAutoUpdateInterval string `json:"geoip_auto_update_interval"`

	// Subscriptions
	Subscriptions []string `json:"subscriptions"`
}

// getAllSettings reads all config fields into a flat response (thread-safe).
func (s *Server) getAllSettings() allSettingsResponse {
	s.cfgMu.RLock()
	c := s.cfgSrc
	s.cfgMu.RUnlock()

	if c == nil {
		return allSettingsResponse{}
	}

	c.RLock()
	defer c.RUnlock()
	mgmtEnabled := true
	if c.Management.Enabled != nil {
		mgmtEnabled = *c.Management.Enabled
	}

	return allSettingsResponse{
		Mode:           c.Mode,
		LogLevel:       c.LogLevel,
		ExternalIP:     c.ExternalIP,
		SkipCertVerify: c.SkipCertVerify,

		ListenerAddress:  c.Listener.Address,
		ListenerPort:     c.Listener.Port,
		ListenerProtocol: c.Listener.Protocol,
		ListenerUsername: c.Listener.Username,
		ListenerPassword: c.Listener.Password,

		MultiPortAddress:  c.MultiPort.Address,
		MultiPortBasePort: c.MultiPort.BasePort,
		MultiPortProtocol: c.MultiPort.Protocol,
		MultiPortUsername: c.MultiPort.Username,
		MultiPortPassword: c.MultiPort.Password,

		PoolMode:              c.Pool.Mode,
		PoolFailureThreshold:  c.Pool.FailureThreshold,
		PoolBlacklistDuration: c.Pool.BlacklistDuration.String(),

		ManagementEnabled:             mgmtEnabled,
		ManagementListen:              c.Management.Listen,
		ManagementProbeTarget:         c.Management.ProbeTarget,
		ManagementPassword:            c.Management.Password,
		ManagementHealthCheckInterval: c.Management.HealthCheckInterval.String(),

		SubRefreshEnabled:            c.SubscriptionRefresh.Enabled,
		SubRefreshInterval:           c.SubscriptionRefresh.Interval.String(),
		SubRefreshTimeout:            c.SubscriptionRefresh.Timeout.String(),
		SubRefreshHealthCheckTimeout: c.SubscriptionRefresh.HealthCheckTimeout.String(),
		SubRefreshDrainTimeout:       c.SubscriptionRefresh.DrainTimeout.String(),
		SubRefreshMinAvailableNodes:  c.SubscriptionRefresh.MinAvailableNodes,

		SourceSyncEnabled:                  c.SourceSync.Enabled,
		SourceSyncManifestURL:              c.SourceSync.ManifestURL,
		SourceSyncManifestToken:            c.SourceSync.ManifestToken,
		SourceSyncRefreshInterval:          c.SourceSync.RefreshInterval.String(),
		SourceSyncRequestTimeout:           c.SourceSync.RequestTimeout.String(),
		SourceSyncFallbackSubscriptions:    c.SourceSync.FallbackSubscriptions,
		SourceSyncDefaultDirectProxyScheme: c.SourceSync.DefaultDirectProxyScheme,

		GeoIPEnabled:            c.GeoIP.Enabled,
		GeoIPDatabasePath:       c.GeoIP.DatabasePath,
		GeoIPAutoUpdateEnabled:  c.GeoIP.AutoUpdateEnabled,
		GeoIPAutoUpdateInterval: c.GeoIP.AutoUpdateInterval.String(),

		Subscriptions: c.Subscriptions,
	}
}

func (s *Server) getLocalServerSettings() map[string]any {
	legacy := s.getAllSettings()
	encoded, err := json.Marshal(legacy)
	if err != nil {
		return map[string]any{}
	}
	response := make(map[string]any)
	if err := json.Unmarshal(encoded, &response); err != nil {
		return map[string]any{}
	}
	delete(response, "listener_password")
	delete(response, "management_password")
	delete(response, "multi_port_password")
	view := s.getLocalServerConfig()
	response["local_server_enabled"] = view.Enabled
	response["local_server_auth_username"] = view.AuthUsername
	response["local_server_password_set"] = view.PasswordSet
	return response
}

func applyAllSettingsRequest(c *config.Config, req allSettingsRequest) {
	if c == nil {
		return
	}
	c.Mode = req.Mode
	c.LogLevel = req.LogLevel
	c.ExternalIP = strings.TrimSpace(req.ExternalIP)
	c.SkipCertVerify = req.SkipCertVerify

	c.Listener.Address = req.ListenerAddress
	c.Listener.Port = req.ListenerPort
	if protocol, err := config.NormalizeInboundProtocol(req.ListenerProtocol); err == nil {
		c.Listener.Protocol = protocol
	}
	c.Listener.Username = req.ListenerUsername
	c.Listener.Password = req.ListenerPassword

	c.MultiPort.Address = req.MultiPortAddress
	c.MultiPort.BasePort = req.MultiPortBasePort
	if protocol, err := config.NormalizeInboundProtocol(req.MultiPortProtocol); err == nil {
		c.MultiPort.Protocol = protocol
	}
	c.MultiPort.Username = req.MultiPortUsername
	c.MultiPort.Password = req.MultiPortPassword

	c.Pool.Mode = req.PoolMode
	c.Pool.FailureThreshold = req.PoolFailureThreshold
	if duration, err := time.ParseDuration(req.PoolBlacklistDuration); err == nil && duration > 0 {
		c.Pool.BlacklistDuration = duration
	}

	if req.ManagementEnabled != nil {
		enabled := *req.ManagementEnabled
		c.Management.Enabled = &enabled
	}
	c.Management.Listen = req.ManagementListen
	c.Management.ProbeTarget = strings.TrimSpace(req.ManagementProbeTarget)
	if c.Management.ProbeTarget != "" {
		c.Management.ProbeTargets = nil
	}
	c.Management.Password = req.ManagementPassword
	if duration, err := time.ParseDuration(req.ManagementHealthCheckInterval); err == nil && duration > 0 {
		c.Management.HealthCheckInterval = duration
	}

	c.SubscriptionRefresh.Enabled = req.SubRefreshEnabled
	if duration, err := time.ParseDuration(req.SubRefreshInterval); err == nil && duration > 0 {
		c.SubscriptionRefresh.Interval = duration
	}
	if duration, err := time.ParseDuration(req.SubRefreshTimeout); err == nil && duration > 0 {
		c.SubscriptionRefresh.Timeout = duration
	}
	if duration, err := time.ParseDuration(req.SubRefreshHealthCheckTimeout); err == nil && duration > 0 {
		c.SubscriptionRefresh.HealthCheckTimeout = duration
	}
	if duration, err := time.ParseDuration(req.SubRefreshDrainTimeout); err == nil && duration > 0 {
		c.SubscriptionRefresh.DrainTimeout = duration
	}
	c.SubscriptionRefresh.MinAvailableNodes = req.SubRefreshMinAvailableNodes

	c.SourceSync.Enabled = req.SourceSyncEnabled
	c.SourceSync.ManifestURL = strings.TrimSpace(req.SourceSyncManifestURL)
	c.SourceSync.ManifestToken = strings.TrimSpace(req.SourceSyncManifestToken)
	if duration, err := time.ParseDuration(req.SourceSyncRefreshInterval); err == nil && duration > 0 {
		c.SourceSync.RefreshInterval = duration
	}
	if duration, err := time.ParseDuration(req.SourceSyncRequestTimeout); err == nil && duration > 0 {
		c.SourceSync.RequestTimeout = duration
	}
	c.SourceSync.FallbackSubscriptions = append([]string(nil), req.SourceSyncFallbackSubscriptions...)
	c.SourceSync.DefaultDirectProxyScheme = strings.TrimSpace(req.SourceSyncDefaultDirectProxyScheme)
	if c.SourceSync.DefaultDirectProxyScheme == "" {
		c.SourceSync.DefaultDirectProxyScheme = "http"
	}

	c.GeoIP.Enabled = req.GeoIPEnabled
	c.GeoIP.DatabasePath = req.GeoIPDatabasePath
	c.GeoIP.AutoUpdateEnabled = req.GeoIPAutoUpdateEnabled
	if duration, err := time.ParseDuration(req.GeoIPAutoUpdateInterval); err == nil && duration > 0 {
		c.GeoIP.AutoUpdateInterval = duration
	}
	c.Subscriptions = append([]string(nil), req.Subscriptions...)
}

// commitAllSettings copies only fields owned by PUT /api/settings. The target
// config remains the object shared by BoxManager and the monitor server.
func commitAllSettings(target, candidate *config.Config) {
	if target == nil || candidate == nil {
		return
	}
	target.Mode = candidate.Mode
	target.LogLevel = candidate.LogLevel
	target.ExternalIP = candidate.ExternalIP
	target.SkipCertVerify = candidate.SkipCertVerify
	target.Listener = candidate.Listener
	target.MultiPort = candidate.MultiPort
	target.Pool = candidate.Pool
	target.Management = candidate.Management
	target.SubscriptionRefresh = candidate.SubscriptionRefresh
	target.SourceSync = candidate.SourceSync
	target.GeoIP = candidate.GeoIP
	target.Subscriptions = candidate.Subscriptions
}

// updateAllSettings applies all settings from request and persists to config file.
func (s *Server) updateAllSettings(req allSettingsRequest) error {
	_, err := s.updateAllSettingsWithReload(req)
	return err
}

func (s *Server) updateAllSettingsWithReload(req allSettingsRequest) (bool, error) {
	// Validate request before applying
	if err := config.ValidateSettingsRequest(
		req.Mode, req.ListenerPort, req.MultiPortBasePort,
		req.ListenerProtocol, req.MultiPortProtocol, req.PoolMode,
		req.PoolBlacklistDuration, req.SubRefreshInterval, req.SubRefreshTimeout,
		req.SubRefreshHealthCheckTimeout, req.SubRefreshDrainTimeout,
		req.SourceSyncRefreshInterval, req.SourceSyncRequestTimeout,
		req.GeoIPAutoUpdateInterval, req.ManagementHealthCheckInterval,
	); err != nil {
		return false, fmt.Errorf("参数验证失败: %w", err)
	}

	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()
	s.cfgMu.RLock()
	c := s.cfgSrc
	s.cfgMu.RUnlock()

	if c == nil {
		return false, errors.New("配置存储未初始化")
	}
	if s.reloadWindowCount > 0 {
		return false, errReloadInProgress
	}

	// Build and persist an isolated candidate before changing the live config.
	// SaveSettings can fail (read-only/missing path); committing only afterward
	// keeps the in-memory source, monitor runtime, and YAML in one state.
	c.Lock()
	effectiveReq := req
	preserveLocalServerCredentials := c.LocalServer.Enabled
	if profiles := s.profileManagerSnapshot(); profiles != nil && profiles.LocalServerEnabled() {
		preserveLocalServerCredentials = true
	}
	if preserveLocalServerCredentials {
		effectiveReq.ListenerUsername = c.LocalServer.Auth.Username
		effectiveReq.ListenerPassword = c.LocalServer.Auth.Password
		effectiveReq.ManagementPassword = c.LocalServer.Auth.Password
	}
	needReload := settingsChangeRequiresReload(c, effectiveReq)
	candidate := c.Clone()
	applyAllSettingsRequest(candidate, effectiveReq)
	candidate.Lock()
	err := candidate.SaveSettings()
	candidate.Unlock()
	if err != nil {
		c.Unlock()
		return false, fmt.Errorf("保存配置失败: %w", err)
	}
	commitAllSettings(c, candidate)
	c.Unlock()

	// Sync ALL monitor-level config fields only after persistence succeeds.
	runtimeCfg, _ := snapshotPersistedServerConfig(candidate)
	s.cfgMu.Lock()
	applyPersistedServerConfig(&s.cfg, runtimeCfg)
	s.cfgMu.Unlock()

	// 动态更新 Manager 的探测目标，使其立即生效
	if (candidate.Management.ProbeTarget != "" || len(candidate.Management.ProbeTargets) > 0) && s.mgr != nil {
		if err := s.mgr.UpdateProbeTargets(candidate.Management.ProbeTargets, candidate.Management.ProbeTarget); err != nil {
			s.logger.Printf("更新探测目标失败: %v", err)
		}
	}
	if s.mgr != nil {
		s.mgr.SetSkipCertVerify(candidate.SkipCertVerify)
	}
	// 动态更新周期健康检查间隔，使其立即生效
	if candidate.Management.HealthCheckInterval > 0 && s.mgr != nil {
		s.mgr.SetHealthCheckInterval(candidate.Management.HealthCheckInterval)
	}

	s.logger.Printf("✅ 设置已保存并同步到运行时")
	return needReload, nil
}

// Start synchronously binds and activates the configured HTTP listener.
