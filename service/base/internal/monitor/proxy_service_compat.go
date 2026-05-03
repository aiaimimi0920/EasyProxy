package monitor

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type proxyCompatCatalog struct {
	ProviderTypes         []proxyCompatProviderType     `json:"providerTypes"`
	RuntimeTemplates      []any                         `json:"runtimeTemplates"`
	StrategyProfiles      []proxyCompatStrategyProfile  `json:"strategyProfiles"`
	ProviderGroups        []proxyCompatProviderGroup    `json:"providerGroups"`
	BusinessStrategies    []proxyCompatBusinessStrategy `json:"businessStrategies"`
	DefaultStrategyModeID string                        `json:"defaultStrategyModeId,omitempty"`
	DefaultStrategyMode   *proxyCompatStrategyMode      `json:"defaultStrategyMode,omitempty"`
	SupportsStrategyMode  bool                          `json:"supportsStrategyMode"`
}

type proxyCompatSnapshot struct {
	ProviderTypes      []proxyCompatProviderType     `json:"providerTypes"`
	RuntimeTemplates   []any                         `json:"runtimeTemplates"`
	Instances          []proxyCompatProviderInstance `json:"instances"`
	Bindings           []proxyCompatBinding          `json:"bindings"`
	Strategies         []proxyCompatStrategyProfile  `json:"strategies"`
	CredentialSets     []any                         `json:"credentialSets"`
	CredentialBindings []any                         `json:"credentialBindings"`
	Leases             []proxyCompatLease            `json:"leases"`
	UsageRecords       []proxyCompatUsageRecord      `json:"usageRecords"`
	ServiceFeedback    []proxyCompatServiceFeedback  `json:"serviceFeedback"`
	UsageStats         []proxyCompatUsageStats       `json:"usageStats"`
}

type proxyCompatProviderType struct {
	Key                         string   `json:"key"`
	DisplayName                 string   `json:"displayName"`
	Description                 string   `json:"description"`
	SupportsDynamicProvisioning bool     `json:"supportsDynamicProvisioning"`
	DefaultStrategyKey          string   `json:"defaultStrategyKey"`
	Tags                        []string `json:"tags"`
}

type proxyCompatProviderGroup struct {
	Key              string   `json:"key"`
	DisplayName      string   `json:"displayName"`
	ProviderTypeKeys []string `json:"providerTypeKeys"`
	Description      string   `json:"description"`
}

type proxyCompatBusinessStrategy struct {
	ID                  string   `json:"id"`
	DisplayName         string   `json:"displayName"`
	Description         string   `json:"description"`
	ProviderGroupOrder  []string `json:"providerGroupOrder,omitempty"`
	FallbackProfileID   string   `json:"fallbackProfileId,omitempty"`
	FallbackStrategyKey string   `json:"fallbackStrategyKey,omitempty"`
}

type proxyCompatStrategyProfile struct {
	ID          string            `json:"id"`
	Key         string            `json:"key"`
	DisplayName string            `json:"displayName"`
	Description string            `json:"description"`
	Metadata    map[string]string `json:"metadata"`
}

type proxyCompatStrategyMode struct {
	Service                string   `json:"service"`
	ModeID                 string   `json:"modeId"`
	ProviderSelections     []string `json:"providerSelections"`
	EligibleProviderGroups []string `json:"eligibleProviderGroups"`
	ProviderGroupOrder     []string `json:"providerGroupOrder"`
	StrategyKey            string   `json:"strategyKey,omitempty"`
	Warnings               []string `json:"warnings"`
	Explain                []string `json:"explain"`
}

type proxyCompatProviderInstance struct {
	ID               string            `json:"id"`
	ProviderTypeKey  string            `json:"providerTypeKey"`
	DisplayName      string            `json:"displayName"`
	Status           string            `json:"status"`
	RuntimeKind      string            `json:"runtimeKind"`
	ConnectorKind    string            `json:"connectorKind"`
	Shared           bool              `json:"shared"`
	CostTier         string            `json:"costTier"`
	HealthScore      float64           `json:"healthScore"`
	AverageLatencyMs int64             `json:"averageLatencyMs"`
	ConnectionRef    string            `json:"connectionRef"`
	HostBindings     []string          `json:"hostBindings"`
	GroupKeys        []string          `json:"groupKeys"`
	Metadata         map[string]string `json:"metadata"`
	CreatedAt        string            `json:"createdAt"`
	UpdatedAt        string            `json:"updatedAt"`
}

type proxyCompatBinding struct {
	HostID          string `json:"hostId"`
	ProviderTypeKey string `json:"providerTypeKey"`
	BindingMode     string `json:"bindingMode"`
	InstanceID      string `json:"instanceId"`
	GroupKey        string `json:"groupKey,omitempty"`
	UpdatedAt       string `json:"updatedAt"`
}

type proxyCompatCheckoutRequest struct {
	HostID                  string            `json:"hostId"`
	ProviderTypeKey         string            `json:"providerTypeKey,omitempty"`
	ProvisionMode           string            `json:"provisionMode"`
	BindingMode             string            `json:"bindingMode"`
	StrategyProfileID       string            `json:"strategyProfileId,omitempty"`
	ProviderStrategyModeID  string            `json:"providerStrategyModeId,omitempty"`
	ProviderGroupSelections []string          `json:"providerGroupSelections,omitempty"`
	PreferredInstanceID     string            `json:"preferredInstanceId,omitempty"`
	RuntimeTemplateID       string            `json:"runtimeTemplateId,omitempty"`
	GroupKey                string            `json:"groupKey,omitempty"`
	Protocol                string            `json:"protocol,omitempty"`
	TTLMinutes              int               `json:"ttlMinutes,omitempty"`
	Metadata                map[string]string `json:"metadata,omitempty"`
}

type proxyCompatPlanResult struct {
	Request               proxyCompatCheckoutRequest  `json:"request"`
	ProviderType          proxyCompatProviderType     `json:"providerType"`
	Instance              proxyCompatProviderInstance `json:"instance"`
	Binding               proxyCompatBinding          `json:"binding"`
	StrategyProfile       *proxyCompatStrategyProfile `json:"strategyProfile,omitempty"`
	ReusedExistingBinding bool                        `json:"reusedExistingBinding"`
	RequiresProvisioning  bool                        `json:"requiresProvisioning"`
	StrategyMode          *proxyCompatStrategyMode    `json:"strategyMode,omitempty"`
}

type proxyCompatCheckoutResult struct {
	Lease        proxyCompatLease            `json:"lease"`
	Instance     proxyCompatProviderInstance `json:"instance"`
	Binding      proxyCompatBinding          `json:"binding"`
	StrategyMode *proxyCompatStrategyMode    `json:"strategyMode,omitempty"`
}

type proxyCompatLease struct {
	ID                 string            `json:"id"`
	HostID             string            `json:"hostId"`
	ProviderTypeKey    string            `json:"providerTypeKey"`
	ProviderInstanceID string            `json:"providerInstanceId"`
	ProxyURL           string            `json:"proxyUrl"`
	Host               string            `json:"host"`
	Port               int               `json:"port"`
	Protocol           string            `json:"protocol"`
	Username           string            `json:"username,omitempty"`
	Password           string            `json:"password,omitempty"`
	Status             string            `json:"status"`
	CreatedAt          string            `json:"createdAt"`
	ExpiresAt          string            `json:"expiresAt,omitempty"`
	ReleasedAt         string            `json:"releasedAt,omitempty"`
	Metadata           map[string]string `json:"metadata"`
}

type proxyCompatUsageReport struct {
	LeaseID         string `json:"leaseId"`
	Success         bool   `json:"success"`
	LatencyMs       int64  `json:"latencyMs,omitempty"`
	ErrorCode       string `json:"errorCode,omitempty"`
	ReportedAt      string `json:"reportedAt,omitempty"`
	ServiceKey      string `json:"serviceKey,omitempty"`
	Stage           string `json:"stage,omitempty"`
	FailureClass    string `json:"failureClass,omitempty"`
	RouteConfidence string `json:"routeConfidence,omitempty"`
}

type proxyCompatUsageRecord struct {
	ID                 string `json:"id"`
	LeaseID            string `json:"leaseId"`
	ProviderInstanceID string `json:"providerInstanceId"`
	SelectedNodeTag    string `json:"selectedNodeTag,omitempty"`
	ReportedAt         string `json:"reportedAt"`
	Success            bool   `json:"success"`
	LatencyMs          int64  `json:"latencyMs,omitempty"`
	ErrorCode          string `json:"errorCode,omitempty"`
	ServiceKey         string `json:"serviceKey,omitempty"`
	Stage              string `json:"stage,omitempty"`
	FailureClass       string `json:"failureClass,omitempty"`
	RouteConfidence    string `json:"routeConfidence,omitempty"`
}

type proxyCompatServiceFeedback struct {
	HostID              string `json:"hostId"`
	NodeTag             string `json:"nodeTag"`
	FeedbackKey         string `json:"feedbackKey,omitempty"`
	ScopeKind           string `json:"scopeKind,omitempty"`
	ScopeValue          string `json:"scopeValue,omitempty"`
	Penalty             int    `json:"penalty"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`
	CooldownUntil       string `json:"cooldownUntil,omitempty"`
	LastErrorClass      string `json:"lastErrorClass,omitempty"`
	LastErrorCode       string `json:"lastErrorCode,omitempty"`
	LastReportedAt      string `json:"lastReportedAt,omitempty"`
}

type proxyCompatUsageStats struct {
	ServiceKey                 string  `json:"serviceKey"`
	Stage                      string  `json:"stage"`
	NodeTag                    string  `json:"nodeTag,omitempty"`
	ServiceTotal               int     `json:"serviceTotal"`
	ServiceFailures            int     `json:"serviceFailures"`
	ServiceFailureRate         float64 `json:"serviceFailureRate"`
	ServiceSentinelFailures    int     `json:"serviceSentinelFailures"`
	ServiceSentinelFailureRate float64 `json:"serviceSentinelFailureRate"`
	NodeTotal                  int     `json:"nodeTotal"`
	NodeFailures               int     `json:"nodeFailures"`
	NodeSuccesses              int     `json:"nodeSuccesses"`
	NodeSuccessRate            float64 `json:"nodeSuccessRate"`
	NodeFailureRate            float64 `json:"nodeFailureRate"`
	NodeSentinelFailures       int     `json:"nodeSentinelFailures"`
	NodeSentinelFailureRate    float64 `json:"nodeSentinelFailureRate"`
}

type proxyCompatState struct {
	mu               sync.RWMutex
	leases           map[string]*proxyCompatLeaseState
	usageRecords     []proxyCompatUsageRecord
	nodeReservations map[string]int
	serviceFeedback  map[string]map[string]*proxyCompatServiceFeedback
}

type proxyCompatLeaseState struct {
	Lease           proxyCompatLease
	SelectedNodeTag string
}

type proxyCompatRuntime struct {
	SharedHost              string
	SharedPort              int
	SharedProtocol          string
	SharedUsername          string
	SharedPassword          string
	AllowSharedPoolFallback bool
	NodeProtocol            string
	NodeUsername            string
	NodePassword            string
	ManagementPort          int
	ConnectionRef           string
	ManagementURL           string
	ProviderInstanceID      string
	ProviderDisplayName     string
	CreatedAt               string
	UpdatedAt               string
}

