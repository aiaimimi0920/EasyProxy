package monitor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/profile"
)

var (
	errLocalServerValidation = errors.New("invalid local server resource")
	errLocalServerNotFound   = errors.New("local server resource not found")
)

type configMutationGuard interface {
	BeginConfigMutation(context.Context) (func(), error)
}

type mutationEnvelope struct {
	Revision         int64  `json:"revision"`
	RegistryRevision uint64 `json:"registry_revision"`
	NeedReload       bool   `json:"need_reload"`
	ProfileScope     string `json:"profile_scope,omitempty"`
	Resource         any    `json:"resource,omitempty"`
	Message          string `json:"message,omitempty"`
}

type apiError struct {
	Error           string `json:"error"`
	CurrentRevision int64  `json:"current_revision,omitempty"`
	NeedReload      bool   `json:"need_reload,omitempty"`
}

type localServerStatusResponse struct {
	Enabled               bool   `json:"enabled"`
	Listen                string `json:"listen"`
	DispatcherReady       bool   `json:"dispatcher_ready"`
	RegistryRevision      uint64 `json:"registry_revision"`
	CredentialGeneration  uint64 `json:"credential_generation"`
	ProfileCount          int    `json:"profile_count"`
	MappingCount          int    `json:"mapping_count"`
	ProviderDegradedCount int    `json:"provider_degraded_count"`
	PeerAddressMode       string `json:"peer_address_mode"`
	SourceIPWarning       string `json:"source_ip_warning"`
}

type profileResourceResponse struct {
	ProfileScope     string                 `json:"profile_scope"`
	DeviceID         string                 `json:"device_id,omitempty"`
	Revision         int64                  `json:"revision"`
	RegistryRevision uint64                 `json:"registry_revision"`
	NeedReload       bool                   `json:"need_reload"`
	Profile          profile.Definition     `json:"profile"`
	ProviderStatus   profile.ProviderStatus `json:"provider_status"`
}

type deviceSummaryResponse struct {
	DeviceID         string     `json:"device_id"`
	DisplayName      string     `json:"display_name"`
	Revision         int64      `json:"revision"`
	ProfileMode      string     `json:"profile_mode"`
	ProfileRevision  int64      `json:"profile_revision,omitempty"`
	EffectiveEnabled bool       `json:"effective_enabled"`
	EffectiveState   string     `json:"effective_state"`
	IdentitySource   string     `json:"identity_source,omitempty"`
	LastSeenIP       string     `json:"last_seen_ip,omitempty"`
	LastSeenAt       *time.Time `json:"last_seen_at,omitempty"`
	MappingCount     int        `json:"mapping_count"`
}

type deviceResourceResponse struct {
	deviceSummaryResponse
	Profile  *profileResourceResponse `json:"profile,omitempty"`
	Mappings []ipMappingResponse      `json:"mappings"`
}

type ipMappingResponse struct {
	MappingID string `json:"mapping_id"`
	CIDR      string `json:"cidr"`
	DeviceID  string `json:"device_id"`
	Priority  int    `json:"priority"`
	Enabled   bool   `json:"enabled"`
	Revision  int64  `json:"revision"`
}

type profileMutationRequest struct {
	ExpectedRevision *int64              `json:"expected_revision,omitempty"`
	Profile          *profile.Definition `json:"profile"`
}

type expectedRevisionRequest struct {
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
}

type enabledMutationRequest struct {
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
	Enabled          *bool  `json:"enabled"`
}

type deviceMutationRequest struct {
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
	DisplayName      string `json:"display_name"`
}

type mappingMutationRequest struct {
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
	CIDR             string `json:"cidr"`
	DeviceID         string `json:"device_id"`
	Priority         int    `json:"priority"`
	Enabled          bool   `json:"enabled"`
}

type routingConfigAliasResponse struct {
	routingConfigPayload
	ProfileScope string `json:"profile_scope"`
	Revision     int64  `json:"revision"`
}

type routingConfigAliasRequest struct {
	routingConfigPayload
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
	Revision         *int64 `json:"revision,omitempty"`
}

func (s *Server) registerLocalServerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/local-server/status", s.withAuth(s.handleLocalServerStatus))
	mux.HandleFunc("/api/local-server/config", s.withAuth(s.handleLocalServerConfig))
	mux.HandleFunc("/api/local-server/profiles/shared", s.withAuth(s.handleSharedProfile))
	mux.HandleFunc("/api/local-server/devices", s.withAuth(s.handleDevices))
	mux.HandleFunc("/api/local-server/devices/", s.withAuth(s.handleDeviceResource))
	mux.HandleFunc("/api/local-server/ip-mappings", s.withAuth(s.handleIPMappings))
	mux.HandleFunc("/api/local-server/ip-mappings/", s.withAuth(s.handleIPMappingResource))
}

