package monitor

import (
	"sort"
	"strings"
	"time"
)

func (m *Manager) Snapshot() []Snapshot {
	return m.SnapshotFiltered(false)
}

// SnapshotFiltered returns a sorted copy of current node states.
// If onlyAvailable is true, it keeps only nodes that are effectively usable:
// either probe-confirmed available or recently proven by successful traffic.
func (m *Manager) SnapshotFiltered(onlyAvailable bool) []Snapshot {
	m.mu.RLock()
	list := make([]*entry, 0, len(m.nodes))
	for _, e := range m.nodes {
		list = append(list, e)
	}
	m.mu.RUnlock()
	snapshots := make([]Snapshot, 0, len(list))
	for _, e := range list {
		snap := e.snapshot()
		if onlyAvailable && !isEffectiveSnapshot(snap) {
			continue
		}
		snapshots = append(snapshots, snap)
	}
	// 按延迟排序（延迟小的在前面，未测试的排在最后）
	sort.Slice(snapshots, func(i, j int) bool {
		latencyI := snapshots[i].LastLatencyMs
		latencyJ := snapshots[j].LastLatencyMs
		// -1 表示未测试，排在最后
		if latencyI < 0 && latencyJ < 0 {
			return snapshots[i].Name < snapshots[j].Name // 都未测试时按名称排序
		}
		if latencyI < 0 {
			return false // i 未测试，排在后面
		}
		if latencyJ < 0 {
			return true // j 未测试，i 排在前面
		}
		if latencyI == latencyJ {
			return snapshots[i].Name < snapshots[j].Name // 延迟相同时按名称排序
		}
		return latencyI < latencyJ
	})
	return snapshots
}

// SourceSelectionStates aggregates recent node health into source-level
// selection hints so pool schedulers can avoid sources with systemic
// handshake/auth failures without permanently removing every node.
func (m *Manager) SourceSelectionStates() map[string]SourceSelectionState {
	m.mu.RLock()
	list := make([]*entry, 0, len(m.nodes))
	for _, e := range m.nodes {
		list = append(list, e)
	}
	m.mu.RUnlock()

	grouped := make(map[string]*SourceSelectionState)
	for _, e := range list {
		snap := e.snapshot()
		sourceRef := strings.TrimSpace(snap.SourceRef)
		if sourceRef == "" {
			continue
		}
		state, ok := grouped[sourceRef]
		if !ok {
			state = &SourceSelectionState{
				Ref:  sourceRef,
				Name: strings.TrimSpace(snap.SourceName),
				Kind: strings.TrimSpace(snap.SourceKind),
			}
			grouped[sourceRef] = state
		}
		state.TotalNodes++
		if isEffectiveSnapshot(snap) {
			state.HealthyNodes++
		}
		if structural, reason := classifySourceStructuralFailure(snap); structural {
			state.StructuralFailures++
			if state.Reason == "" {
				state.Reason = reason
			}
		}
	}

	result := make(map[string]SourceSelectionState, len(grouped))
	for ref, state := range grouped {
		applySourceSelectionPenalty(state)
		result[ref] = *state
	}
	return result
}