type proxyCompatCandidate struct {
	Snapshot             Snapshot
	ReservationCount     int
	ServiceLeaseCount    int
	ServicePenalty       int
	ServiceCooling       bool
	UsageStats           proxyCompatUsageStats
	RecentSuccessCount   int
	RecentSuccessStreak  int
	RecentSuccessPenalty int
	SelectionTier        string
	EndpointHost         string
	EndpointPort         int
	Protocol             string
	Username             string
	Password             string
	EndpointMode         string
}

type proxyCompatRecentSuccessReusePreference struct {
	Enabled   bool
	Threshold int
	Window    time.Duration
}

type proxyCompatMaintenanceResult struct {
	expired []string
	cleaned []string
}

var (
	errProxyCompatUnsupportedProvider = errors.New("requested provider is not supported by the EasyProxy compatibility layer")
	errProxyCompatNoNodes             = errors.New("no effective EasyProxy nodes are currently available")
)

type proxyCompatUsageFailureScope string

const (
	proxyCompatUsageFailureNone    proxyCompatUsageFailureScope = "none"
	proxyCompatUsageFailureGlobal  proxyCompatUsageFailureScope = "global"
	proxyCompatUsageFailureService proxyCompatUsageFailureScope = "service"
)

const (
	proxyCompatFailureClassNone         = "none"
	proxyCompatFailureClassUnknown      = "unknown"
	proxyCompatFailureClassRouteFailure = "route_failure"
	proxyCompatFailureClassBusinessRisk = "business_risk"
	proxyCompatFailureClassAccountAuth  = "account_or_auth"
)

const (
	proxyCompatRouteConfidenceLow    = "low"
	proxyCompatRouteConfidenceMedium = "medium"
	proxyCompatRouteConfidenceHigh   = "high"
)

const (
	proxyCompatFeedbackScopeNode           = "node"
	proxyCompatFeedbackScopeProtocolFamily = "protocol_family"
	proxyCompatFeedbackScopeNodeMode       = "node_mode"
	proxyCompatFeedbackScopeDomainFamily   = "domain_family"
)

type proxyCompatServiceFeedbackRef struct {
	Key        string
	ScopeKind  string
	ScopeValue string
}

type proxyCompatUsageFeedbackDecision struct {
	Scope              proxyCompatUsageFailureScope
	ErrorClass         string
	Penalty            int
	CooldownBase       time.Duration
	CooldownEscalated  time.Duration
	EscalateAfterCount int
}

func newProxyCompatState() *proxyCompatState {
	return &proxyCompatState{
		leases:           make(map[string]*proxyCompatLeaseState),
		usageRecords:     make([]proxyCompatUsageRecord, 0, 64),
		nodeReservations: make(map[string]int),
		serviceFeedback:  make(map[string]map[string]*proxyCompatServiceFeedback),
	}
}

func (s *Server) compatState() *proxyCompatState {
	if s.proxyCompat == nil {
		s.proxyCompat = newProxyCompatState()
	}
	return s.proxyCompat
}

func (s *Server) handleProxyCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	runtimeCfg := s.resolveProxyCompatRuntime(r)
	writeJSON(w, map[string]any{
		"catalog": proxyCompatCatalog{
			ProviderTypes:    []proxyCompatProviderType{proxyCompatProviderTypeDefinition()},
			RuntimeTemplates: []any{},
			StrategyProfiles: []proxyCompatStrategyProfile{proxyCompatStrategyProfileDefinition()},
			ProviderGroups: []proxyCompatProviderGroup{
				{
					Key:              "easy-proxies",
					DisplayName:      "EasyProxy",
					ProviderTypeKeys: []string{"easy-proxies"},
					Description:      "EasyProxy managed runtime pool",
				},
				{
					Key:              "manual",
					DisplayName:      "Manual",
					ProviderTypeKeys: []string{},
					Description:      "Legacy manual providers are not hosted by this compatibility layer.",
				},
			},
			BusinessStrategies: []proxyCompatBusinessStrategy{
				{
					ID:                  "available-first",
					DisplayName:         "Available First",
					Description:         "Prefer any currently available EasyProxy node.",
					ProviderGroupOrder:  []string{"easy-proxies"},
					FallbackStrategyKey: "health-first",
				},
				{
					ID:                  "easy-proxies-first",
					DisplayName:         "EasyProxy First",
					Description:         "Directly use the EasyProxy compatibility pool.",
					ProviderGroupOrder:  []string{"easy-proxies"},
					FallbackStrategyKey: "health-first",
				},
			},
			DefaultStrategyModeID: "easy-proxies-first",
			DefaultStrategyMode:   proxyCompatStrategyModeDefinition(),
			SupportsStrategyMode:  true,
		},
		"runtime": map[string]any{
			"managementUrl": runtimeCfg.ManagementURL,
		},
	})
}

func (s *Server) handleProxySnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	runtimeCfg := s.resolveProxyCompatRuntime(r)
	instance := s.buildProxyCompatInstance(r, runtimeCfg)
	leases, usage, serviceFeedback, usageStats := s.compatState().snapshot()
	writeJSON(w, map[string]any{
		"snapshot": proxyCompatSnapshot{
			ProviderTypes:      []proxyCompatProviderType{proxyCompatProviderTypeDefinition()},
			RuntimeTemplates:   []any{},
			Instances:          []proxyCompatProviderInstance{instance},
			Bindings:           s.buildProxyCompatBindings(instance.ID, leases),
			Strategies:         []proxyCompatStrategyProfile{proxyCompatStrategyProfileDefinition()},
			CredentialSets:     []any{},
			CredentialBindings: []any{},
			Leases:             leases,
			UsageRecords:       usage,
			ServiceFeedback:    serviceFeedback,
			UsageStats:         usageStats,
		},
	})
}

func (s *Server) handleProxyPlanCheckout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.mgr != nil {
		if err := s.mgr.WaitForInitialProbe(0); err != nil {
			writeProxyCompatError(w, http.StatusServiceUnavailable, "INITIAL_PROXY_PROBE_PENDING", err.Error())
			return
		}
	}

	var request proxyCompatCheckoutRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeProxyCompatError(w, http.StatusBadRequest, "INVALID_JSON", "Request body is not valid JSON.")
		return
	}

	candidate, runtimeCfg, err := s.resolveProxyCompatCandidate(r, request)
	if err != nil {
		s.respondProxyCompatResolveError(w, err)
		return
	}

	instance := s.buildProxyCompatInstance(r, runtimeCfg)
	writeJSON(w, map[string]any{
		"plan": proxyCompatPlanResult{
			Request:               request,
			ProviderType:          proxyCompatProviderTypeDefinition(),
			Instance:              instance,
			Binding:               s.buildProxyCompatBinding(request.HostID, instance.ID, request.BindingMode),
			StrategyProfile:       ptrProxyCompatStrategyProfile(proxyCompatStrategyProfileDefinition()),
			ReusedExistingBinding: false,
			RequiresProvisioning:  false,
			StrategyMode:          proxyCompatStrategyModeDefinition(),
		},
		"selectedNode": map[string]any{
			"tag":          candidate.Snapshot.Tag,
			"name":         candidate.Snapshot.Name,
			"endpointHost": candidate.EndpointHost,
			"endpointPort": candidate.EndpointPort,
			"endpointMode": candidate.EndpointMode,
		},
	})
}

func (s *Server) handleProxyCheckout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.mgr != nil {
		if err := s.mgr.WaitForInitialProbe(0); err != nil {
			writeProxyCompatError(w, http.StatusServiceUnavailable, "INITIAL_PROXY_PROBE_PENDING", err.Error())
			return
		}
	}

	var request proxyCompatCheckoutRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeProxyCompatError(w, http.StatusBadRequest, "INVALID_JSON", "Request body is not valid JSON.")
		return
	}

	s.proxyCompatCheckoutMu.Lock()
	defer s.proxyCompatCheckoutMu.Unlock()

	candidate, runtimeCfg, err := s.resolveProxyCompatCandidate(r, request)
	if err != nil {
		s.respondProxyCompatResolveError(w, err)
		return
	}

	lease, leaseState := s.createProxyCompatLease(request, runtimeCfg, candidate)
	s.compatState().storeLease(leaseState)
	instance := s.buildProxyCompatInstance(r, runtimeCfg)
	writeJSON(w, map[string]any{
		"result": proxyCompatCheckoutResult{
			Lease:        lease,
			Instance:     instance,
			Binding:      s.buildProxyCompatBinding(request.HostID, instance.ID, request.BindingMode),
			StrategyMode: proxyCompatStrategyModeDefinition(),
		},
	})
}

func (s *Server) handleProxyReportUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var report proxyCompatUsageReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		writeProxyCompatError(w, http.StatusBadRequest, "INVALID_JSON", "Request body is not valid JSON.")
		return
	}
	if strings.TrimSpace(report.LeaseID) == "" {
		writeProxyCompatError(w, http.StatusBadRequest, "INVALID_REPORT", "leaseId is required.")
		return
	}

	record, selectedNodeTag, hostID, err := s.compatState().recordUsage(report)
	if err != nil {
		writeProxyCompatError(w, http.StatusNotFound, "LEASE_NOT_FOUND", err.Error())
		return
	}
	s.applyProxyCompatUsageFeedback(selectedNodeTag, hostID, record)
	writeJSON(w, map[string]any{"record": record})
}

func (s *Server) handleProxyLeaseItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/proxy/leases/")
	if path == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if strings.HasSuffix(path, "/release") {
		leaseID := strings.TrimSuffix(path, "/release")
		s.handleProxyReleaseLease(w, r, leaseID)
		return
	}

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	leaseID, err := url.PathUnescape(path)
	if err != nil || strings.TrimSpace(leaseID) == "" {
		writeProxyCompatError(w, http.StatusBadRequest, "INVALID_LEASE_ID", "Lease id is invalid.")
		return
	}

	lease, ok := s.compatState().readLease(leaseID)
	if !ok {
		writeJSON(w, map[string]any{"lease": nil})
		return
	}
	writeJSON(w, map[string]any{"lease": lease})
}