func (s *Server) beginConfigMutation(ctx context.Context) (func(), error) {
	manager := s.nodeManagerSnapshot()
	guard, ok := manager.(configMutationGuard)
	if !ok {
		return func() {}, nil
	}
	return guard.BeginConfigMutation(ctx)
}

func (s *Server) beginLocalServerMutation(ctx context.Context) (func(), error) {
	releaseBarrier, err := s.beginConfigMutation(ctx)
	if err != nil {
		return nil, err
	}
	s.configUpdateMu.Lock()
	if s.reloadWindowCount > 0 {
		s.configUpdateMu.Unlock()
		releaseBarrier()
		return nil, errReloadInProgress
	}
	return func() {
		s.configUpdateMu.Unlock()
		releaseBarrier()
	}, nil
}

func (s *Server) handleLocalServerStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, apiError{Error: "profile_manager_unavailable"})
		return
	}
	status := profiles.RuntimeStatus()
	credentials := profiles.Credentials()
	routingStatus := RoutingStatus{}
	if routing := s.routingSnapshot(); routing != nil {
		routingStatus = routing.RoutingStatus()
	}
	listen := routingStatus.Listen
	if listen == "" {
		s.cfgMu.RLock()
		cfg := s.cfgSrc
		s.cfgMu.RUnlock()
		if cfg != nil {
			cfg.RLock()
			listen = cfg.DispatchListen()
			cfg.RUnlock()
		}
	}
	writeJSON(w, localServerStatusResponse{
		Enabled:               profiles.LocalServerEnabled(),
		Listen:                listen,
		DispatcherReady:       routingStatus.DispatcherReady,
		RegistryRevision:      status.RegistryRevision,
		CredentialGeneration:  credentials.Generation,
		ProfileCount:          status.ProfileCount,
		MappingCount:          status.MappingCount,
		ProviderDegradedCount: status.ProviderDegradedCount,
		PeerAddressMode:       "tcp_peer",
		SourceIPWarning:       "Docker/NAT may collapse multiple clients to the same observed source IP; prefer explicit device_id when identity must be stable.",
	})
}

func (s *Server) localServerRoutingStatus(manager *profile.Manager) RoutingStatus {
	shared := manager.SharedProfile()
	routingStatus := RoutingStatus{}
	if routing := s.routingSnapshot(); routing != nil {
		routingStatus = routing.RoutingStatus()
	}
	if shared == nil {
		return routingStatus
	}
	definition := shared.Definition()
	selection := shared.Selection()
	routingStatus.Enabled = definition.Enabled
	routingStatus.SharedEnabled = definition.Enabled
	routingStatus.SharedRevision = shared.Revision()
	routingStatus.ProfileScope = "shared"
	routingStatus.DefaultStrategy = selection.DefaultStrategy
	routingStatus.FinalPolicy = definition.FinalPolicy
	routingStatus.RuleCount = shared.RuleCount()
	if routingStatus.Listen == "" {
		s.cfgMu.RLock()
		cfg := s.cfgSrc
		s.cfgMu.RUnlock()
		if cfg != nil {
			cfg.RLock()
			routingStatus.Listen = cfg.DispatchListen()
			cfg.RUnlock()
		}
	}
	return routingStatus
}

