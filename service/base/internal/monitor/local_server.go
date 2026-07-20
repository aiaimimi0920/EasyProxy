package monitor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/profile"
	"easy_proxies/internal/store"
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

func (s *Server) handleSharedProfile(w http.ResponseWriter, r *http.Request) {
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, apiError{Error: "profile_manager_unavailable"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		shared := profiles.SharedProfile()
		if shared == nil {
			s.writeLocalServerError(w, errLocalServerNotFound)
			return
		}
		writeJSON(w, profileResponse(profiles, shared, "shared", ""))
	case http.MethodPut:
		var req profileMutationRequest
		if err := decodeJSONBody(r, &req); err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		if req.Profile == nil {
			s.writeLocalServerError(w, fmt.Errorf("%w: profile is required", errLocalServerValidation))
			return
		}
		expected, err := expectedRevision(r, req.ExpectedRevision)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		result, err := s.updateSharedProfile(r.Context(), *req.Profile, expected)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		writeJSON(w, profileMutationEnvelope(profiles, result, "shared", ""))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) updateSharedProfile(ctx context.Context, definition profile.Definition, expected int64) (profile.MutationResult, error) {
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		return profile.MutationResult{}, errors.New("profile manager is unavailable")
	}
	prepared, err := profiles.PrepareShared(definition, expected)
	if err != nil {
		return profile.MutationResult{}, fmt.Errorf("%w: %v", errLocalServerValidation, err)
	}
	release, err := s.beginLocalServerMutation(ctx)
	if err != nil {
		return profile.MutationResult{}, err
	}
	defer release()
	if err := profiles.ReserveShared(prepared); err != nil {
		return profile.MutationResult{}, err
	}
	defer profiles.AbortShared(prepared)

	s.cfgMu.RLock()
	cfg := s.cfgSrc
	s.cfgMu.RUnlock()
	if cfg == nil {
		return profile.MutationResult{}, errors.New("config storage is not initialized")
	}
	cfg.Lock()
	currentRevision := cfg.LocalServer.SharedRevision
	if currentRevision <= 0 {
		currentRevision = 1
	}
	if currentRevision != expected {
		cfg.Unlock()
		return profile.MutationResult{}, &store.RevisionConflictError{CurrentRevision: currentRevision}
	}
	candidate := cfg.Clone()
	if err := profile.ApplyDefinitionToRouting(prepared.Definition, &candidate.Routing); err != nil {
		cfg.Unlock()
		return profile.MutationResult{}, fmt.Errorf("%w: %v", errLocalServerValidation, err)
	}
	candidate.LocalServer.SharedRevision = currentRevision + 1
	candidate.Lock()
	err = candidate.SaveSettings()
	candidate.Unlock()
	if err != nil {
		cfg.Unlock()
		return profile.MutationResult{}, fmt.Errorf("save shared profile: %w", err)
	}
	cfg.Routing = candidate.Routing
	cfg.LocalServer.SharedRevision = candidate.LocalServer.SharedRevision
	cfg.Unlock()

	result := profiles.PublishShared(prepared)
	if result.Revision != candidate.LocalServer.SharedRevision {
		return profile.MutationResult{}, errors.New("shared profile publication lost its prepared revision")
	}
	if routing := s.routingSnapshot(); routing != nil {
		_ = routing.ApplyHot(candidate)
	}
	return result, nil
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
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
	devices, err := profiles.ListDevices(r.Context())
	if err != nil {
		s.writeLocalServerError(w, err)
		return
	}
	mappings, err := profiles.ListIPMappings(r.Context())
	if err != nil {
		s.writeLocalServerError(w, err)
		return
	}
	activity := profiles.ActivitySnapshot()
	responses := make([]deviceSummaryResponse, 0, len(devices)+len(activity))
	persisted := make(map[string]struct{}, len(devices))
	for _, device := range devices {
		persisted[device.DeviceID] = struct{}{}
		responses = append(responses, buildDeviceSummary(profiles, device, mappings, activity))
	}
	activityOnly := make([]string, 0)
	for deviceID := range activity {
		if _, ok := persisted[deviceID]; !ok {
			activityOnly = append(activityOnly, deviceID)
		}
	}
	sort.Strings(activityOnly)
	for _, deviceID := range activityOnly {
		responses = append(responses, buildDeviceSummary(profiles, store.Device{
			DeviceID:    deviceID,
			DisplayName: deviceID,
		}, mappings, activity))
	}
	writeJSON(w, map[string]any{"devices": responses})
}