func (s *Server) handleProxyReleaseLease(w http.ResponseWriter, r *http.Request, encodedLeaseID string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	leaseID, err := url.PathUnescape(encodedLeaseID)
	if err != nil || strings.TrimSpace(leaseID) == "" {
		writeProxyCompatError(w, http.StatusBadRequest, "INVALID_LEASE_ID", "Lease id is invalid.")
		return
	}

	if err := s.compatState().releaseLease(leaseID); err != nil {
		writeProxyCompatError(w, http.StatusNotFound, "LEASE_NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleProxyMaintenanceRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	maintenance := s.compatState().runMaintenance()
	writeJSON(w, map[string]any{
		"maintenance": map[string]any{
			"expired":   maintenance.expired,
			"cleaned":   maintenance.cleaned,
			"refreshed": []any{},
		},
	})
}

func (state *proxyCompatState) runMaintenance() proxyCompatMaintenanceResult {
	now := time.Now()
	state.mu.Lock()
	defer state.mu.Unlock()
	state.clearExpiredServiceCooldownsLocked(now)

	expired := make([]string, 0)
	for leaseID, leaseState := range state.leases {
		if leaseState.Lease.ExpiresAt == "" {
			continue
		}
		expiresAt, err := time.Parse(time.RFC3339, leaseState.Lease.ExpiresAt)
		if err != nil || !now.After(expiresAt) {
			continue
		}
		if leaseState.Lease.Status == "active" {
			leaseState.Lease.Status = "expired"
			leaseState.Lease.ReleasedAt = now.Format(time.RFC3339)
			state.releaseReservationLocked(leaseState.SelectedNodeTag)
		}
		expired = append(expired, leaseID)
	}

	cleaned := make([]string, 0)
	const keepUsageRecords = 2048
	if len(state.usageRecords) > keepUsageRecords {
		dropCount := len(state.usageRecords) - keepUsageRecords
		for idx := 0; idx < dropCount; idx++ {
			cleaned = append(cleaned, state.usageRecords[idx].ID)
		}
		state.usageRecords = append([]proxyCompatUsageRecord(nil), state.usageRecords[dropCount:]...)
	}

	cutoff := now.Add(-24 * time.Hour)
	for hostID, byNode := range state.serviceFeedback {
		for nodeTag, feedback := range byNode {
			cooldownUntil, _ := time.Parse(time.RFC3339, strings.TrimSpace(feedback.CooldownUntil))
			lastReportedAt, _ := time.Parse(time.RFC3339, strings.TrimSpace(feedback.LastReportedAt))
			if (cooldownUntil.IsZero() || now.After(cooldownUntil)) && (lastReportedAt.IsZero() || lastReportedAt.Before(cutoff)) {
				delete(byNode, nodeTag)
			}
		}
		if len(byNode) == 0 {
			delete(state.serviceFeedback, hostID)
		}
	}

	return proxyCompatMaintenanceResult{
		expired: expired,
		cleaned: cleaned,
	}
}

func (state *proxyCompatState) storeLease(leaseState *proxyCompatLeaseState) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.leases[leaseState.Lease.ID] = leaseState
	if leaseState.SelectedNodeTag != "" {
		state.nodeReservations[leaseState.SelectedNodeTag]++
	}
}

func (state *proxyCompatState) readLease(leaseID string) (proxyCompatLease, bool) {
	state.mu.RLock()
	defer state.mu.RUnlock()
	leaseState, ok := state.leases[leaseID]
	if !ok {
		return proxyCompatLease{}, false
	}
	return cloneProxyCompatLease(leaseState.Lease), true
}

func (state *proxyCompatState) releaseLease(leaseID string) error {
	state.mu.Lock()
	defer state.mu.Unlock()

	leaseState, ok := state.leases[leaseID]
	if !ok {
		return fmt.Errorf("lease %q not found", leaseID)
	}
	if leaseState.Lease.Status != "active" {
		return nil
	}
	leaseState.Lease.Status = "released"
	leaseState.Lease.ReleasedAt = time.Now().Format(time.RFC3339)
	state.releaseReservationLocked(leaseState.SelectedNodeTag)
	return nil
}

func (state *proxyCompatState) recordUsage(report proxyCompatUsageReport) (proxyCompatUsageRecord, string, string, error) {
	state.mu.Lock()
	defer state.mu.Unlock()

	leaseState, ok := state.leases[report.LeaseID]
	if !ok {
		return proxyCompatUsageRecord{}, "", "", fmt.Errorf("lease %q not found", report.LeaseID)
	}

	reportedAt := time.Now().Format(time.RFC3339)
	if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(report.ReportedAt)); err == nil {
		reportedAt = parsed.Format(time.RFC3339)
	}

	record := proxyCompatUsageRecord{
		ID:                 mustGenerateCompatID("usage"),
		LeaseID:            report.LeaseID,
		ProviderInstanceID: leaseState.Lease.ProviderInstanceID,
		SelectedNodeTag:    leaseState.SelectedNodeTag,
		ReportedAt:         reportedAt,
		Success:            report.Success,
	}
	if report.LatencyMs > 0 {
		record.LatencyMs = report.LatencyMs
	}
	if trimmed := strings.TrimSpace(report.ErrorCode); trimmed != "" {
		record.ErrorCode = trimmed
	}
	record.ServiceKey = normalizeProxyCompatServiceKey(
		firstNonEmptyCompatValue(
			report.ServiceKey,
			leaseState.Lease.Metadata["serviceKey"],
			leaseState.Lease.HostID,
		),
		leaseState.Lease.HostID,
	)
	record.Stage = normalizeProxyCompatUsageStage(
		firstNonEmptyCompatValue(
			report.Stage,
			leaseState.Lease.Metadata["stage"],
			leaseState.Lease.Metadata["purpose"],
		),
	)
	record.FailureClass = normalizeProxyCompatFailureClass(report.FailureClass)
	record.RouteConfidence = normalizeProxyCompatRouteConfidence(report.RouteConfidence)
	if !record.Success {
		inferredClass, inferredConfidence := inferProxyCompatFailureSemantics(record.ErrorCode)
		if record.FailureClass == "" {
			record.FailureClass = inferredClass
		}
		if record.RouteConfidence == "" {
			record.RouteConfidence = inferredConfidence
		}
	} else {
		if record.FailureClass == "" {
			record.FailureClass = proxyCompatFailureClassNone
		}
		if record.RouteConfidence == "" {
			record.RouteConfidence = proxyCompatRouteConfidenceLow
		}
	}

	state.usageRecords = append(state.usageRecords, record)
	if len(state.usageRecords) > 2048 {
		state.usageRecords = append([]proxyCompatUsageRecord(nil), state.usageRecords[len(state.usageRecords)-2048:]...)
	}
	return record, leaseState.SelectedNodeTag, leaseState.Lease.HostID, nil
}

func (state *proxyCompatState) snapshot() ([]proxyCompatLease, []proxyCompatUsageRecord, []proxyCompatServiceFeedback, []proxyCompatUsageStats) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.clearExpiredServiceCooldownsLocked(time.Now())

	leases := make([]proxyCompatLease, 0, len(state.leases))
	for _, leaseState := range state.leases {
		leases = append(leases, cloneProxyCompatLease(leaseState.Lease))
	}
	sort.SliceStable(leases, func(i, j int) bool {
		return leases[i].CreatedAt > leases[j].CreatedAt
	})

	usage := append([]proxyCompatUsageRecord(nil), state.usageRecords...)
	sort.SliceStable(usage, func(i, j int) bool {
		return usage[i].ReportedAt > usage[j].ReportedAt
	})

	serviceFeedback := make([]proxyCompatServiceFeedback, 0, len(state.serviceFeedback))
	for hostID, byNode := range state.serviceFeedback {
		for _, feedback := range byNode {
			if feedback == nil {
				continue
			}
			serviceFeedback = append(serviceFeedback, proxyCompatServiceFeedback{
				HostID:              hostID,
				NodeTag:             feedback.NodeTag,
				FeedbackKey:         feedback.FeedbackKey,
				ScopeKind:           feedback.ScopeKind,
				ScopeValue:          feedback.ScopeValue,
				Penalty:             feedback.Penalty,
				ConsecutiveFailures: feedback.ConsecutiveFailures,
				CooldownUntil:       feedback.CooldownUntil,
				LastErrorClass:      feedback.LastErrorClass,
				LastErrorCode:       feedback.LastErrorCode,
				LastReportedAt:      feedback.LastReportedAt,
			})
		}
	}
	sort.SliceStable(serviceFeedback, func(i, j int) bool {
		if serviceFeedback[i].HostID != serviceFeedback[j].HostID {
			return serviceFeedback[i].HostID < serviceFeedback[j].HostID
		}
		if serviceFeedback[i].ScopeKind != serviceFeedback[j].ScopeKind {
			return serviceFeedback[i].ScopeKind < serviceFeedback[j].ScopeKind
		}
		return serviceFeedback[i].ScopeValue < serviceFeedback[j].ScopeValue
	})
	return leases, usage, serviceFeedback, buildProxyCompatUsageStats(state.usageRecords)
}

func (state *proxyCompatState) businessRiskStats(serviceKey, stage, nodeTag string) proxyCompatUsageStats {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return computeProxyCompatUsageStats(state.usageRecords, serviceKey, stage, nodeTag)
}

func buildProxyCompatUsageStats(records []proxyCompatUsageRecord) []proxyCompatUsageStats {
	type serviceStageKey struct {
		serviceKey string
		stage      string
	}
	type nodeKey struct {
		serviceKey string
		stage      string
		nodeTag    string
	}

	serviceTotals := make(map[serviceStageKey]*proxyCompatUsageStats)
	nodeTotals := make(map[nodeKey]*proxyCompatUsageStats)

	for _, record := range records {
		serviceKey := normalizeProxyCompatServiceKey(record.ServiceKey, "")
		stage := normalizeProxyCompatUsageStage(record.Stage)
		if serviceKey == "" || stage == "" || !proxyCompatCountsTowardBusinessBaseline(record) {
			continue
		}

		svcKey := serviceStageKey{serviceKey: serviceKey, stage: stage}
		svcStats, ok := serviceTotals[svcKey]
		if !ok {
			svcStats = &proxyCompatUsageStats{
				ServiceKey: serviceKey,
				Stage:      stage,
			}
			serviceTotals[svcKey] = svcStats
		}
		svcStats.ServiceTotal++
		if !record.Success {
			svcStats.ServiceFailures++
			if proxyCompatCountsAsSentinelRateLimit(record.ErrorCode) {
				svcStats.ServiceSentinelFailures++
			}
		}

		if strings.TrimSpace(record.SelectedNodeTag) == "" {
			continue
		}
		nKey := nodeKey{
			serviceKey: serviceKey,
			stage:      stage,
			nodeTag:    strings.TrimSpace(record.SelectedNodeTag),
		}
		nodeStats, ok := nodeTotals[nKey]
		if !ok {
			nodeStats = &proxyCompatUsageStats{
				ServiceKey: serviceKey,
				Stage:      stage,
				NodeTag:    strings.TrimSpace(record.SelectedNodeTag),
			}
			nodeTotals[nKey] = nodeStats
		}
		nodeStats.NodeTotal++
		if record.Success {
			nodeStats.NodeSuccesses++
		} else {
			nodeStats.NodeFailures++
			if proxyCompatCountsAsSentinelRateLimit(record.ErrorCode) {
				nodeStats.NodeSentinelFailures++
			}
		}
	}

	result := make([]proxyCompatUsageStats, 0, len(nodeTotals))
	for key, nodeStats := range nodeTotals {
		svcStats := serviceTotals[serviceStageKey{serviceKey: key.serviceKey, stage: key.stage}]
		if svcStats != nil {
			nodeStats.ServiceTotal = svcStats.ServiceTotal
			nodeStats.ServiceFailures = svcStats.ServiceFailures
			nodeStats.ServiceFailureRate = proxyCompatFailureRate(svcStats.ServiceFailures, svcStats.ServiceTotal)
			nodeStats.ServiceSentinelFailures = svcStats.ServiceSentinelFailures
			nodeStats.ServiceSentinelFailureRate = proxyCompatFailureRate(svcStats.ServiceSentinelFailures, svcStats.ServiceTotal)
		}
		nodeStats.NodeSuccessRate = proxyCompatRatio(nodeStats.NodeSuccesses, nodeStats.NodeTotal)
		nodeStats.NodeFailureRate = proxyCompatFailureRate(nodeStats.NodeFailures, nodeStats.NodeTotal)
		nodeStats.NodeSentinelFailureRate = proxyCompatFailureRate(nodeStats.NodeSentinelFailures, nodeStats.NodeTotal)
		result = append(result, *nodeStats)
	}

	sort.SliceStable(result, func(i, j int) bool {
		if result[i].ServiceKey != result[j].ServiceKey {
			return result[i].ServiceKey < result[j].ServiceKey
		}
		if result[i].Stage != result[j].Stage {
			return result[i].Stage < result[j].Stage
		}
		return result[i].NodeTag < result[j].NodeTag
	})
	return result
}

