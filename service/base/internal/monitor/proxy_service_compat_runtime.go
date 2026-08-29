package monitor

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"easy_proxies/internal/profile"
)

func (s *Server) buildProxyCompatInstance(r *http.Request, runtimeCfg proxyCompatRuntime) proxyCompatProviderInstance {
	allNodes := s.mgr.Snapshot()
	effectiveNodes := filterEffectiveSnapshots(allNodes)
	avgLatency := int64(0)
	if len(effectiveNodes) > 0 {
		var totalLatency int64
		var count int64
		for _, snap := range effectiveNodes {
			if snap.LastLatencyMs <= 0 {
				continue
			}
			totalLatency += snap.LastLatencyMs
			count++
		}
		if count > 0 {
			avgLatency = totalLatency / count
		}
	}

	status := "offline"
	if len(effectiveNodes) > 0 {
		status = "active"
	} else if len(allNodes) > 0 {
		status = "degraded"
	}

	healthScore := 0.0
	if len(allNodes) > 0 {
		healthScore = float64(len(effectiveNodes)) / float64(len(allNodes))
	}

	return proxyCompatProviderInstance{
		ID:               runtimeCfg.ProviderInstanceID,
		ProviderTypeKey:  "easy-proxies",
		DisplayName:      runtimeCfg.ProviderDisplayName,
		Status:           status,
		RuntimeKind:      "external",
		ConnectorKind:    "easy-proxy-compat",
		Shared:           true,
		CostTier:         "free",
		HealthScore:      healthScore,
		AverageLatencyMs: avgLatency,
		ConnectionRef:    runtimeCfg.ConnectionRef,
		HostBindings:     []string{},
		GroupKeys:        []string{"easy-proxies"},
		Metadata: map[string]string{
			"proxyHost":         runtimeCfg.SharedHost,
			"proxyPort":         strconv.Itoa(runtimeCfg.SharedPort),
			"proxyProtocol":     runtimeCfg.SharedProtocol,
			"proxyUsername":     runtimeCfg.SharedUsername,
			"proxyPassword":     runtimeCfg.SharedPassword,
			"managementUrl":     runtimeCfg.ManagementURL,
			"managementPort":    strconv.Itoa(runtimeCfg.ManagementPort),
			"availableNodes":    strconv.Itoa(len(effectiveNodes)),
			"allNodes":          strconv.Itoa(len(allNodes)),
			"availabilityScore": strconv.Itoa(proxyCompatAverageAvailabilityScore(effectiveNodes)),
		},
		CreatedAt: runtimeCfg.CreatedAt,
		UpdatedAt: runtimeCfg.UpdatedAt,
	}
}

func (s *Server) buildProxyCompatBindings(instanceID string, leases []proxyCompatLease) []proxyCompatBinding {
	if len(leases) == 0 {
		return []proxyCompatBinding{}
	}
	byHost := make(map[string]proxyCompatBinding)
	for _, lease := range leases {
		hostID := strings.TrimSpace(lease.HostID)
		if hostID == "" {
			continue
		}
		binding := s.buildProxyCompatBinding(hostID, instanceID, "shared-instance")
		if lease.Status != "active" {
			binding.BindingMode = "released"
		}
		byHost[hostID] = binding
	}
	keys := make([]string, 0, len(byHost))
	for key := range byHost {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]proxyCompatBinding, 0, len(keys))
	for _, key := range keys {
		result = append(result, byHost[key])
	}
	return result
}

func (s *Server) buildProxyCompatBinding(hostID, instanceID, bindingMode string) proxyCompatBinding {
	mode := strings.TrimSpace(bindingMode)
	if mode == "" {
		mode = "shared-instance"
	}
	return proxyCompatBinding{
		HostID:          hostID,
		ProviderTypeKey: "easy-proxies",
		BindingMode:     mode,
		InstanceID:      instanceID,
		GroupKey:        "easy-proxies",
		UpdatedAt:       time.Now().Format(time.RFC3339),
	}
}

func proxyCompatProviderTypeDefinition() proxyCompatProviderType {
	return proxyCompatProviderType{
		Key:                         "easy-proxies",
		DisplayName:                 "EasyProxy",
		Description:                 "Local EasyProxy runtime compatibility provider.",
		SupportsDynamicProvisioning: false,
		DefaultStrategyKey:          "health-first",
		Tags:                        []string{"local-runtime", "compatibility"},
	}
}