// SourceHealthStates aggregates runtime availability by source_ref so operator
// tooling can answer questions like total/effective/blacklisted/pending counts
// for manifest-fed providers such as ZenProxy.
func (m *Manager) SourceHealthStates() map[string]SourceHealthState {
	m.mu.RLock()
	list := make([]*entry, 0, len(m.nodes))
	for _, e := range m.nodes {
		list = append(list, e)
	}
	m.mu.RUnlock()

	grouped := make(map[string]*SourceHealthState)
	for _, e := range list {
		snap := e.snapshot()
		sourceRef := strings.TrimSpace(snap.SourceRef)
		if sourceRef == "" {
			continue
		}
		state, ok := grouped[sourceRef]
		if !ok {
			state = &SourceHealthState{
				Ref:  sourceRef,
				Name: strings.TrimSpace(snap.SourceName),
				Kind: strings.TrimSpace(snap.SourceKind),
			}
			grouped[sourceRef] = state
		}

		state.TotalNodes++
		if snap.EffectiveAvailable {
			state.EffectiveAvailableNodes++
		}
		if isProbeAvailable(snap) {
			state.ProbeAvailableNodes++
		}
		if snap.TrafficProvenUsable {
			state.TrafficProvenNodes++
		}
		if snap.Blacklisted {
			state.BlacklistedNodes++
		}
		if !snap.InitialCheckDone {
			state.PendingNodes++
		} else if !snap.EffectiveAvailable && !snap.Blacklisted {
			state.UnavailableNodes++
		}
		if structural, reason := classifySourceStructuralFailure(snap); structural {
			state.StructuralFailures++
			if state.SelectionReason == "" {
				state.SelectionReason = reason
			}
		}
	}

	result := make(map[string]SourceHealthState, len(grouped))
	for ref, state := range grouped {
		selection := &SourceSelectionState{
			Ref:                state.Ref,
			Name:               state.Name,
			Kind:               state.Kind,
			TotalNodes:         state.TotalNodes,
			HealthyNodes:       state.EffectiveAvailableNodes,
			StructuralFailures: state.StructuralFailures,
			Reason:             state.SelectionReason,
		}
		applySourceSelectionPenalty(selection)
		state.SelectionPenalty = selection.Penalty
		state.SelectionExcluded = selection.Excluded
		state.SelectionReason = selection.Reason
		result[ref] = *state
	}
	return result
}

// SecondarySelectionStates aggregates source-internal node features such as
// protocol family, node mode, and domain family so the pool can degrade
// recurrently bad clusters without excluding an otherwise healthy source.
func (m *Manager) SecondarySelectionStates() map[string]SecondarySelectionState {
	m.mu.RLock()
	list := make([]*entry, 0, len(m.nodes))
	for _, e := range m.nodes {
		list = append(list, e)
	}
	m.mu.RUnlock()

	grouped := make(map[string]*SecondarySelectionState)
	for _, e := range list {
		snap := e.snapshot()
		sourceRef := strings.TrimSpace(snap.SourceRef)
		if sourceRef == "" {
			continue
		}
		structural, reason := classifySourceStructuralFailure(snap)
		dimensions := []struct {
			name  string
			value string
		}{
			{name: SelectionDimensionProtocolFamily, value: strings.TrimSpace(snap.ProtocolFamily)},
			{name: SelectionDimensionNodeMode, value: strings.TrimSpace(snap.NodeMode)},
			{name: SelectionDimensionDomainFamily, value: strings.TrimSpace(snap.DomainFamily)},
		}
		for _, dimension := range dimensions {
			if dimension.value == "" {
				continue
			}
			key := SecondarySelectionStateKey(sourceRef, dimension.name, dimension.value)
			state, ok := grouped[key]
			if !ok {
				state = &SecondarySelectionState{
					Key:        key,
					SourceRef:  sourceRef,
					SourceName: strings.TrimSpace(snap.SourceName),
					SourceKind: strings.TrimSpace(snap.SourceKind),
					Dimension:  dimension.name,
					Value:      dimension.value,
				}
				grouped[key] = state
			}
			state.TotalNodes++
			if isEffectiveSnapshot(snap) {
				state.HealthyNodes++
			}
			if structural {
				state.StructuralFailures++
				if state.Reason == "" {
					state.Reason = reason
				}
			}
		}
	}

	result := make(map[string]SecondarySelectionState, len(grouped))
	for key, state := range grouped {
		applySecondarySelectionPenalty(state)
		result[key] = *state
	}
	return result
}