func computeProxyCompatUsageStats(records []proxyCompatUsageRecord, serviceKey, stage, nodeTag string) proxyCompatUsageStats {
	serviceKey = normalizeProxyCompatServiceKey(serviceKey, "")
	stage = normalizeProxyCompatUsageStage(stage)
	nodeTag = strings.TrimSpace(nodeTag)
	stats := proxyCompatUsageStats{
		ServiceKey: serviceKey,
		Stage:      stage,
		NodeTag:    nodeTag,
	}
	if serviceKey == "" || stage == "" || nodeTag == "" {
		return stats
	}

	for _, record := range records {
		if normalizeProxyCompatServiceKey(record.ServiceKey, "") != serviceKey {
			continue
		}
		if normalizeProxyCompatUsageStage(record.Stage) != stage {
			continue
		}
		if !proxyCompatCountsTowardBusinessBaseline(record) {
			continue
		}
		stats.ServiceTotal++
		if !record.Success {
			stats.ServiceFailures++
			if proxyCompatCountsAsSentinelRateLimit(record.ErrorCode) {
				stats.ServiceSentinelFailures++
			}
		}
		if strings.TrimSpace(record.SelectedNodeTag) != nodeTag {
			continue
		}
		stats.NodeTotal++
		if record.Success {
			stats.NodeSuccesses++
		} else {
			stats.NodeFailures++
			if proxyCompatCountsAsSentinelRateLimit(record.ErrorCode) {
				stats.NodeSentinelFailures++
			}
		}
	}

	stats.ServiceFailureRate = proxyCompatFailureRate(stats.ServiceFailures, stats.ServiceTotal)
	stats.ServiceSentinelFailureRate = proxyCompatFailureRate(stats.ServiceSentinelFailures, stats.ServiceTotal)
	stats.NodeSuccessRate = proxyCompatRatio(stats.NodeSuccesses, stats.NodeTotal)
	stats.NodeFailureRate = proxyCompatFailureRate(stats.NodeFailures, stats.NodeTotal)
	stats.NodeSentinelFailureRate = proxyCompatFailureRate(stats.NodeSentinelFailures, stats.NodeTotal)
	return stats
}

func proxyCompatCountsAsSentinelRateLimit(errorCode string) bool {
	normalized := strings.ToLower(strings.TrimSpace(errorCode))
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "sentinel rate limit") ||
		strings.Contains(normalized, "blocked by sentinel") ||
		strings.Contains(normalized, "code\": \"555\"") ||
		(strings.Contains(normalized, "sentinel") && strings.Contains(normalized, "555"))
}

func proxyCompatCountsTowardBusinessBaseline(record proxyCompatUsageRecord) bool {
	if record.Success {
		return true
	}
	return normalizeProxyCompatFailureClass(record.FailureClass) == proxyCompatFailureClassBusinessRisk
}

func proxyCompatFailureRate(failures, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(failures) / float64(total)
}

func proxyCompatRatio(value, total int) float64 {
	if total <= 0 {
		return 0
	}
	return float64(value) / float64(total)
}

func (state *proxyCompatState) reservationCount(nodeTag string) int {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.nodeReservations[nodeTag]
}

func (state *proxyCompatState) activeServiceLeaseCount(serviceKey, nodeTag string) int {
	normalizedServiceKey := normalizeProxyCompatServiceKey(serviceKey, "")
	normalizedNodeTag := strings.TrimSpace(nodeTag)
	if normalizedServiceKey == "" || normalizedNodeTag == "" {
		return 0
	}

	state.mu.RLock()
	defer state.mu.RUnlock()

	count := 0
	for _, leaseState := range state.leases {
		if leaseState == nil || leaseState.Lease.Status != "active" {
			continue
		}
		if strings.TrimSpace(leaseState.SelectedNodeTag) != normalizedNodeTag {
			continue
		}
		leaseServiceKey := normalizeProxyCompatServiceKey(
			firstNonEmptyCompatValue(
				leaseState.Lease.Metadata["serviceKey"],
				leaseState.Lease.Metadata["service"],
				leaseState.Lease.HostID,
			),
			leaseState.Lease.HostID,
		)
		if leaseServiceKey == normalizedServiceKey {
			count++
		}
	}
	return count
}

func (state *proxyCompatState) recentServiceSuccessStats(serviceKey, stage, nodeTag string, window time.Duration) (int, int) {
	normalizedServiceKey := normalizeProxyCompatServiceKey(serviceKey, "")
	normalizedStage := normalizeProxyCompatUsageStage(stage)
	normalizedNodeTag := strings.TrimSpace(nodeTag)
	if normalizedServiceKey == "" || normalizedStage == "" || normalizedNodeTag == "" || window <= 0 {
		return 0, 0
	}

	cutoff := time.Now().Add(-window)
	state.mu.RLock()
	defer state.mu.RUnlock()

	successCount := 0
	successStreak := 0
	streakOpen := true
	for idx := len(state.usageRecords) - 1; idx >= 0; idx-- {
		record := state.usageRecords[idx]
		if strings.TrimSpace(record.SelectedNodeTag) != normalizedNodeTag {
			continue
		}
		if normalizeProxyCompatServiceKey(record.ServiceKey, "") != normalizedServiceKey {
			continue
		}
		if normalizeProxyCompatUsageStage(record.Stage) != normalizedStage {
			continue
		}

		reportedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(record.ReportedAt))
		if err == nil && !reportedAt.IsZero() && reportedAt.Before(cutoff) {
			break
		}

		if record.Success {
			successCount++
			if streakOpen {
				successStreak++
			}
			continue
		}
		streakOpen = false
	}

	return successCount, successStreak
}

func (state *proxyCompatState) releaseReservationLocked(nodeTag string) {
	if nodeTag == "" {
		return
	}
	current := state.nodeReservations[nodeTag]
	if current <= 1 {
		delete(state.nodeReservations, nodeTag)
		return
	}
	state.nodeReservations[nodeTag] = current - 1
}

func (state *proxyCompatState) serviceFeedbackForNode(hostID, nodeTag string) (proxyCompatServiceFeedback, bool) {
	return state.serviceFeedbackForRef(hostID, proxyCompatServiceFeedbackRef{
		Key:        proxyCompatServiceFeedbackKey(proxyCompatFeedbackScopeNode, nodeTag),
		ScopeKind:  proxyCompatFeedbackScopeNode,
		ScopeValue: strings.TrimSpace(nodeTag),
	})
}