func (s *Server) handleLocalServerRoutingConfig(w http.ResponseWriter, r *http.Request, manager *profile.Manager) {
	switch r.Method {
	case http.MethodGet:
		shared := manager.SharedProfile()
		if shared == nil {
			s.writeLocalServerError(w, errLocalServerNotFound)
			return
		}
		payload, err := s.routingPayloadFromDefinition(shared.Definition())
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		writeJSON(w, routingConfigAliasResponse{routingConfigPayload: payload, ProfileScope: "shared", Revision: shared.Revision()})
	case http.MethodPut:
		var req routingConfigAliasRequest
		if err := decodeJSONBody(r, &req); err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		definition, err := s.routingDefinitionFromPayload(req.routingConfigPayload)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		shared := manager.SharedProfile()
		if shared == nil {
			s.writeLocalServerError(w, errLocalServerNotFound)
			return
		}
		definition.NodeFilter = shared.Definition().NodeFilter
		bodyRevision := req.ExpectedRevision
		if req.Revision != nil {
			if bodyRevision != nil && *bodyRevision != *req.Revision {
				s.writeLocalServerError(w, fmt.Errorf("%w: revision and expected_revision contradict", errLocalServerValidation))
				return
			}
			bodyRevision = req.Revision
		}
		expected := shared.Revision()
		if bodyRevision != nil || r.Header.Get("If-Match") != "" || r.Header.Get("If-None-Match") != "" {
			expected, err = expectedRevision(r, bodyRevision)
			if err != nil {
				s.writeLocalServerError(w, err)
				return
			}
		}
		result, err := s.updateSharedProfile(r.Context(), definition, expected)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		response := profileMutationEnvelope(manager, result, "shared", "")
		response.Message = "shared profile saved"
		writeJSON(w, response)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) routingPayloadFromDefinition(definition profile.Definition) (routingConfigPayload, error) {
	var routing config.RoutingConfig
	if err := profile.ApplyDefinitionToRouting(definition, &routing); err != nil {
		return routingConfigPayload{}, fmt.Errorf("%w: %v", errLocalServerValidation, err)
	}
	s.cfgMu.RLock()
	cfg := s.cfgSrc
	s.cfgMu.RUnlock()
	if cfg != nil {
		cfg.RLock()
		routing.Listen = cfg.Routing.Listen
		cfg.RUnlock()
	}
	return routingPayloadFromRouting(routing), nil
}

func routingPayloadFromRouting(routing config.RoutingConfig) routingConfigPayload {
	providers := make([]routingProviderConfig, 0, len(routing.RuleProviders))
	for _, provider := range routing.RuleProviders {
		providers = append(providers, routingProviderConfig{
			URL:      provider.URL,
			Policy:   provider.Policy,
			Behavior: provider.Behavior,
			Interval: provider.Interval.String(),
		})
	}
	useDefaults := true
	if routing.UseDefaultRules != nil {
		useDefaults = *routing.UseDefaultRules
	}
	return routingConfigPayload{
		Enabled:            routing.Enabled,
		Listen:             routing.Listen,
		DefaultStrategy:    routing.DefaultStrategy,
		UseDefaultRules:    useDefaults,
		FinalPolicy:        routing.FinalPolicy,
		Rules:              append([]string(nil), routing.Rules...),
		RuleProviders:      providers,
		LongLivedMinUptime: routing.LongLived.MinUptime.String(),
		LongLivedMinRate:   routing.LongLived.MinSuccessRate,
		SessionTTL:         routing.Session.TTL.String(),
	}
}

func (s *Server) routingDefinitionFromPayload(req routingConfigPayload) (profile.Definition, error) {
	var longLivedUptime, sessionTTL time.Duration
	var err error
	if strings.TrimSpace(req.LongLivedMinUptime) != "" {
		longLivedUptime, err = time.ParseDuration(strings.TrimSpace(req.LongLivedMinUptime))
		if err != nil || longLivedUptime < 0 {
			return profile.Definition{}, fmt.Errorf("%w: invalid long-lived uptime", errLocalServerValidation)
		}
	}
	if strings.TrimSpace(req.SessionTTL) != "" {
		sessionTTL, err = time.ParseDuration(strings.TrimSpace(req.SessionTTL))
		if err != nil || sessionTTL < 0 {
			return profile.Definition{}, fmt.Errorf("%w: invalid session TTL", errLocalServerValidation)
		}
	}
	if req.LongLivedMinRate < 0 || req.LongLivedMinRate > 1 {
		return profile.Definition{}, fmt.Errorf("%w: invalid long-lived success rate", errLocalServerValidation)
	}
	providers := make([]config.RuleProvider, 0, len(req.RuleProviders))
	for _, provider := range req.RuleProviders {
		if strings.TrimSpace(provider.URL) == "" {
			continue
		}
		interval := time.Duration(0)
		if strings.TrimSpace(provider.Interval) != "" {
			interval, err = time.ParseDuration(strings.TrimSpace(provider.Interval))
			if err != nil || interval < 0 {
				return profile.Definition{}, fmt.Errorf("%w: invalid provider interval", errLocalServerValidation)
			}
		}
		providers = append(providers, config.RuleProvider{
			URL:      strings.TrimSpace(provider.URL),
			Policy:   strings.TrimSpace(provider.Policy),
			Behavior: strings.TrimSpace(provider.Behavior),
			Interval: interval,
		})
	}
	if err := config.ValidateRuleProviders(providers); err != nil {
		return profile.Definition{}, fmt.Errorf("%w: %v", errLocalServerValidation, err)
	}
	routing := config.RoutingConfig{
		Enabled:         req.Enabled,
		DefaultStrategy: strings.TrimSpace(req.DefaultStrategy),
		FinalPolicy:     strings.TrimSpace(req.FinalPolicy),
		Rules:           append([]string(nil), req.Rules...),
		RuleProviders:   providers,
		LongLived:       config.LongLivedConfig{MinUptime: longLivedUptime, MinSuccessRate: req.LongLivedMinRate},
		Session:         config.SessionConfig{TTL: sessionTTL},
	}
	useDefaults := req.UseDefaultRules
	routing.UseDefaultRules = &useDefaults
	return profile.DefinitionFromRouting(routing), nil
}