func proxyCompatStrategyProfileDefinition() proxyCompatStrategyProfile {
	return proxyCompatStrategyProfile{
		ID:          "easyproxies-default",
		Key:         "health-first",
		DisplayName: "EasyProxy Health First",
		Description: "Prefer effective EasyProxy nodes ordered by reservations, score, active connections, and latency.",
		Metadata:    map[string]string{},
	}
}

func proxyCompatStrategyModeDefinition() *proxyCompatStrategyMode {
	return &proxyCompatStrategyMode{
		Service:                "proxy",
		ModeID:                 "easy-proxies-first",
		ProviderSelections:     []string{"easy-proxies"},
		EligibleProviderGroups: []string{"easy-proxies"},
		ProviderGroupOrder:     []string{"easy-proxies"},
		StrategyKey:            "health-first",
		Warnings:               []string{},
		Explain: []string{
			"Using EasyProxy compatibility provider.",
			"Only effective local runtime nodes are eligible.",
			"External task reports reduce availability score for failing routes.",
		},
	}
}

func ptrProxyCompatStrategyProfile(value proxyCompatStrategyProfile) *proxyCompatStrategyProfile {
	return &value
}

func (s *Server) createProxyCompatLease(
	request proxyCompatCheckoutRequest,
	runtimeCfg proxyCompatRuntime,
	candidate proxyCompatCandidate,
) (proxyCompatLease, *proxyCompatLeaseState) {
	now := time.Now()
	lease := proxyCompatLease{
		ID:                 mustGenerateCompatID("lease"),
		HostID:             strings.TrimSpace(request.HostID),
		ProviderTypeKey:    "easy-proxies",
		ProviderInstanceID: runtimeCfg.ProviderInstanceID,
		ProxyURL:           buildProxyCompatURL(candidate.Protocol, candidate.EndpointHost, candidate.EndpointPort, candidate.Username, candidate.Password),
		Host:               candidate.EndpointHost,
		Port:               candidate.EndpointPort,
		Protocol:           candidate.Protocol,
		Username:           candidate.Username,
		Password:           candidate.Password,
		Status:             "active",
		CreatedAt:          now.Format(time.RFC3339),
		Metadata: map[string]string{
			"selectedNodeTag":                  candidate.Snapshot.Tag,
			"selectedNodeName":                 candidate.Snapshot.Name,
			"selectedNodePort":                 strconv.Itoa(candidate.EndpointPort),
			"selectedNodeMode":                 candidate.EndpointMode,
			"selectedNodeRegion":               candidate.Snapshot.Region,
			"selectedNodeCountry":              candidate.Snapshot.Country,
			"selectedNodeSelectionTier":        candidate.SelectionTier,
			"selectedNodeAvailability":         strconv.FormatBool(candidate.Snapshot.Available),
			"selectedNodeBlacklisted":          strconv.FormatBool(candidate.Snapshot.Blacklisted),
			"selectedNodeAvailabilityScore":    strconv.Itoa(candidate.Snapshot.AvailabilityScore),
			"selectedNodeRecentSuccessCount":   strconv.Itoa(candidate.RecentSuccessCount),
			"selectedNodeRecentSuccessStreak":  strconv.Itoa(candidate.RecentSuccessStreak),
			"selectedNodeRecentSuccessPenalty": strconv.Itoa(candidate.RecentSuccessPenalty),
			"managementUrl":                    runtimeCfg.ManagementURL,
			"connectionRef":                    runtimeCfg.ConnectionRef,
		},
	}
	if request.TTLMinutes > 0 {
		lease.ExpiresAt = now.Add(time.Duration(request.TTLMinutes) * time.Minute).Format(time.RFC3339)
	}
	for key, value := range request.Metadata {
		trimmedKey := strings.TrimSpace(key)
		trimmedValue := strings.TrimSpace(value)
		if trimmedKey == "" || trimmedValue == "" {
			continue
		}
		lease.Metadata[trimmedKey] = trimmedValue
	}
	return lease, &proxyCompatLeaseState{
		Lease:           lease,
		SelectedNodeTag: candidate.Snapshot.Tag,
	}
}

func buildProxyCompatURL(protocol, host string, port int, username, password string) string {
	scheme := strings.TrimSpace(protocol)
	if scheme == "" {
		scheme = "http"
	}
	if username != "" || password != "" {
		return fmt.Sprintf("%s://%s:%s@%s:%d", scheme, url.QueryEscape(username), url.QueryEscape(password), host, port)
	}
	return fmt.Sprintf("%s://%s:%d", scheme, host, port)
}

func proxyUsernameForHost(base, hostID string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return base
	}
	normalized, err := profile.NormalizeDeviceID(hostID)
	if err != nil || normalized == "" {
		return base
	}
	return base + "+dev=" + normalized
}