func (state *proxyCompatState) serviceFeedbackForRef(hostID string, ref proxyCompatServiceFeedbackRef) (proxyCompatServiceFeedback, bool) {
	normalizedHostID := normalizeProxyCompatHostID(hostID)
	if normalizedHostID == "" || strings.TrimSpace(ref.Key) == "" {
		return proxyCompatServiceFeedback{}, false
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	state.clearExpiredServiceCooldownsLocked(time.Now())

	byNode, ok := state.serviceFeedback[normalizedHostID]
	if !ok {
		return proxyCompatServiceFeedback{}, false
	}
	feedback, ok := byNode[ref.Key]
	if !ok || feedback == nil {
		return proxyCompatServiceFeedback{}, false
	}
	return *feedback, true
}

func (state *proxyCompatState) recordServiceSuccess(hostID, nodeTag string) {
	state.recordServiceSuccessForRefs(hostID, []proxyCompatServiceFeedbackRef{{
		Key:        proxyCompatServiceFeedbackKey(proxyCompatFeedbackScopeNode, nodeTag),
		ScopeKind:  proxyCompatFeedbackScopeNode,
		ScopeValue: strings.TrimSpace(nodeTag),
	}})
}

func (state *proxyCompatState) recordServiceSuccessForRefs(hostID string, refs []proxyCompatServiceFeedbackRef) {
	normalizedHostID := normalizeProxyCompatHostID(hostID)
	if normalizedHostID == "" || len(refs) == 0 {
		return
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	byNode, ok := state.serviceFeedback[normalizedHostID]
	if !ok {
		return
	}
	for _, ref := range refs {
		if strings.TrimSpace(ref.Key) == "" {
			continue
		}
		delete(byNode, ref.Key)
	}
	if len(byNode) == 0 {
		delete(state.serviceFeedback, normalizedHostID)
	}
}

func (state *proxyCompatState) recordServiceFailure(hostID, nodeTag, errorCode string, decision proxyCompatUsageFeedbackDecision) proxyCompatServiceFeedback {
	return state.recordServiceFailureForRef(hostID, proxyCompatServiceFeedbackRef{
		Key:        proxyCompatServiceFeedbackKey(proxyCompatFeedbackScopeNode, nodeTag),
		ScopeKind:  proxyCompatFeedbackScopeNode,
		ScopeValue: strings.TrimSpace(nodeTag),
	}, errorCode, decision)
}

func (state *proxyCompatState) recordServiceFailureForRef(
	hostID string,
	ref proxyCompatServiceFeedbackRef,
	errorCode string,
	decision proxyCompatUsageFeedbackDecision,
) proxyCompatServiceFeedback {
	normalizedHostID := normalizeProxyCompatHostID(hostID)
	if normalizedHostID == "" || strings.TrimSpace(ref.Key) == "" {
		return proxyCompatServiceFeedback{}
	}

	now := time.Now()
	state.mu.Lock()
	defer state.mu.Unlock()
	state.clearExpiredServiceCooldownsLocked(now)

	byNode, ok := state.serviceFeedback[normalizedHostID]
	if !ok {
		byNode = make(map[string]*proxyCompatServiceFeedback)
		state.serviceFeedback[normalizedHostID] = byNode
	}
	feedback, ok := byNode[ref.Key]
	if !ok || feedback == nil {
		feedback = &proxyCompatServiceFeedback{
			HostID:      normalizedHostID,
			NodeTag:     strings.TrimSpace(ref.ScopeValue),
			FeedbackKey: strings.TrimSpace(ref.Key),
			ScopeKind:   strings.TrimSpace(ref.ScopeKind),
			ScopeValue:  strings.TrimSpace(ref.ScopeValue),
		}
		if feedback.ScopeKind != proxyCompatFeedbackScopeNode {
			feedback.NodeTag = ""
		}
		byNode[ref.Key] = feedback
	}

	consecutiveFailures := 1
	lastReportedAt, _ := time.Parse(time.RFC3339, strings.TrimSpace(feedback.LastReportedAt))
	if feedback.ConsecutiveFailures > 0 &&
		feedback.LastErrorClass == decision.ErrorClass &&
		!lastReportedAt.IsZero() &&
		now.Sub(lastReportedAt) <= 24*time.Hour {
		consecutiveFailures = feedback.ConsecutiveFailures + 1
	}

	feedback.ConsecutiveFailures = consecutiveFailures
	feedback.Penalty = decision.Penalty + max(0, consecutiveFailures-1)*5
	if feedback.Penalty > 95 {
		feedback.Penalty = 95
	}
	feedback.LastErrorClass = decision.ErrorClass
	feedback.LastErrorCode = strings.TrimSpace(errorCode)
	feedback.LastReportedAt = now.Format(time.RFC3339)

	cooldown := decision.CooldownBase
	if decision.EscalateAfterCount > 0 &&
		consecutiveFailures >= decision.EscalateAfterCount &&
		decision.CooldownEscalated > 0 {
		cooldown = decision.CooldownEscalated
	}
	if cooldown > 0 {
		feedback.CooldownUntil = now.Add(cooldown).Format(time.RFC3339)
	} else {
		feedback.CooldownUntil = ""
	}

	return *feedback
}

func (state *proxyCompatState) clearExpiredServiceCooldownsLocked(now time.Time) {
	for hostID, byNode := range state.serviceFeedback {
		for nodeTag, feedback := range byNode {
			if feedback == nil {
				delete(byNode, nodeTag)
				continue
			}
			cooldownUntil, err := time.Parse(time.RFC3339, strings.TrimSpace(feedback.CooldownUntil))
			if err != nil || cooldownUntil.IsZero() || now.After(cooldownUntil) {
				feedback.CooldownUntil = ""
			}
		}
		if len(byNode) == 0 {
			delete(state.serviceFeedback, hostID)
		}
	}
}

func (state *proxyCompatState) serviceFeedbackAggregateForSnapshot(subjectKeys []string, snap Snapshot) (int, bool) {
	refs := proxyCompatServiceFeedbackRefsForSnapshot(snap)
	if len(refs) == 0 {
		return 0, false
	}
	totalPenalty := 0
	anyCooling := false
	now := time.Now()
	seenSubjects := make(map[string]struct{}, len(subjectKeys))
	for _, rawSubjectKey := range subjectKeys {
		subjectKey := normalizeProxyCompatHostID(rawSubjectKey)
		if subjectKey == "" {
			continue
		}
		if _, exists := seenSubjects[subjectKey]; exists {
			continue
		}
		seenSubjects[subjectKey] = struct{}{}
		for _, ref := range refs {
			feedback, ok := state.serviceFeedbackForRef(subjectKey, ref)
			if !ok {
				continue
			}
			totalPenalty += feedback.Penalty
			if cooldownUntil, _ := time.Parse(time.RFC3339, strings.TrimSpace(feedback.CooldownUntil)); !cooldownUntil.IsZero() && cooldownUntil.After(now) {
				anyCooling = true
			}
		}
	}
	if totalPenalty > 95 {
		totalPenalty = 95
	}
	return totalPenalty, anyCooling
}

func (state *proxyCompatState) recordServiceSuccessForSnapshot(hostID string, snap Snapshot) {
	state.recordServiceSuccessForRefs(hostID, proxyCompatServiceFeedbackRefsForSnapshot(snap))
}

func (state *proxyCompatState) recordServiceFailureForSnapshot(
	hostID string,
	snap Snapshot,
	errorCode string,
	decision proxyCompatUsageFeedbackDecision,
) []proxyCompatServiceFeedback {
	refs := proxyCompatServiceFeedbackRefsForSnapshot(snap)
	results := make([]proxyCompatServiceFeedback, 0, len(refs))
	for _, ref := range refs {
		scopedDecision := proxyCompatScopedUsageDecision(ref.ScopeKind, decision)
		if scopedDecision.Scope == proxyCompatUsageFailureNone || scopedDecision.Penalty <= 0 {
			continue
		}
		feedback := state.recordServiceFailureForRef(hostID, ref, errorCode, scopedDecision)
		if strings.TrimSpace(feedback.FeedbackKey) != "" {
			results = append(results, feedback)
		}
	}
	return results
}

func proxyCompatServiceFeedbackRefsForSnapshot(snap Snapshot) []proxyCompatServiceFeedbackRef {
	refs := make([]proxyCompatServiceFeedbackRef, 0, 4)
	if tag := strings.TrimSpace(snap.Tag); tag != "" {
		refs = append(refs, proxyCompatServiceFeedbackRef{
			Key:        proxyCompatServiceFeedbackKey(proxyCompatFeedbackScopeNode, tag),
			ScopeKind:  proxyCompatFeedbackScopeNode,
			ScopeValue: tag,
		})
	}
	if value := strings.TrimSpace(snap.ProtocolFamily); value != "" {
		refs = append(refs, proxyCompatServiceFeedbackRef{
			Key:        proxyCompatServiceFeedbackKey(proxyCompatFeedbackScopeProtocolFamily, value),
			ScopeKind:  proxyCompatFeedbackScopeProtocolFamily,
			ScopeValue: value,
		})
	}
	if value := strings.TrimSpace(snap.NodeMode); value != "" {
		refs = append(refs, proxyCompatServiceFeedbackRef{
			Key:        proxyCompatServiceFeedbackKey(proxyCompatFeedbackScopeNodeMode, value),
			ScopeKind:  proxyCompatFeedbackScopeNodeMode,
			ScopeValue: value,
		})
	}
	if value := strings.TrimSpace(snap.DomainFamily); value != "" {
		refs = append(refs, proxyCompatServiceFeedbackRef{
			Key:        proxyCompatServiceFeedbackKey(proxyCompatFeedbackScopeDomainFamily, value),
			ScopeKind:  proxyCompatFeedbackScopeDomainFamily,
			ScopeValue: value,
		})
	}
	return refs
}

func proxyCompatServiceFeedbackKey(scopeKind string, scopeValue string) string {
	return strings.TrimSpace(scopeKind) + "::" + strings.TrimSpace(scopeValue)
}

func proxyCompatScopedUsageDecision(scopeKind string, decision proxyCompatUsageFeedbackDecision) proxyCompatUsageFeedbackDecision {
	scoped := decision
	switch strings.TrimSpace(scopeKind) {
	case proxyCompatFeedbackScopeNode:
		return scoped
	case proxyCompatFeedbackScopeNodeMode:
		scoped.Penalty = min(55, max(15, decision.Penalty-18))
		scoped.CooldownBase = minDuration(decision.CooldownBase, 8*time.Minute)
		scoped.CooldownEscalated = minDuration(decision.CooldownEscalated, 30*time.Minute)
	case proxyCompatFeedbackScopeDomainFamily:
		scoped.Penalty = min(45, max(12, decision.Penalty-24))
		scoped.CooldownBase = minDuration(decision.CooldownBase, 6*time.Minute)
		scoped.CooldownEscalated = minDuration(decision.CooldownEscalated, 20*time.Minute)
	case proxyCompatFeedbackScopeProtocolFamily:
		scoped.Penalty = min(18, max(4, decision.Penalty/4))
		scoped.CooldownBase = 0
		scoped.CooldownEscalated = 0
	default:
		scoped.Penalty = min(20, max(4, decision.Penalty/4))
		scoped.CooldownBase = 0
		scoped.CooldownEscalated = 0
	}
	return scoped
}

func minDuration(left, right time.Duration) time.Duration {
	switch {
	case left <= 0:
		return right
	case right <= 0:
		return left
	case left < right:
		return left
	default:
		return right
	}
}

func cloneProxyCompatLease(lease proxyCompatLease) proxyCompatLease {
	cloned := lease
	if lease.Metadata != nil {
		cloned.Metadata = make(map[string]string, len(lease.Metadata))
		for key, value := range lease.Metadata {
			cloned.Metadata[key] = value
		}
	}
	return cloned
}

func (s *Server) respondProxyCompatResolveError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errProxyCompatUnsupportedProvider):
		writeProxyCompatError(w, http.StatusServiceUnavailable, "PROVIDER_INSTANCE_UNAVAILABLE", err.Error())
	case errors.Is(err, errProxyCompatNoNodes):
		writeProxyCompatError(w, http.StatusServiceUnavailable, "NO_PROXY_PROVIDER_ROUTE", err.Error())
	default:
		writeProxyCompatError(w, http.StatusInternalServerError, "PROXY_COMPAT_RESOLVE_FAILED", err.Error())
	}
}