func (s *Server) handleDeviceResource(w http.ResponseWriter, r *http.Request) {
	deviceID, action, err := parseDeviceResourcePath(r)
	if err != nil {
		s.writeLocalServerError(w, err)
		return
	}
	switch action {
	case "":
		s.handleDeviceItem(w, r, deviceID)
	case "profile":
		s.handleDeviceProfile(w, r, deviceID)
	case "profile/enabled":
		s.handleDeviceProfileEnabled(w, r, deviceID)
	case "profile/copy-shared":
		s.handleDeviceProfileCopyShared(w, r, deviceID)
	default:
		s.writeLocalServerError(w, errLocalServerNotFound)
	}
}

func (s *Server) handleDeviceItem(w http.ResponseWriter, r *http.Request, deviceID string) {
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, apiError{Error: "profile_manager_unavailable"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		device, err := profiles.GetDevice(r.Context(), deviceID)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		activity := profiles.ActivitySnapshot()
		if device == nil {
			if _, ok := activity[deviceID]; !ok {
				s.writeLocalServerError(w, errLocalServerNotFound)
				return
			}
			device = &store.Device{DeviceID: deviceID, DisplayName: deviceID}
		}
		mappings, err := profiles.ListIPMappings(r.Context())
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		writeJSON(w, buildDeviceResource(profiles, *device, mappings, activity))
	case http.MethodPut:
		var req deviceMutationRequest
		if err := decodeJSONBody(r, &req); err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		expected, err := expectedRevision(r, req.ExpectedRevision)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		release, err := s.beginLocalServerMutation(r.Context())
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		mappings, err := profiles.ListIPMappings(r.Context())
		if err != nil {
			release()
			s.writeLocalServerError(w, err)
			return
		}
		device, err := profiles.PutDevice(r.Context(), deviceID, strings.TrimSpace(req.DisplayName), expected)
		release()
		if err != nil {
			s.writeLocalServerError(w, classifyLocalServerValidation(err))
			return
		}
		writeJSON(w, mutationEnvelope{
			Revision:         device.Revision,
			RegistryRevision: profiles.RuntimeStatus().RegistryRevision,
			NeedReload:       false,
			Resource:         buildDeviceResource(profiles, device, mappings, profiles.ActivitySnapshot()),
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleDeviceProfile(w http.ResponseWriter, r *http.Request, deviceID string) {
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, apiError{Error: "profile_manager_unavailable"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		selected := profiles.DeviceProfile(deviceID)
		scope := "device"
		if selected == nil {
			selected = profiles.SharedProfile()
			scope = "shared"
		}
		if selected == nil {
			s.writeLocalServerError(w, errLocalServerNotFound)
			return
		}
		writeJSON(w, profileResponse(profiles, selected, scope, deviceID))
	case http.MethodPut:
		var req profileMutationRequest
		if err := decodeJSONBody(r, &req); err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		if req.Profile == nil {
			s.writeLocalServerError(w, fmt.Errorf("%w: profile is required", errLocalServerValidation))
			return
		}
		expected, err := expectedRevision(r, req.ExpectedRevision)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		release, err := s.beginLocalServerMutation(r.Context())
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		result, err := profiles.PutDeviceProfile(r.Context(), deviceID, *req.Profile, expected)
		release()
		if err != nil {
			s.writeLocalServerError(w, classifyLocalServerValidation(err))
			return
		}
		writeJSON(w, profileMutationEnvelope(profiles, result, "device", deviceID))
	case http.MethodDelete:
		expected, err := expectedRevisionFromOptionalBody(r)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		release, err := s.beginLocalServerMutation(r.Context())
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		result, err := profiles.DeleteDeviceProfile(r.Context(), deviceID, expected)
		release()
		if err != nil {
			s.writeLocalServerError(w, classifyLocalServerValidation(err))
			return
		}
		shared := profiles.SharedProfile()
		if shared == nil {
			s.writeLocalServerError(w, errLocalServerNotFound)
			return
		}
		writeJSON(w, mutationEnvelope{
			Revision:         shared.Revision(),
			RegistryRevision: result.RegistryRevision,
			NeedReload:       false,
			ProfileScope:     "shared",
			Resource:         profileResponse(profiles, shared, "shared", ""),
			Message:          "device profile deleted",
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleDeviceProfileEnabled(w http.ResponseWriter, r *http.Request, deviceID string) {
	if r.Method != http.MethodPatch {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, apiError{Error: "profile_manager_unavailable"})
		return
	}
	var req enabledMutationRequest
	if err := decodeJSONBody(r, &req); err != nil {
		s.writeLocalServerError(w, err)
		return
	}
	if req.Enabled == nil {
		s.writeLocalServerError(w, fmt.Errorf("%w: enabled is required", errLocalServerValidation))
		return
	}
	expected, err := expectedRevision(r, req.ExpectedRevision)
	if err != nil {
		s.writeLocalServerError(w, err)
		return
	}
	release, err := s.beginLocalServerMutation(r.Context())
	if err != nil {
		s.writeLocalServerError(w, err)
		return
	}
	result, err := profiles.SetDeviceProfileEnabled(r.Context(), deviceID, *req.Enabled, expected)
	release()
	if err != nil {
		if errors.Is(err, profile.ErrDeviceProfileNotFound) {
			err = errLocalServerNotFound
		}
		s.writeLocalServerError(w, classifyLocalServerValidation(err))
		return
	}
	writeJSON(w, profileMutationEnvelope(profiles, result, "device", deviceID))
}

func (s *Server) handleDeviceProfileCopyShared(w http.ResponseWriter, r *http.Request, deviceID string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, apiError{Error: "profile_manager_unavailable"})
		return
	}
	if current := profiles.DeviceProfile(deviceID); current != nil {
		s.writeLocalServerError(w, &store.RevisionConflictError{CurrentRevision: current.Revision()})
		return
	}
	release, err := s.beginLocalServerMutation(r.Context())
	if err != nil {
		s.writeLocalServerError(w, err)
		return
	}
	result, err := profiles.CopySharedProfile(r.Context(), deviceID)
	release()
	if err != nil {
		s.writeLocalServerError(w, classifyLocalServerValidation(err))
		return
	}
	writeJSON(w, profileMutationEnvelope(profiles, result, "device", deviceID))
}

func (s *Server) handleIPMappings(w http.ResponseWriter, r *http.Request) {
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, apiError{Error: "profile_manager_unavailable"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		mappings, err := profiles.ListIPMappings(r.Context())
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		writeJSON(w, map[string]any{"mappings": ipMappingResponses(mappings)})
	case http.MethodPost:
		var req mappingMutationRequest
		if err := decodeJSONBody(r, &req); err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		expected, err := expectedRevision(r, req.ExpectedRevision)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		if expected != 0 {
			s.writeLocalServerError(w, fmt.Errorf("%w: mapping create requires expected_revision 0", errLocalServerValidation))
			return
		}
		cidr, err := normalizeMappingCIDR(req.CIDR)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		mappingID, err := newMappingID()
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		release, err := s.beginLocalServerMutation(r.Context())
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		mapping, registryRevision, err := profiles.PutIPMapping(r.Context(), store.DeviceIPMapping{
			MappingID: mappingID,
			CIDR:      cidr,
			DeviceID:  req.DeviceID,
			Priority:  req.Priority,
			Enabled:   req.Enabled,
		}, 0)
		release()
		if err != nil {
			s.writeLocalServerError(w, classifyLocalServerValidation(err))
			return
		}
		writeJSON(w, mutationEnvelope{
			Revision:         mapping.Revision,
			RegistryRevision: registryRevision,
			NeedReload:       false,
			Resource:         ipMappingResponseFromStore(mapping),
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleIPMappingResource(w http.ResponseWriter, r *http.Request) {
	mappingID, err := parseMappingResourcePath(r)
	if err != nil {
		s.writeLocalServerError(w, err)
		return
	}
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, apiError{Error: "profile_manager_unavailable"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		var req mappingMutationRequest
		if err := decodeJSONBody(r, &req); err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		expected, err := expectedRevision(r, req.ExpectedRevision)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		cidr, err := normalizeMappingCIDR(req.CIDR)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		release, err := s.beginLocalServerMutation(r.Context())
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		mapping, registryRevision, err := profiles.PutIPMapping(r.Context(), store.DeviceIPMapping{
			MappingID: mappingID,
			CIDR:      cidr,
			DeviceID:  req.DeviceID,
			Priority:  req.Priority,
			Enabled:   req.Enabled,
		}, expected)
		release()
		if err != nil {
			s.writeLocalServerError(w, classifyLocalServerValidation(err))
			return
		}
		writeJSON(w, mutationEnvelope{
			Revision:         mapping.Revision,
			RegistryRevision: registryRevision,
			NeedReload:       false,
			Resource:         ipMappingResponseFromStore(mapping),
		})
	case http.MethodDelete:
		expected, err := expectedRevisionFromOptionalBody(r)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		release, err := s.beginLocalServerMutation(r.Context())
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		current, err := profiles.GetIPMapping(r.Context(), mappingID)
		if err != nil {
			release()
			s.writeLocalServerError(w, err)
			return
		}
		registryRevision, err := profiles.DeleteIPMapping(r.Context(), mappingID, expected)
		release()
		if err != nil {
			s.writeLocalServerError(w, classifyLocalServerValidation(err))
			return
		}
		revision := expected
		var resource any
		if current != nil {
			revision = current.Revision
			resource = ipMappingResponseFromStore(*current)
		}
		writeJSON(w, mutationEnvelope{
			Revision:         revision,
			RegistryRevision: registryRevision,
			NeedReload:       false,
			Resource:         resource,
			Message:          "mapping deleted",
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func profileResponse(manager *profile.Manager, compiled *profile.CompiledProfile, scope, deviceID string) profileResourceResponse {
	status := manager.RuntimeStatus()
	profileID := "shared"
	if scope == "device" {
		profileID = deviceID
	}
	return profileResourceResponse{
		ProfileScope:     scope,
		DeviceID:         deviceID,
		Revision:         compiled.Revision(),
		RegistryRevision: status.RegistryRevision,
		NeedReload:       false,
		Profile:          compiled.Definition(),
		ProviderStatus:   manager.ProviderStatus(profileID),
	}
}

func profileMutationResponse(manager *profile.Manager, result profile.MutationResult, scope, deviceID string) profileResourceResponse {
	compiled := result.Profile
	if compiled == nil {
		if scope == "device" {
			compiled = manager.DeviceProfile(deviceID)
		} else {
			compiled = manager.SharedProfile()
		}
	}
	if compiled == nil {
		return profileResourceResponse{ProfileScope: scope, DeviceID: deviceID, RegistryRevision: result.RegistryRevision}
	}
	response := profileResponse(manager, compiled, scope, deviceID)
	response.RegistryRevision = result.RegistryRevision
	return response
}

func profileMutationEnvelope(manager *profile.Manager, result profile.MutationResult, scope, deviceID string) mutationEnvelope {
	return mutationEnvelope{
		Revision:         result.Revision,
		RegistryRevision: result.RegistryRevision,
		NeedReload:       false,
		ProfileScope:     scope,
		Resource:         profileMutationResponse(manager, result, scope, deviceID),
	}
}

func buildDeviceSummary(manager *profile.Manager, device store.Device, mappings []store.DeviceIPMapping, activity map[string]profile.DeviceActivity) deviceSummaryResponse {
	compiled := manager.DeviceProfile(device.DeviceID)
	mode := "independent"
	if compiled == nil {
		compiled = manager.SharedProfile()
		mode = "shared"
	}
	response := deviceSummaryResponse{
		DeviceID:        device.DeviceID,
		DisplayName:     device.DisplayName,
		Revision:        device.Revision,
		ProfileMode:     mode,
		EffectiveState:  "DIRECT",
		ProfileRevision: 0,
	}
	if compiled != nil {
		response.ProfileRevision = compiled.Revision()
		response.EffectiveEnabled = compiled.Enabled()
		if compiled.Enabled() {
			response.EffectiveState = "PROFILE"
		}
	}
	for _, mapping := range mappings {
		if mapping.DeviceID == device.DeviceID {
			response.MappingCount++
		}
	}
	if observed, ok := activity[device.DeviceID]; ok {
		response.IdentitySource = string(observed.Source)
		if observed.LastSeenIP.IsValid() {
			response.LastSeenIP = observed.LastSeenIP.String()
		}
		if !observed.LastSeenAt.IsZero() {
			seenAt := observed.LastSeenAt
			response.LastSeenAt = &seenAt
		}
	}
	return response
}

func buildDeviceResource(manager *profile.Manager, device store.Device, mappings []store.DeviceIPMapping, activity map[string]profile.DeviceActivity) deviceResourceResponse {
	response := deviceResourceResponse{
		deviceSummaryResponse: buildDeviceSummary(manager, device, mappings, activity),
		Mappings:              make([]ipMappingResponse, 0),
	}
	selected := manager.DeviceProfile(device.DeviceID)
	scope := "device"
	if selected == nil {
		selected = manager.SharedProfile()
		scope = "shared"
	}
	if selected != nil {
		profileResource := profileResponse(manager, selected, scope, device.DeviceID)
		if scope == "shared" {
			profileResource.DeviceID = ""
		}
		response.Profile = &profileResource
	}
	for _, mapping := range mappings {
		if mapping.DeviceID == device.DeviceID {
			response.Mappings = append(response.Mappings, ipMappingResponseFromStore(mapping))
		}
	}
	return response
}

func ipMappingResponses(mappings []store.DeviceIPMapping) []ipMappingResponse {
	responses := make([]ipMappingResponse, 0, len(mappings))
	for _, mapping := range mappings {
		responses = append(responses, ipMappingResponseFromStore(mapping))
	}
	return responses
}

func ipMappingResponseFromStore(mapping store.DeviceIPMapping) ipMappingResponse {
	return ipMappingResponse{
		MappingID: mapping.MappingID,
		CIDR:      mapping.CIDR,
		DeviceID:  mapping.DeviceID,
		Priority:  mapping.Priority,
		Enabled:   mapping.Enabled,
		Revision:  mapping.Revision,
	}
}

func parseDeviceResourcePath(r *http.Request) (string, string, error) {
	const prefix = "/api/local-server/devices/"
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), prefix)
	parts := strings.Split(strings.Trim(escaped, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "", "", errLocalServerNotFound
	}
	rawID, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", "", fmt.Errorf("%w: invalid escaped device_id", errLocalServerValidation)
	}
	deviceID, err := profile.NormalizeDeviceID(rawID)
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", errLocalServerValidation, err)
	}
	action := strings.Join(parts[1:], "/")
	return deviceID, action, nil
}

func parseMappingResourcePath(r *http.Request) (string, error) {
	const prefix = "/api/local-server/ip-mappings/"
	escaped := strings.Trim(strings.TrimPrefix(r.URL.EscapedPath(), prefix), "/")
	if escaped == "" || strings.Contains(escaped, "/") {
		return "", errLocalServerNotFound
	}
	mappingID, err := url.PathUnescape(escaped)
	if err != nil || strings.TrimSpace(mappingID) == "" {
		return "", fmt.Errorf("%w: invalid mapping_id", errLocalServerValidation)
	}
	return strings.TrimSpace(mappingID), nil
}

func decodeJSONBody(r *http.Request, target any) error {
	if r == nil || r.Body == nil {
		return fmt.Errorf("%w: request body is required", errLocalServerValidation)
	}
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		return fmt.Errorf("%w: invalid request body: %v", errLocalServerValidation, err)
	}
	return nil
}

func expectedRevision(r *http.Request, bodyRevision *int64) (int64, error) {
	if bodyRevision != nil && *bodyRevision < 0 {
		return 0, fmt.Errorf("%w: expected_revision must be non-negative", errLocalServerValidation)
	}
	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
	ifNoneMatch := strings.TrimSpace(r.Header.Get("If-None-Match"))
	if ifMatch != "" && ifNoneMatch != "" {
		return 0, fmt.Errorf("%w: If-Match and If-None-Match are mutually exclusive", errLocalServerValidation)
	}
	headerSet := false
	headerRevision := int64(0)
	switch {
	case ifNoneMatch != "":
		if ifNoneMatch != "*" {
			return 0, fmt.Errorf("%w: If-None-Match must be *", errLocalServerValidation)
		}
		headerSet = true
	case ifMatch != "":
		trimmed := strings.Trim(ifMatch, `"`)
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil || parsed < 0 {
			return 0, fmt.Errorf("%w: invalid If-Match revision", errLocalServerValidation)
		}
		headerSet = true
		headerRevision = parsed
	}
	if headerSet {
		if bodyRevision != nil && *bodyRevision != headerRevision {
			return 0, fmt.Errorf("%w: header and body revisions contradict", errLocalServerValidation)
		}
		return headerRevision, nil
	}
	if bodyRevision != nil {
		return *bodyRevision, nil
	}
	return 0, nil
}

func expectedRevisionFromOptionalBody(r *http.Request) (int64, error) {
	var bodyRevision *int64
	if r != nil && r.Body != nil && r.ContentLength != 0 {
		var body expectedRevisionRequest
		if err := decodeJSONBody(r, &body); err != nil {
			return 0, err
		}
		bodyRevision = body.ExpectedRevision
	}
	return expectedRevision(r, bodyRevision)
}

func normalizeMappingCIDR(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if prefix, err := netip.ParsePrefix(trimmed); err == nil {
		return prefix.Masked().String(), nil
	}
	addr, err := netip.ParseAddr(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: invalid IP or CIDR %q", errLocalServerValidation, value)
	}
	bits := 128
	if addr.Is4() {
		bits = 32
	}
	return netip.PrefixFrom(addr, bits).String(), nil
}

func newMappingID() (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate mapping id: %w", err)
	}
	return "map-" + hex.EncodeToString(bytes), nil
}

func classifyLocalServerValidation(err error) error {
	if err == nil {
		return nil
	}
	var conflict *store.RevisionConflictError
	if errors.As(err, &conflict) || errors.Is(err, store.ErrDeviceNotFound) || errors.Is(err, errLocalServerNotFound) {
		return err
	}
	if errors.Is(err, profile.ErrInvalidDefinition) || errors.Is(err, profile.ErrInvalidDeviceID) {
		return fmt.Errorf("%w: %v", errLocalServerValidation, err)
	}
	return err
}

func (s *Server) writeLocalServerError(w http.ResponseWriter, err error) {
	response := apiError{Error: "internal_error"}
	status := http.StatusInternalServerError
	var conflict *store.RevisionConflictError
	switch {
	case errors.Is(err, errReloadInProgress):
		status = http.StatusConflict
		response.Error = "reload_in_progress"
		response.NeedReload = true
	case errors.As(err, &conflict):
		status = http.StatusConflict
		response.Error = "revision_conflict"
		response.CurrentRevision = conflict.CurrentRevision
	case errors.Is(err, errLocalServerNotFound), errors.Is(err, store.ErrDeviceNotFound):
		status = http.StatusNotFound
		response.Error = "not_found"
	case errors.Is(err, errLocalServerValidation):
		status = http.StatusUnprocessableEntity
		response.Error = err.Error()
	default:
		response.Error = err.Error()
	}
	w.WriteHeader(status)
	writeJSON(w, response)
}