func (s *Server) localServerCompatEnabled() bool {
	if manager := s.profileManagerSnapshot(); manager != nil {
		return manager.LocalServerEnabled()
	}
	return s.localServerConfigured()
}

func (s *Server) localServerConfigured() bool {
	s.cfgMu.RLock()
	cfg := s.cfgSrc
	s.cfgMu.RUnlock()
	if cfg == nil {
		return false
	}
	cfg.RLock()
	enabled := cfg.LocalServer.Enabled
	cfg.RUnlock()
	return enabled
}

func mustGenerateCompatID(prefix string) string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%s", prefix, hex.EncodeToString(raw))
}

func (s *Server) resolveProxyCompatRuntime(r *http.Request) proxyCompatRuntime {
	s.cfgMu.RLock()
	cfgSrc := s.cfgSrc
	s.cfgMu.RUnlock()
	runtimeCfg := s.runtimeConfig()

	listenerPort := 22323
	listenerProtocol := "http"
	listenerUsername := ""
	listenerPassword := ""
	nodeProtocol := "http"
	nodeUsername := runtimeCfg.ProxyUsername
	nodePassword := runtimeCfg.ProxyPassword
	managementPort := 29888
	mode := ""
	createdAt := time.Now().Format(time.RFC3339)

	if cfgSrc != nil {
		cfgSrc.RLock()
		mode = cfgSrc.Mode
		if cfgSrc.Listener.Port > 0 {
			listenerPort = int(cfgSrc.Listener.Port)
		}
		if strings.TrimSpace(cfgSrc.Listener.Protocol) != "" {
			listenerProtocol = strings.TrimSpace(cfgSrc.Listener.Protocol)
		}
		listenerUsername = strings.TrimSpace(cfgSrc.Listener.Username)
		listenerPassword = strings.TrimSpace(cfgSrc.Listener.Password)
		if parsedPort := parseCompatPort(cfgSrc.Management.Listen); parsedPort > 0 {
			managementPort = parsedPort
		}
		if mode == "hybrid" || mode == "multi-port" {
			if strings.TrimSpace(cfgSrc.MultiPort.Protocol) != "" {
				nodeProtocol = strings.TrimSpace(cfgSrc.MultiPort.Protocol)
			} else {
				nodeProtocol = listenerProtocol
			}
			nodeUsername = strings.TrimSpace(cfgSrc.MultiPort.Username)
			nodePassword = strings.TrimSpace(cfgSrc.MultiPort.Password)
		} else {
			nodeProtocol = listenerProtocol
			nodeUsername = listenerUsername
			nodePassword = listenerPassword
		}
		cfgSrc.RUnlock()
	}
	if s.localServerCompatEnabled() {
		credentials := s.credentialSnapshot()
		listenerUsername = credentials.Username
		listenerPassword = credentials.Password
		nodeUsername = listenerUsername
		nodePassword = listenerPassword
	} else if runtimeCfg.ProxyUsername != "" || runtimeCfg.ProxyPassword != "" {
		listenerUsername = runtimeCfg.ProxyUsername
		listenerPassword = runtimeCfg.ProxyPassword
		nodeUsername = listenerUsername
		nodePassword = listenerPassword
	}

	if nodeProtocol == "" {
		nodeProtocol = listenerProtocol
	}
	if nodeUsername == "" && nodePassword == "" {
		nodeUsername = listenerUsername
		nodePassword = listenerPassword
	}

	host := inferCompatRequestHost(r, runtimeCfg.ExternalIP)
	scheme := inferCompatRequestScheme(r)
	refScheme := "easy-proxy"
	if scheme == "https" {
		refScheme = "easy-proxy-ssl"
	}
	return proxyCompatRuntime{
		SharedHost:              host,
		SharedPort:              listenerPort,
		SharedProtocol:          listenerProtocol,
		SharedUsername:          listenerUsername,
		SharedPassword:          listenerPassword,
		AllowSharedPoolFallback: mode != "hybrid" && mode != "multi-port",
		NodeProtocol:            nodeProtocol,
		NodeUsername:            nodeUsername,
		NodePassword:            nodePassword,
		ManagementPort:          managementPort,
		ConnectionRef:           fmt.Sprintf("%s://%s:%d", refScheme, host, managementPort),
		ManagementURL:           fmt.Sprintf("%s://%s:%d", scheme, host, managementPort),
		ProviderInstanceID:      "easyproxy-default",
		ProviderDisplayName:     "EasyProxy Default Instance",
		CreatedAt:               createdAt,
		UpdatedAt:               time.Now().Format(time.RFC3339),
	}
}