func (s *Server) resolveProxyCompatCandidate(r *http.Request, request proxyCompatCheckoutRequest) (proxyCompatCandidate, proxyCompatRuntime, error) {
	if providerType := strings.TrimSpace(strings.ToLower(request.ProviderTypeKey)); providerType != "" && providerType != "easy-proxies" {
		return proxyCompatCandidate{}, proxyCompatRuntime{}, fmt.Errorf("%w: %s", errProxyCompatUnsupportedProvider, providerType)
	}

	runtimeCfg := s.resolveProxyCompatRuntime(r)
	nodes, selectionTier := selectProxyCompatCandidateSnapshots(s.mgr.Snapshot())
	if len(nodes) == 0 {
		return proxyCompatCandidate{}, runtimeCfg, errProxyCompatNoNodes
	}
	serviceKey := normalizeProxyCompatServiceKey(
		firstNonEmptyCompatValue(
			request.Metadata["serviceKey"],
			request.Metadata["service"],
		),
		request.HostID,
	)
	stage := normalizeProxyCompatUsageStage(
		firstNonEmptyCompatValue(
			request.Metadata["stage"],
			request.Metadata["purpose"],
		),
	)
	feedbackSubjectKeys := proxyCompatFeedbackSubjectKeys(
		request.HostID,
		serviceKey,
	)
	recentSuccessPreference := proxyCompatRecentSuccessReusePreferenceFromRequest(request)

	candidates := make([]proxyCompatCandidate, 0, len(nodes))
	preferHistoricalStats := proxyCompatShouldPreferHistoricalSuccessRouting(serviceKey, stage)
	for _, snap := range nodes {
		endpointHost := normalizeCompatEndpointHost(snap.ListenAddress, runtimeCfg.SharedHost)
		endpointPort := int(snap.Port)
		endpointMode := "dedicated-node"
		protocol := runtimeCfg.NodeProtocol
		username := runtimeCfg.NodeUsername
		password := runtimeCfg.NodePassword
		if endpointPort <= 0 {
			if !runtimeCfg.AllowSharedPoolFallback {
				continue
			}
			endpointHost = runtimeCfg.SharedHost
			endpointPort = runtimeCfg.SharedPort
			endpointMode = "shared-pool"
			protocol = runtimeCfg.SharedProtocol
			username = runtimeCfg.SharedUsername
			password = runtimeCfg.SharedPassword
		}
		servicePenalty, serviceCooling := s.compatState().serviceFeedbackAggregateForSnapshot(feedbackSubjectKeys, snap)
		usageStats := proxyCompatUsageStats{}
		if preferHistoricalStats {
			usageStats = s.compatState().businessRiskStats(serviceKey, stage, snap.Tag)
		}
		recentSuccessCount := 0
		recentSuccessStreak := 0
		recentSuccessPenalty := 0
		if recentSuccessPreference.Enabled {
			recentSuccessCount, recentSuccessStreak = s.compatState().recentServiceSuccessStats(
				serviceKey,
				stage,
				snap.Tag,
				recentSuccessPreference.Window,
			)
			recentSuccessPenalty = proxyCompatRecentSuccessReusePenalty(
				recentSuccessCount,
				recentSuccessStreak,
				recentSuccessPreference.Threshold,
			)
		}
		candidates = append(candidates, proxyCompatCandidate{
			Snapshot:             snap,
			ReservationCount:     s.compatState().reservationCount(snap.Tag),
			ServiceLeaseCount:    s.compatState().activeServiceLeaseCount(serviceKey, snap.Tag),
			ServicePenalty:       servicePenalty,
			ServiceCooling:       serviceCooling,
			UsageStats:           usageStats,
			RecentSuccessCount:   recentSuccessCount,
			RecentSuccessStreak:  recentSuccessStreak,
			RecentSuccessPenalty: recentSuccessPenalty,
			SelectionTier:        selectionTier,
			EndpointHost:         endpointHost,
			EndpointPort:         endpointPort,
			Protocol:             protocol,
			Username:             username,
			Password:             password,
			EndpointMode:         endpointMode,
		})
	}
	if len(candidates) == 0 {
		return proxyCompatCandidate{}, runtimeCfg, fmt.Errorf(
			"%w: no EasyProxy candidates expose dedicated listener ports for the active runtime mode",
			errProxyCompatNoNodes,
		)
	}

	eligible := make([]proxyCompatCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ServiceCooling {
			continue
		}
		eligible = append(eligible, candidate)
	}
	if len(eligible) == 0 {
		if selectionTier == "degraded" && proxyCompatRequiresStrictDegradedServiceCooldown(serviceKey, stage) {
			return proxyCompatCandidate{}, runtimeCfg, fmt.Errorf(
				"%w: no degraded EasyProxy nodes are currently available for service %s",
				errProxyCompatNoNodes,
				serviceKey,
			)
		}
		eligible = append(eligible, candidates...)
		for idx := range eligible {
			tier := strings.TrimSpace(eligible[idx].SelectionTier)
			if tier == "" {
				tier = "effective"
			}
			if !strings.Contains(tier, "cooldown-fallback") {
				tier += "-cooldown-fallback"
			}
			eligible[idx].SelectionTier = tier
		}
	}
	if selectionTier == "degraded" {
		spreadEligible := make([]proxyCompatCandidate, 0, len(eligible))
		hadServiceOverlap := false
		for _, candidate := range eligible {
			if candidate.ServiceLeaseCount > 0 {
				hadServiceOverlap = true
				continue
			}
			spreadEligible = append(spreadEligible, candidate)
		}
		if hadServiceOverlap && len(spreadEligible) > 0 {
			eligible = spreadEligible
			for idx := range eligible {
				tier := strings.TrimSpace(eligible[idx].SelectionTier)
				if tier == "" {
					tier = "degraded"
				}
				if !strings.Contains(tier, "service-spread") {
					tier += "-service-spread"
				}
				eligible[idx].SelectionTier = tier
			}
		}
	}

	sort.SliceStable(eligible, func(i, j int) bool {
		left := eligible[i]
		right := eligible[j]
		if left.ServiceLeaseCount != right.ServiceLeaseCount {
			return left.ServiceLeaseCount < right.ServiceLeaseCount
		}
		if left.ServicePenalty != right.ServicePenalty {
			return left.ServicePenalty < right.ServicePenalty
		}
		if recentSuccessPreference.Enabled && left.RecentSuccessPenalty != right.RecentSuccessPenalty {
			return left.RecentSuccessPenalty < right.RecentSuccessPenalty
		}
		if recentSuccessPreference.Enabled && left.RecentSuccessStreak != right.RecentSuccessStreak {
			return left.RecentSuccessStreak < right.RecentSuccessStreak
		}
		if recentSuccessPreference.Enabled && left.RecentSuccessCount != right.RecentSuccessCount {
			return left.RecentSuccessCount < right.RecentSuccessCount
		}
		if preferHistoricalStats {
			leftHistoricalPenalty := proxyCompatHistoricalRegistrationPenalty(left.UsageStats)
			rightHistoricalPenalty := proxyCompatHistoricalRegistrationPenalty(right.UsageStats)
			if leftHistoricalPenalty != rightHistoricalPenalty {
				return leftHistoricalPenalty < rightHistoricalPenalty
			}
			if left.UsageStats.NodeSuccessRate != right.UsageStats.NodeSuccessRate {
				return left.UsageStats.NodeSuccessRate > right.UsageStats.NodeSuccessRate
			}
			if left.UsageStats.NodeSentinelFailureRate != right.UsageStats.NodeSentinelFailureRate {
				return left.UsageStats.NodeSentinelFailureRate < right.UsageStats.NodeSentinelFailureRate
			}
		}
		if left.ReservationCount != right.ReservationCount {
			return left.ReservationCount < right.ReservationCount
		}
		if left.Snapshot.AvailabilityScore != right.Snapshot.AvailabilityScore {
			return left.Snapshot.AvailabilityScore > right.Snapshot.AvailabilityScore
		}
		if left.Snapshot.ActiveConnections != right.Snapshot.ActiveConnections {
			return left.Snapshot.ActiveConnections < right.Snapshot.ActiveConnections
		}
		leftLatency := normalizeCompatLatency(left.Snapshot.LastLatencyMs)
		rightLatency := normalizeCompatLatency(right.Snapshot.LastLatencyMs)
		if leftLatency != rightLatency {
			return leftLatency < rightLatency
		}
		if left.Snapshot.SuccessCount != right.Snapshot.SuccessCount {
			return left.Snapshot.SuccessCount > right.Snapshot.SuccessCount
		}
		if left.Snapshot.FailureCount != right.Snapshot.FailureCount {
			return left.Snapshot.FailureCount < right.Snapshot.FailureCount
		}
		return left.Snapshot.Tag < right.Snapshot.Tag
	})

	return eligible[0], runtimeCfg, nil
}

func selectProxyCompatCandidateSnapshots(nodes []Snapshot) ([]Snapshot, string) {
	effective := filterEffectiveSnapshots(nodes)
	if len(effective) > 0 {
		return effective, "effective"
	}

	degraded := filterCompatFallbackSnapshots(nodes)
	if len(degraded) > 0 {
		return degraded, "degraded"
	}
	return nil, ""
}

func filterCompatFallbackSnapshots(nodes []Snapshot) []Snapshot {
	if len(nodes) == 0 {
		return nil
	}

	nonBlacklisted := make([]Snapshot, 0, len(nodes))
	for _, snap := range nodes {
		if snap.Blacklisted {
			continue
		}
		nonBlacklisted = append(nonBlacklisted, snap)
	}
	if len(nonBlacklisted) > 0 {
		return nonBlacklisted
	}
	return append([]Snapshot(nil), nodes...)
}

func normalizeCompatLatency(value int64) int64 {
	if value <= 0 {
		return 1<<62 - 1
	}
	return value
}

func normalizeCompatEndpointHost(candidate string, fallback string) string {
	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return fallback
	}
	switch trimmed {
	case "0.0.0.0", "::", "[::]", "127.0.0.1", "localhost":
		return fallback
	default:
		return trimmed
	}
}

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

	listenerPort := 22323
	listenerProtocol := "http"
	listenerUsername := ""
	listenerPassword := ""
	nodeProtocol := "http"
	nodeUsername := s.cfg.ProxyUsername
	nodePassword := s.cfg.ProxyPassword
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

	if nodeProtocol == "" {
		nodeProtocol = listenerProtocol
	}
	if nodeUsername == "" && nodePassword == "" {
		nodeUsername = listenerUsername
		nodePassword = listenerPassword
	}

	host := inferCompatRequestHost(r, s.cfg.ExternalIP)
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

func (s *Server) applyProxyCompatUsageFeedback(selectedNodeTag, hostID string, record proxyCompatUsageRecord) {
	trimmedTag := strings.TrimSpace(selectedNodeTag)
	if trimmedTag == "" {
		return
	}

	nodeEntry, err := s.mgr.entry(trimmedTag)
	if err != nil {
		return
	}

	destination := strings.TrimSpace(hostID)
	if destination == "" {
		destination = "compat-report"
	}

	feedbackSubjectKeys := proxyCompatFeedbackSubjectKeys(hostID, record.ServiceKey)
	if record.Success {
		nodeEntry.recordSuccess(destination)
		nodeEntry.applyUsageReportSuccess()
		snapshot := nodeEntry.snapshot()
		for _, subjectKey := range feedbackSubjectKeys {
			s.compatState().recordServiceSuccessForSnapshot(subjectKey, snapshot)
		}
		return
	}

	errMessage := strings.TrimSpace(record.ErrorCode)
	if errMessage == "" {
		errMessage = "task reported proxy route failure"
	}
	failureClass := normalizeProxyCompatFailureClass(record.FailureClass)
	routeConfidence := normalizeProxyCompatRouteConfidence(record.RouteConfidence)
	if failureClass == "" {
		failureClass = proxyCompatFailureClassUnknown
	}
	if routeConfidence == "" {
		routeConfidence = proxyCompatRouteConfidenceLow
	}

	switch failureClass {
	case proxyCompatFailureClassAccountAuth:
		nodeEntry.recordObservationFailure(errors.New(errMessage), destination)
		nodeEntry.applyUsageReportFailure(0, false)
	case proxyCompatFailureClassBusinessRisk:
		nodeEntry.recordObservationFailure(errors.New(errMessage), destination)
		nodeEntry.applyUsageReportFailure(0, false)
		snapshot := nodeEntry.snapshot()
		localDecision := proxyCompatLocalBusinessRiskDecision(errMessage, routeConfidence)
		s.compatState().recordServiceFailureForSnapshot(hostID, snapshot, errMessage, localDecision)

		serviceKey := normalizeProxyCompatServiceKey(record.ServiceKey, hostID)
		stats := s.compatState().businessRiskStats(serviceKey, record.Stage, trimmedTag)
		if proxyCompatShouldCooldownSentinelBusinessRisk(serviceKey, record.Stage, errMessage, stats, routeConfidence) {
			serviceDecision := proxyCompatSentinelBusinessRiskDecision(errMessage, routeConfidence)
			s.compatState().recordServiceFailureForSnapshot(serviceKey, snapshot, errMessage, serviceDecision)
		} else if proxyCompatShouldCooldownBusinessRisk(stats, routeConfidence) {
			serviceDecision := proxyCompatServiceBusinessRiskDecision(errMessage, routeConfidence)
			s.compatState().recordServiceFailureForSnapshot(serviceKey, snapshot, errMessage, serviceDecision)
		}
	case proxyCompatFailureClassRouteFailure:
		serviceKey := normalizeProxyCompatServiceKey(record.ServiceKey, hostID)
		if proxyCompatShouldServiceCooldownLoginBlockedRouteFailure(serviceKey, record.Stage, errMessage, routeConfidence) {
			nodeEntry.recordObservationFailure(errors.New(errMessage), destination)
			nodeEntry.applyUsageReportFailure(0, false)
			snapshot := nodeEntry.snapshot()
			serviceDecision := proxyCompatLoginBlockedServiceRouteFailureDecision(errMessage, routeConfidence)
			s.compatState().recordServiceFailureForSnapshot(serviceKey, snapshot, errMessage, serviceDecision)
			return
		}

		decision := proxyCompatRouteFailureDecision(errMessage, routeConfidence)
		nodeEntry.recordFailure(errors.New(errMessage), destination)
		nodeEntry.applyUsageReportFailure(decision.Penalty, true)
		reportFailures := nodeEntry.snapshot().ConsecutiveReportFailures
		cooldown := decision.CooldownBase
		if decision.EscalateAfterCount > 0 &&
			reportFailures >= decision.EscalateAfterCount &&
			decision.CooldownEscalated > 0 {
			cooldown = decision.CooldownEscalated
		}
		if cooldown > 0 {
			nodeEntry.blacklistUntil(time.Now().Add(cooldown))
		}
		if proxyCompatShouldDirectServiceCooldownRouteFailure(serviceKey, record.Stage, errMessage, routeConfidence) {
			snapshot := nodeEntry.snapshot()
			serviceDecision := proxyCompatDirectServiceRouteFailureDecision(errMessage, routeConfidence)
			s.compatState().recordServiceFailureForSnapshot(serviceKey, snapshot, errMessage, serviceDecision)
		}
	default:
		nodeEntry.recordObservationFailure(errors.New(errMessage), destination)
		nodeEntry.applyUsageReportFailure(0, false)
	}
}

