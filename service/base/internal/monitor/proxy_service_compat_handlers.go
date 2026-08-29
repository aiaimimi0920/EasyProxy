package monitor

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

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
	if err := s.waitForCompatCheckoutReady(); err != nil {
		writeProxyCompatError(w, http.StatusServiceUnavailable, "INITIAL_PROXY_PROBE_PENDING", err.Error())
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
	if err := s.waitForCompatCheckoutReady(); err != nil {
		writeProxyCompatError(w, http.StatusServiceUnavailable, "INITIAL_PROXY_PROBE_PENDING", err.Error())
		return
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

func (s *Server) waitForCompatCheckoutReady() error {
	if s.mgr == nil {
		return nil
	}
	snapshots := s.mgr.Snapshot()
	sourceStates := s.mgr.SourceSelectionStates()
	secondaryStates := s.mgr.SecondarySelectionStates()
	if nodes, _ := selectProxyCompatCandidateSnapshotsWithSelection(snapshots, sourceStates, secondaryStates); len(nodes) > 0 {
		return nil
	}
	if err := s.mgr.WaitForInitialProbe(0); err != nil {
		snapshots = s.mgr.Snapshot()
		sourceStates = s.mgr.SourceSelectionStates()
		secondaryStates = s.mgr.SecondarySelectionStates()
		nodes, _ := selectProxyCompatCandidateSnapshotsWithSelection(snapshots, sourceStates, secondaryStates)
		if len(nodes) > 0 {
			return nil
		}
		return err
	}
	return nil
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
