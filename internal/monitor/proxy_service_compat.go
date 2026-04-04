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
	LeaseID    string `json:"leaseId"`
	Success    bool   `json:"success"`
	LatencyMs  int64  `json:"latencyMs,omitempty"`
	ErrorCode  string `json:"errorCode,omitempty"`
	ReportedAt string `json:"reportedAt,omitempty"`
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
}

type proxyCompatState struct {
	mu               sync.RWMutex
	leases           map[string]*proxyCompatLeaseState
	usageRecords     []proxyCompatUsageRecord
	nodeReservations map[string]int
}

type proxyCompatLeaseState struct {
	Lease           proxyCompatLease
	SelectedNodeTag string
}

type proxyCompatRuntime struct {
	SharedHost          string
	SharedPort          int
	SharedProtocol      string
	SharedUsername      string
	SharedPassword      string
	NodeProtocol        string
	NodeUsername        string
	NodePassword        string
	ManagementPort      int
	ConnectionRef       string
	ManagementURL       string
	ProviderInstanceID  string
	ProviderDisplayName string
	CreatedAt           string
	UpdatedAt           string
}

type proxyCompatCandidate struct {
	Snapshot         Snapshot
	ReservationCount int
	EndpointHost     string
	EndpointPort     int
	Protocol         string
	Username         string
	Password         string
	EndpointMode     string
}

type proxyCompatMaintenanceResult struct {
	expired []string
	cleaned []string
}

var (
	errProxyCompatUnsupportedProvider = errors.New("requested provider is not supported by the EasyProxy compatibility layer")
	errProxyCompatNoNodes             = errors.New("no effective EasyProxy nodes are currently available")
)

func newProxyCompatState() *proxyCompatState {
	return &proxyCompatState{
		leases:           make(map[string]*proxyCompatLeaseState),
		usageRecords:     make([]proxyCompatUsageRecord, 0, 64),
		nodeReservations: make(map[string]int),
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
	leases, usage := s.compatState().snapshot()
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
		},
	})
}

func (s *Server) handleProxyPlanCheckout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
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
	s.applyProxyCompatUsageFeedback(selectedNodeTag, hostID, report)
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
	const keepUsageRecords = 256
	if len(state.usageRecords) > keepUsageRecords {
		dropCount := len(state.usageRecords) - keepUsageRecords
		for idx := 0; idx < dropCount; idx++ {
			cleaned = append(cleaned, state.usageRecords[idx].ID)
		}
		state.usageRecords = append([]proxyCompatUsageRecord(nil), state.usageRecords[dropCount:]...)
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

	state.usageRecords = append(state.usageRecords, record)
	if len(state.usageRecords) > 256 {
		state.usageRecords = append([]proxyCompatUsageRecord(nil), state.usageRecords[len(state.usageRecords)-256:]...)
	}
	return record, leaseState.SelectedNodeTag, leaseState.Lease.HostID, nil
}

func (state *proxyCompatState) snapshot() ([]proxyCompatLease, []proxyCompatUsageRecord) {
	state.mu.RLock()
	defer state.mu.RUnlock()

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
	return leases, usage
}

func (state *proxyCompatState) reservationCount(nodeTag string) int {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.nodeReservations[nodeTag]
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
	nodes := filterEffectiveSnapshots(s.mgr.Snapshot())
	if len(nodes) == 0 {
		return proxyCompatCandidate{}, runtimeCfg, errProxyCompatNoNodes
	}

	candidates := make([]proxyCompatCandidate, 0, len(nodes))
	for _, snap := range nodes {
		endpointHost := normalizeCompatEndpointHost(snap.ListenAddress, runtimeCfg.SharedHost)
		endpointPort := int(snap.Port)
		endpointMode := "dedicated-node"
		protocol := runtimeCfg.NodeProtocol
		username := runtimeCfg.NodeUsername
		password := runtimeCfg.NodePassword
		if endpointPort <= 0 {
			endpointHost = runtimeCfg.SharedHost
			endpointPort = runtimeCfg.SharedPort
			endpointMode = "shared-pool"
			protocol = runtimeCfg.SharedProtocol
			username = runtimeCfg.SharedUsername
			password = runtimeCfg.SharedPassword
		}
		candidates = append(candidates, proxyCompatCandidate{
			Snapshot:         snap,
			ReservationCount: s.compatState().reservationCount(snap.Tag),
			EndpointHost:     endpointHost,
			EndpointPort:     endpointPort,
			Protocol:         protocol,
			Username:         username,
			Password:         password,
			EndpointMode:     endpointMode,
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left := candidates[i]
		right := candidates[j]
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

	return candidates[0], runtimeCfg, nil
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
			"selectedNodeTag":     candidate.Snapshot.Tag,
			"selectedNodeName":    candidate.Snapshot.Name,
			"selectedNodePort":    strconv.Itoa(candidate.EndpointPort),
			"selectedNodeMode":    candidate.EndpointMode,
			"selectedNodeRegion":  candidate.Snapshot.Region,
			"selectedNodeCountry": candidate.Snapshot.Country,
			"managementUrl":       runtimeCfg.ManagementURL,
			"connectionRef":       runtimeCfg.ConnectionRef,
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

	listenerPort := 2323
	listenerProtocol := "http"
	listenerUsername := ""
	listenerPassword := ""
	nodeProtocol := "http"
	nodeUsername := s.cfg.ProxyUsername
	nodePassword := s.cfg.ProxyPassword
	managementPort := 9888
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
		SharedHost:          host,
		SharedPort:          listenerPort,
		SharedProtocol:      listenerProtocol,
		SharedUsername:      listenerUsername,
		SharedPassword:      listenerPassword,
		NodeProtocol:        nodeProtocol,
		NodeUsername:        nodeUsername,
		NodePassword:        nodePassword,
		ManagementPort:      managementPort,
		ConnectionRef:       fmt.Sprintf("%s://%s:%d", refScheme, host, managementPort),
		ManagementURL:       fmt.Sprintf("%s://%s:%d", scheme, host, managementPort),
		ProviderInstanceID:  "easyproxy-default",
		ProviderDisplayName: "EasyProxy Default Instance",
		CreatedAt:           createdAt,
		UpdatedAt:           time.Now().Format(time.RFC3339),
	}
}

func (s *Server) applyProxyCompatUsageFeedback(selectedNodeTag, hostID string, report proxyCompatUsageReport) {
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

	if report.Success {
		nodeEntry.recordSuccess(destination)
		nodeEntry.applyUsageReportSuccess()
		return
	}

	errMessage := strings.TrimSpace(report.ErrorCode)
	if errMessage == "" {
		errMessage = "task reported proxy route failure"
	}

	nodeEntry.recordFailure(errors.New(errMessage), destination)
	nodeEntry.applyUsageReportFailure()
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

func writeProxyCompatError(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	writeJSON(w, map[string]any{
		"error":   code,
		"message": message,
	})
}