func proxyCompatFeedbackSubjectKeys(hostID, serviceKey string) []string {
	keys := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, raw := range []string{
		normalizeProxyCompatHostID(hostID),
		normalizeProxyCompatServiceKey(serviceKey, hostID),
	} {
		if raw == "" {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		keys = append(keys, raw)
	}
	return keys
}

func proxyCompatRouteFailureDecision(errorCode, routeConfidence string) proxyCompatUsageFeedbackDecision {
	decision := classifyProxyCompatUsageFailure(errorCode)
	if decision.Scope != proxyCompatUsageFailureGlobal {
		decision = proxyCompatUsageFeedbackDecision{
			Scope:              proxyCompatUsageFailureGlobal,
			ErrorClass:         "route:network",
			Penalty:            22,
			CooldownBase:       5 * time.Minute,
			CooldownEscalated:  30 * time.Minute,
			EscalateAfterCount: 3,
		}
	}

	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceLow:
		decision.Penalty = max(8, decision.Penalty-10)
		decision.CooldownBase = minDuration(decision.CooldownBase, 3*time.Minute)
		decision.CooldownEscalated = minDuration(decision.CooldownEscalated, 12*time.Minute)
	case proxyCompatRouteConfidenceMedium:
		decision.Penalty = max(12, decision.Penalty-4)
		decision.CooldownBase = minDuration(decision.CooldownBase, 4*time.Minute)
		decision.CooldownEscalated = minDuration(decision.CooldownEscalated, 20*time.Minute)
	}
	return decision
}

func proxyCompatLocalBusinessRiskDecision(errorCode, routeConfidence string) proxyCompatUsageFeedbackDecision {
	penalty := 10
	baseCooldown := 2 * time.Minute
	escalatedCooldown := 8 * time.Minute
	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceMedium:
		penalty = 14
		baseCooldown = 3 * time.Minute
		escalatedCooldown = 10 * time.Minute
	case proxyCompatRouteConfidenceHigh:
		penalty = 18
		baseCooldown = 4 * time.Minute
		escalatedCooldown = 12 * time.Minute
	}
	return proxyCompatUsageFeedbackDecision{
		Scope:              proxyCompatUsageFailureService,
		ErrorClass:         proxyCompatBusinessRiskErrorClass(errorCode),
		Penalty:            penalty,
		CooldownBase:       baseCooldown,
		CooldownEscalated:  escalatedCooldown,
		EscalateAfterCount: 3,
	}
}

func proxyCompatServiceBusinessRiskDecision(errorCode, routeConfidence string) proxyCompatUsageFeedbackDecision {
	penalty := 18
	baseCooldown := 8 * time.Minute
	escalatedCooldown := 25 * time.Minute
	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceMedium:
		penalty = 24
		baseCooldown = 10 * time.Minute
		escalatedCooldown = 35 * time.Minute
	case proxyCompatRouteConfidenceHigh:
		penalty = 30
		baseCooldown = 12 * time.Minute
		escalatedCooldown = 45 * time.Minute
	}
	return proxyCompatUsageFeedbackDecision{
		Scope:              proxyCompatUsageFailureService,
		ErrorClass:         proxyCompatBusinessRiskErrorClass(errorCode),
		Penalty:            penalty,
		CooldownBase:       baseCooldown,
		CooldownEscalated:  escalatedCooldown,
		EscalateAfterCount: 2,
	}
}

func proxyCompatBusinessRiskErrorClass(errorCode string) string {
	decision := classifyProxyCompatUsageFailure(errorCode)
	if decision.Scope == proxyCompatUsageFailureService && strings.TrimSpace(decision.ErrorClass) != "" {
		return decision.ErrorClass
	}
	return "application:business_risk"
}

func proxyCompatIsRegistrationService(serviceKey string) bool {
	normalized := normalizeProxyCompatServiceKey(serviceKey, "")
	if normalized == "" {
		return false
	}
	for _, prefix := range []string{
		"accio-register",
		"register-service",
		"register-orchestration",
	} {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}
	return false
}

func proxyCompatShouldPreferHistoricalSuccessRouting(serviceKey, stage string) bool {
	stage = normalizeProxyCompatUsageStage(stage)
	return stage == "registration" && proxyCompatIsRegistrationService(serviceKey)
}

func proxyCompatHistoricalRegistrationPenalty(stats proxyCompatUsageStats) float64 {
	if stats.NodeTotal <= 0 {
		return 5
	}
	penalty := (1 - stats.NodeSuccessRate) * 25
	penalty += stats.NodeSentinelFailureRate * 60
	if stats.NodeSentinelFailures >= 2 {
		penalty += 15
	}
	delta := stats.NodeSentinelFailureRate - stats.ServiceSentinelFailureRate
	if delta > 0 {
		penalty += delta * 50
	}
	if stats.NodeTotal >= 3 && stats.NodeSuccessRate <= 0.20 {
		penalty += 12
	}
	return penalty
}

func proxyCompatRecentSuccessReusePreferenceFromRequest(request proxyCompatCheckoutRequest) proxyCompatRecentSuccessReusePreference {
	enabled := proxyCompatMetadataBool(
		request.Metadata,
		"avoidRecentSuccessReuse",
		"preferFreshNodeAfterSuccess",
	)
	threshold := proxyCompatMetadataPositiveInt(
		request.Metadata,
		2,
		"recentSuccessReuseThreshold",
		"recentSuccessThreshold",
	)
	windowMinutes := proxyCompatMetadataPositiveInt(
		request.Metadata,
		20,
		"recentSuccessReuseWindowMinutes",
		"recentSuccessWindowMinutes",
	)
	return proxyCompatRecentSuccessReusePreference{
		Enabled:   enabled,
		Threshold: threshold,
		Window:    time.Duration(windowMinutes) * time.Minute,
	}
}

func proxyCompatRecentSuccessReusePenalty(successCount, successStreak, threshold int) int {
	if successCount <= 0 || successStreak <= 0 {
		return 0
	}
	if threshold <= 0 {
		threshold = 1
	}

	penalty := min(successCount, 3) * 6
	if successStreak >= threshold {
		penalty += 24 + (successStreak-threshold)*14
	}
	return penalty
}

func proxyCompatShouldCooldownBusinessRisk(stats proxyCompatUsageStats, routeConfidence string) bool {
	if stats.ServiceTotal < 10 || stats.NodeTotal < 5 || stats.NodeFailures < 4 {
		return false
	}
	delta := stats.NodeFailureRate - stats.ServiceFailureRate
	if stats.ServiceFailureRate <= 0.25 && stats.NodeFailures >= 5 && stats.NodeSuccesses == 0 {
		return true
	}

	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceHigh:
		return stats.NodeFailureRate >= 0.60 && delta >= 0.25
	case proxyCompatRouteConfidenceMedium:
		return stats.NodeFailureRate >= 0.70 && delta >= 0.35
	default:
		return stats.NodeFailureRate >= 0.85 && delta >= 0.45
	}
}

func proxyCompatShouldCooldownSentinelBusinessRisk(serviceKey, stage, errorCode string, stats proxyCompatUsageStats, routeConfidence string) bool {
	if !proxyCompatShouldPreferHistoricalSuccessRouting(serviceKey, stage) {
		return false
	}
	if !proxyCompatCountsAsSentinelRateLimit(errorCode) {
		return false
	}
	if stats.ServiceTotal < 6 || stats.NodeTotal < 3 || stats.NodeSentinelFailures < 2 {
		return false
	}
	delta := stats.NodeSentinelFailureRate - stats.ServiceSentinelFailureRate
	if stats.ServiceSentinelFailureRate <= 0.15 && stats.NodeSentinelFailures >= 2 && stats.NodeSuccesses == 0 {
		return true
	}
	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceHigh:
		return stats.NodeSentinelFailureRate >= 0.40 && delta >= 0.12
	case proxyCompatRouteConfidenceMedium:
		return stats.NodeSentinelFailureRate >= 0.45 && delta >= 0.15
	default:
		return stats.NodeSentinelFailureRate >= 0.55 && delta >= 0.20
	}
}

func proxyCompatSentinelBusinessRiskDecision(errorCode, routeConfidence string) proxyCompatUsageFeedbackDecision {
	penalty := 30
	baseCooldown := 12 * time.Minute
	escalatedCooldown := 40 * time.Minute
	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceMedium:
		penalty = 34
		baseCooldown = 15 * time.Minute
		escalatedCooldown = 50 * time.Minute
	case proxyCompatRouteConfidenceHigh:
		penalty = 40
		baseCooldown = 18 * time.Minute
		escalatedCooldown = 60 * time.Minute
	}
	return proxyCompatUsageFeedbackDecision{
		Scope:              proxyCompatUsageFailureService,
		ErrorClass:         "application:sentinel_hotspot",
		Penalty:            penalty,
		CooldownBase:       baseCooldown,
		CooldownEscalated:  escalatedCooldown,
		EscalateAfterCount: 2,
	}
}

func proxyCompatRequiresStrictDegradedServiceCooldown(serviceKey, stage string) bool {
	stage = normalizeProxyCompatUsageStage(stage)
	return stage == "registration" && proxyCompatIsRegistrationService(serviceKey)
}

func proxyCompatShouldDirectServiceCooldownRouteFailure(serviceKey, stage, errorCode, routeConfidence string) bool {
	if !proxyCompatRequiresStrictDegradedServiceCooldown(serviceKey, stage) {
		return false
	}
	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceHigh, proxyCompatRouteConfidenceMedium:
	default:
		return false
	}

	normalized := strings.ToLower(strings.TrimSpace(errorCode))
	if normalized == "" {
		return false
	}
	tripMarkers := []string{
		"net::err_connection_closed",
		"err_connection_closed",
		"remote end closed",
		"unexpected eof",
		"connection reset",
		"econnreset",
	}
	for _, marker := range tripMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func proxyCompatDirectServiceRouteFailureDecision(errorCode, routeConfidence string) proxyCompatUsageFeedbackDecision {
	penalty := 36
	baseCooldown := 10 * time.Minute
	escalatedCooldown := 30 * time.Minute
	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceMedium:
		penalty = 28
		baseCooldown = 8 * time.Minute
		escalatedCooldown = 20 * time.Minute
	}
	return proxyCompatUsageFeedbackDecision{
		Scope:              proxyCompatUsageFailureService,
		ErrorClass:         "route:registration_close",
		Penalty:            penalty,
		CooldownBase:       baseCooldown,
		CooldownEscalated:  escalatedCooldown,
		EscalateAfterCount: 2,
	}
}

func proxyCompatShouldServiceCooldownLoginBlockedRouteFailure(serviceKey, stage, errorCode, routeConfidence string) bool {
	if !proxyCompatRequiresStrictDegradedServiceCooldown(serviceKey, stage) {
		return false
	}
	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceHigh, proxyCompatRouteConfidenceMedium:
	default:
		return false
	}

	normalized := strings.ToLower(strings.TrimSpace(errorCode))
	if normalized == "" {
		return false
	}
	loginHosts := []string{
		"platform.openai.com/login",
		"auth.openai.com",
		"chatgpt.com/auth/login_with",
		"chatgpt.com/auth/login",
	}
	matchedHost := false
	for _, host := range loginHosts {
		if strings.Contains(normalized, host) {
			matchedHost = true
			break
		}
	}
	if !matchedHost {
		return false
	}

	blockMarkers := []string{
		"proxy route failure blocked",
		"easy_proxy_probe_failed",
		"just a moment",
		"challenge=yes",
		"status=403",
	}
	for _, marker := range blockMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func proxyCompatLoginBlockedServiceRouteFailureDecision(errorCode, routeConfidence string) proxyCompatUsageFeedbackDecision {
	penalty := 30
	baseCooldown := 8 * time.Minute
	escalatedCooldown := 25 * time.Minute
	switch normalizeProxyCompatRouteConfidence(routeConfidence) {
	case proxyCompatRouteConfidenceMedium:
		penalty = 24
		baseCooldown = 6 * time.Minute
		escalatedCooldown = 18 * time.Minute
	}
	return proxyCompatUsageFeedbackDecision{
		Scope:              proxyCompatUsageFailureService,
		ErrorClass:         "route:login_blocked",
		Penalty:            penalty,
		CooldownBase:       baseCooldown,
		CooldownEscalated:  escalatedCooldown,
		EscalateAfterCount: 2,
	}
}