// TrafficSummary returns aggregated traffic totals/speeds and per-node speeds.
// includeNodes controls whether per-node details are returned.
func (m *Manager) TrafficSummary(includeNodes bool) TrafficSummary {
	m.mu.RLock()
	list := make([]*entry, 0, len(m.nodes))
	for _, e := range m.nodes {
		list = append(list, e)
	}
	m.mu.RUnlock()

	summary := TrafficSummary{
		NodeCount: len(list),
		SampledAt: time.Now(),
	}
	if includeNodes {
		summary.Nodes = make([]NodeTrafficSpeed, 0, len(list))
	}

	for _, e := range list {
		totalUp := e.totalUpload.Load()
		totalDown := e.totalDownload.Load()

		e.mu.RLock()
		upSpeed := e.uploadSpeed
		downSpeed := e.downloadSpeed
		tag := e.info.Tag
		e.mu.RUnlock()

		summary.TotalUpload += totalUp
		summary.TotalDownload += totalDown
		summary.UploadSpeed += upSpeed
		summary.DownloadSpeed += downSpeed

		if includeNodes {
			summary.Nodes = append(summary.Nodes, NodeTrafficSpeed{
				Tag:           tag,
				UploadSpeed:   upSpeed,
				DownloadSpeed: downSpeed,
				TotalUpload:   totalUp,
				TotalDownload: totalDown,
			})
		}
	}

	return summary
}

func classifySourceStructuralFailure(snap Snapshot) (bool, string) {
	if !snap.InitialCheckDone || isEffectiveSnapshot(snap) {
		return false, ""
	}

	errText := strings.ToLower(strings.TrimSpace(snap.LastError))
	if errText == "" {
		return false, ""
	}

	switch {
	case strings.Contains(errText, "reality verification failed"):
		return true, "reality_verification_failed"
	case strings.Contains(errText, "authentication failed, status code: 200"):
		return true, "authentication_failed_status_200"
	case strings.Contains(errText, "unexpected http response status: 500"),
		strings.Contains(errText, "unexpected http response status: 530"):
		return true, "unexpected_http_status"
	case strings.Contains(errText, "tls handshake: eof"),
		strings.Contains(errText, "tls: first record does not look like a tls handshake"):
		return true, "tls_handshake_eof"
	case errText == "eof", strings.Contains(errText, "unexpected eof"):
		return true, "protocol_eof"
	case strings.Contains(errText, "i/o timeout"),
		strings.Contains(errText, "context deadline exceeded"),
		strings.Contains(errText, "timeout: no recent network activity"):
		return true, "probe_timeout"
	default:
		return false, ""
	}
}

func applySecondarySelectionPenalty(state *SecondarySelectionState) {
	if state == nil || state.StructuralFailures == 0 {
		return
	}
	switch state.Dimension {
	case SelectionDimensionNodeMode:
		applyDimensionPenalty(state, 12, 28, 58, 72)
	case SelectionDimensionDomainFamily:
		applyDimensionPenalty(state, 8, 22, 50, 65)
	case SelectionDimensionProtocolFamily:
		applyDimensionPenalty(state, 4, 12, 24, 38)
	default:
		applyDimensionPenalty(state, 4, 10, 20, 30)
	}
}

func applySourceSelectionPenalty(state *SourceSelectionState) {
	if state == nil {
		return
	}
	switch {
	case state.StructuralFailures == 0:
		state.Penalty = 0
	case state.HealthyNodes == 0 && state.StructuralFailures >= 2:
		state.Penalty = 85
		state.Excluded = true
	case state.HealthyNodes == 0 && state.TotalNodes == 1 && state.StructuralFailures == 1:
		state.Penalty = 70
		state.Excluded = true
	case state.StructuralFailures*2 >= state.TotalNodes:
		state.Penalty = 45
	case state.StructuralFailures >= 1:
		state.Penalty = 20
	}
}

func applyDimensionPenalty(
	state *SecondarySelectionState,
	minorPenalty int,
	mixedPenalty int,
	excludedPenalty int,
	saturatedPenalty int,
) {
	switch {
	case state.StructuralFailures == 0:
		state.Penalty = 0
	case state.HealthyNodes == 0:
		state.Penalty = excludedPenalty
		state.Excluded = true
		if state.StructuralFailures >= 2 {
			state.Penalty = saturatedPenalty
		}
		if state.TotalNodes == state.StructuralFailures && state.TotalNodes >= 2 && state.Penalty < saturatedPenalty {
			state.Penalty = saturatedPenalty
		}
	case state.StructuralFailures >= 2:
		state.Penalty = mixedPenalty
	default:
		state.Penalty = minorPenalty
	}
}

// Probe triggers a manual health check.