func classifyProxyCompatUsageFailure(errorCode string) proxyCompatUsageFeedbackDecision {
	normalized := strings.ToLower(strings.TrimSpace(errorCode))
	if normalized == "" {
		return proxyCompatUsageFeedbackDecision{
			Scope:      proxyCompatUsageFailureGlobal,
			ErrorClass: "route:unknown",
			Penalty:    15,
		}
	}

	authMarkers := []string{
		"401",
		"403",
		"502",
		"not_login",
		"not login",
		"auth not pass",
		"auth failed",
		"invalid token",
		"quota",
	}
	for _, marker := range authMarkers {
		if strings.Contains(normalized, marker) {
			return proxyCompatUsageFeedbackDecision{
				Scope:      proxyCompatUsageFailureNone,
				ErrorClass: "application:auth",
			}
		}
	}

	type markerRule struct {
		marker   string
		decision proxyCompatUsageFeedbackDecision
	}
	riskMarkers := []markerRule{
		{
			marker: "eudf5",
			decision: proxyCompatUsageFeedbackDecision{
				Scope:              proxyCompatUsageFailureService,
				ErrorClass:         "application:eudf5",
				Penalty:            70,
				CooldownBase:       20 * time.Minute,
				CooldownEscalated:  2 * time.Hour,
				EscalateAfterCount: 2,
			},
		},
		{
			marker: "eudf",
			decision: proxyCompatUsageFeedbackDecision{
				Scope:              proxyCompatUsageFailureService,
				ErrorClass:         "application:eudf",
				Penalty:            65,
				CooldownBase:       20 * time.Minute,
				CooldownEscalated:  90 * time.Minute,
				EscalateAfterCount: 2,
			},
		},
		{
			marker: "unusual traffic",
			decision: proxyCompatUsageFeedbackDecision{
				Scope:              proxyCompatUsageFailureService,
				ErrorClass:         "application:unusual_traffic",
				Penalty:            60,
				CooldownBase:       20 * time.Minute,
				CooldownEscalated:  90 * time.Minute,
				EscalateAfterCount: 2,
			},
		},
		{
			marker: "sentinel rate limit",
			decision: proxyCompatUsageFeedbackDecision{
				Scope:              proxyCompatUsageFailureService,
				ErrorClass:         "application:sentinel_rate_limit",
				Penalty:            60,
				CooldownBase:       20 * time.Minute,
				CooldownEscalated:  90 * time.Minute,
				EscalateAfterCount: 2,
			},
		},
		{
			marker: "slider_failed",
			decision: proxyCompatUsageFeedbackDecision{
				Scope:              proxyCompatUsageFailureService,
				ErrorClass:         "application:slider_failed",
				Penalty:            55,
				CooldownBase:       12 * time.Minute,
				CooldownEscalated:  45 * time.Minute,
				EscalateAfterCount: 2,
			},
		},
		{
			marker: "email_submit_not_accepted",
			decision: proxyCompatUsageFeedbackDecision{
				Scope:              proxyCompatUsageFailureService,
				ErrorClass:         "application:email_submit_not_accepted",
				Penalty:            50,
				CooldownBase:       12 * time.Minute,
				CooldownEscalated:  45 * time.Minute,
				EscalateAfterCount: 2,
			},
		},
		{
			marker: "captcha",
			decision: proxyCompatUsageFeedbackDecision{
				Scope:              proxyCompatUsageFailureService,
				ErrorClass:         "application:captcha",
				Penalty:            50,
				CooldownBase:       10 * time.Minute,
				CooldownEscalated:  30 * time.Minute,
				EscalateAfterCount: 2,
			},
		},
	}
	for _, rule := range riskMarkers {
		if strings.Contains(normalized, rule.marker) {
			return rule.decision
		}
	}

	networkMarkers := []string{
		"timeout",
		"tls",
		"connection reset",
		"proxy route failure",
		"connection refused",
		"econnreset",
		"network unreachable",
		"dial tcp",
		"i/o timeout",
	}
	for _, marker := range networkMarkers {
		if strings.Contains(normalized, marker) {
			return proxyCompatUsageFeedbackDecision{
				Scope:              proxyCompatUsageFailureGlobal,
				ErrorClass:         "route:network",
				Penalty:            25,
				CooldownBase:       5 * time.Minute,
				CooldownEscalated:  30 * time.Minute,
				EscalateAfterCount: 3,
			}
		}
	}

	return proxyCompatUsageFeedbackDecision{
		Scope:      proxyCompatUsageFailureGlobal,
		ErrorClass: "route:generic",
		Penalty:    15,
	}
}

func proxyCompatAverageAvailabilityScore(nodes []Snapshot) int {
	if len(nodes) == 0 {
		return 0
	}
	total := 0
	for _, node := range nodes {
		total += node.AvailabilityScore
	}
	return total / len(nodes)
}

func inferCompatRequestScheme(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		first := strings.ToLower(strings.TrimSpace(strings.Split(forwarded, ",")[0]))
		if first == "https" {
			return "https"
		}
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func inferCompatRequestHost(r *http.Request, externalIP string) string {
	host := strings.TrimSpace(r.Host)
	if host != "" {
		if parsedHost, _, err := net.SplitHostPort(host); err == nil {
			host = parsedHost
		}
	}
	if host == "" || isCompatLoopbackOrWildcard(host) {
		if trimmedExternal := strings.TrimSpace(externalIP); trimmedExternal != "" {
			host = trimmedExternal
		}
	}
	if host == "" || isCompatLoopbackOrWildcard(host) {
		host = "127.0.0.1"
	}
	return host
}

func isCompatLoopbackOrWildcard(host string) bool {
	switch strings.Trim(strings.TrimSpace(host), "[]") {
	case "", "0.0.0.0", "127.0.0.1", "::", "::1", "localhost":
		return true
	default:
		return false
	}
}

func parseCompatPort(listen string) int {
	trimmed := strings.TrimSpace(listen)
	if trimmed == "" {
		return 0
	}
	if _, port, err := net.SplitHostPort(trimmed); err == nil {
		if parsed, parseErr := strconv.Atoi(port); parseErr == nil && parsed > 0 {
			return parsed
		}
	}
	if parsed, err := strconv.Atoi(trimmed); err == nil && parsed > 0 {
		return parsed
	}
	return 0
}

func normalizeProxyCompatHostID(hostID string) string {
	return strings.ToLower(strings.TrimSpace(hostID))
}

func normalizeProxyCompatServiceKey(serviceKey string, hostID string) string {
	normalized := strings.ToLower(strings.TrimSpace(serviceKey))
	if normalized != "" {
		return normalized
	}
	return normalizeProxyCompatHostID(hostID)
}

func normalizeProxyCompatUsageStage(stage string) string {
	normalized := strings.ToLower(strings.TrimSpace(stage))
	if normalized == "" {
		return "request"
	}
	return normalized
}

func normalizeProxyCompatFailureClass(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case proxyCompatFailureClassNone:
		return proxyCompatFailureClassNone
	case proxyCompatFailureClassRouteFailure:
		return proxyCompatFailureClassRouteFailure
	case proxyCompatFailureClassBusinessRisk:
		return proxyCompatFailureClassBusinessRisk
	case proxyCompatFailureClassAccountAuth:
		return proxyCompatFailureClassAccountAuth
	case proxyCompatFailureClassUnknown:
		return proxyCompatFailureClassUnknown
	default:
		return ""
	}
}

func normalizeProxyCompatRouteConfidence(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case proxyCompatRouteConfidenceLow:
		return proxyCompatRouteConfidenceLow
	case proxyCompatRouteConfidenceMedium:
		return proxyCompatRouteConfidenceMedium
	case proxyCompatRouteConfidenceHigh:
		return proxyCompatRouteConfidenceHigh
	default:
		return ""
	}
}

func proxyCompatMetadataBool(metadata map[string]string, keys ...string) bool {
	for _, key := range keys {
		value := strings.ToLower(strings.TrimSpace(metadata[key]))
		switch value {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return false
}

func proxyCompatMetadataPositiveInt(metadata map[string]string, fallback int, keys ...string) int {
	for _, key := range keys {
		value := strings.TrimSpace(metadata[key])
		if value == "" {
			continue
		}
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			continue
		}
		return parsed
	}
	return fallback
}

func inferProxyCompatFailureSemantics(errorCode string) (string, string) {
	normalized := strings.ToLower(strings.TrimSpace(errorCode))
	if normalized == "" {
		return proxyCompatFailureClassUnknown, proxyCompatRouteConfidenceLow
	}

	authMarkers := []string{
		"401",
		"403",
		"502",
		"not_login",
		"not login",
		"auth not pass",
		"auth failed",
		"invalid token",
		"quota_empty",
		"quota empty",
		"quota=0",
		"0/520",
	}
	for _, marker := range authMarkers {
		if strings.Contains(normalized, marker) {
			return proxyCompatFailureClassAccountAuth, proxyCompatRouteConfidenceLow
		}
	}

	mediumRiskMarkers := []string{
		"eudf5",
		"eudf",
		"unusual traffic",
		"sentinel rate limit",
	}
	for _, marker := range mediumRiskMarkers {
		if strings.Contains(normalized, marker) {
			return proxyCompatFailureClassBusinessRisk, proxyCompatRouteConfidenceMedium
		}
	}

	lowRiskMarkers := []string{
		"slider_failed",
		"email_submit_not_accepted",
		"captcha",
		"otp_timeout",
		"import_activation_failed",
		"import_not_login",
		"import_quota_unavailable",
		"import_callback_failed",
		"timeoutexception",
	}
	for _, marker := range lowRiskMarkers {
		if strings.Contains(normalized, marker) {
			return proxyCompatFailureClassBusinessRisk, proxyCompatRouteConfidenceLow
		}
	}

	networkMarkers := []string{
		"net::err_connection_closed",
		"err_connection_closed",
		"net::err_proxy_connection_failed",
		"err_proxy_connection_failed",
		"net::err_tunnel_connection_failed",
		"err_tunnel_connection_failed",
		"timeout",
		"tls",
		"connection reset",
		"proxy route failure",
		"connection refused",
		"econnreset",
		"network unreachable",
		"dial tcp",
		"i/o timeout",
		"unable to connect to proxy",
		"remote end closed",
		"unexpected eof",
		"read timed out",
	}
	for _, marker := range networkMarkers {
		if strings.Contains(normalized, marker) {
			return proxyCompatFailureClassRouteFailure, proxyCompatRouteConfidenceHigh
		}
	}

	return proxyCompatFailureClassUnknown, proxyCompatRouteConfidenceLow
}

func firstNonEmptyCompatValue(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func writeProxyCompatError(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	writeJSON(w, map[string]any{
		"error":   code,
		"message": message,
	})
}
