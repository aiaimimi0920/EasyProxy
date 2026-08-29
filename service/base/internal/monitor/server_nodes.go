package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	// First check if this is an API request that wasn't matched
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Try to serve static file
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		// Clean the path to avoid directory traversal
		cleanPath := "assets" + r.URL.Path
		_, err := embeddedFS.Open(cleanPath)
		if err == nil {
			// If file exists, serve it
			r.URL.Path = cleanPath // rewrite path for FileServer
			http.FileServer(http.FS(embeddedFS)).ServeHTTP(w, r)
			return
		}
	}

	// For root or non-existent files (SPA routing), serve index.html
	data, err := embeddedFS.ReadFile("assets/index.html")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func isTruthyQueryValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func isEffectiveSnapshot(snap Snapshot) bool {
	effective, _, _ := effectiveAvailabilityDetails(snap)
	return effective
}

func filterEffectiveSnapshots(nodes []Snapshot) []Snapshot {
	filtered := make([]Snapshot, 0, len(nodes))
	for _, snap := range nodes {
		if isEffectiveSnapshot(snap) {
			filtered = append(filtered, snap)
		}
	}
	return filtered
}

func preferEffectiveSnapshots(nodes []Snapshot) []Snapshot {
	reordered := append([]Snapshot(nil), nodes...)
	sort.SliceStable(reordered, func(i, j int) bool {
		leftEffective := isEffectiveSnapshot(reordered[i])
		rightEffective := isEffectiveSnapshot(reordered[j])
		if leftEffective == rightEffective {
			return false
		}
		return leftEffective && !rightEffective
	})
	return reordered
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	onlyAvailable := isTruthyQueryValue(r.URL.Query().Get("only_available")) ||
		isTruthyQueryValue(r.URL.Query().Get("available_only"))
	preferAvailable := onlyAvailable || isTruthyQueryValue(r.URL.Query().Get("prefer_available"))
	if (onlyAvailable || preferAvailable) && s.mgr != nil {
		if err := s.mgr.WaitForInitialProbe(0); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			writeJSON(w, map[string]any{
				"error":   "INITIAL_PROXY_PROBE_PENDING",
				"message": err.Error(),
			})
			return
		}
	}

	allNodes := s.mgr.Snapshot()
	availableNodes := filterEffectiveSnapshots(allNodes)
	probeAvailableNodes := 0
	trafficProvenNodes := 0
	for _, snap := range allNodes {
		if snap.InitialCheckDone && snap.Available && !snap.Blacklisted {
			probeAvailableNodes++
		}
		if snap.TrafficProvenUsable {
			trafficProvenNodes++
		}
	}
	nodes := allNodes
	if onlyAvailable {
		nodes = availableNodes
	} else if preferAvailable {
		nodes = preferEffectiveSnapshots(allNodes)
	}

	totalNodes := len(nodes)

	// Calculate region statistics and traffic totals
	regionStats := make(map[string]int)
	regionHealthy := make(map[string]int)
	for _, snap := range nodes {
		region := snap.Region
		if region == "" {
			region = "other"
		}
		regionStats[region]++
		if isEffectiveSnapshot(snap) {
			regionHealthy[region]++
		}
	}

	traffic := s.mgr.TrafficSummary(false)

	payload := map[string]any{
		"nodes":                 nodes,
		"total_nodes":           totalNodes,
		"all_total_nodes":       len(allNodes),
		"available_nodes":       len(availableNodes),
		"probe_available_nodes": probeAvailableNodes,
		"traffic_proven_nodes":  trafficProvenNodes,
		"total_upload":          traffic.TotalUpload,
		"total_download":        traffic.TotalDownload,
		"upload_speed":          traffic.UploadSpeed,
		"download_speed":        traffic.DownloadSpeed,
		"traffic_sampled":       traffic.SampledAt,
		"region_stats":          regionStats,
		"region_healthy":        regionHealthy,
		"only_available":        onlyAvailable,
		"prefer_available":      preferAvailable,
	}
	writeJSON(w, payload)
}

func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	snapshots := s.mgr.Snapshot()
	var totalCalls, totalSuccess int64
	debugNodes := make([]map[string]any, 0, len(snapshots))
	for _, snap := range snapshots {
		totalCalls += snap.SuccessCount + int64(snap.FailureCount)
		totalSuccess += snap.SuccessCount
		debugNodes = append(debugNodes, map[string]any{
			"tag":                   snap.Tag,
			"name":                  snap.Name,
			"mode":                  snap.Mode,
			"port":                  snap.Port,
			"source_kind":           snap.SourceKind,
			"source_name":           snap.SourceName,
			"source_ref":            snap.SourceRef,
			"availability_score":    snap.AvailabilityScore,
			"failure_count":         snap.FailureCount,
			"reported_success":      snap.ReportedSuccessCount,
			"reported_failure":      snap.ReportedFailureCount,
			"success_count":         snap.SuccessCount,
			"active_connections":    snap.ActiveConnections,
			"initial_check_done":    snap.InitialCheckDone,
			"available":             snap.Available,
			"effective_available":   snap.EffectiveAvailable,
			"traffic_proven_usable": snap.TrafficProvenUsable,
			"availability_source":   snap.AvailabilitySource,
			"last_latency_ms":       snap.LastLatencyMs,
			"last_success":          snap.LastSuccess,
			"last_failure":          snap.LastFailure,
			"last_error":            snap.LastError,
			"blacklisted":           snap.Blacklisted,
			"total_upload":          snap.TotalUpload,
			"total_download":        snap.TotalDownload,
			"timeline":              snap.Timeline,
		})
	}
	var successRate float64
	if totalCalls > 0 {
		successRate = float64(totalSuccess) / float64(totalCalls) * 100
	}
	writeJSON(w, map[string]any{
		"nodes":         debugNodes,
		"total_calls":   totalCalls,
		"total_success": totalSuccess,
		"success_rate":  successRate,
	})
}

func (s *Server) handleBestProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	topN := 1
	if v := r.URL.Query().Get("top"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			topN = n
		}
	}

	allNodes := s.mgr.Snapshot()
	available := filterEffectiveSnapshots(allNodes)

	if len(available) == 0 {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"error": "no available proxy nodes"})
		return
	}

	// Rank: AvailabilityScore desc → LastLatencyMs asc → ActiveConnections asc
	sort.SliceStable(available, func(i, j int) bool {
		if available[i].AvailabilityScore != available[j].AvailabilityScore {
			return available[i].AvailabilityScore > available[j].AvailabilityScore
		}
		li, lj := available[i].LastLatencyMs, available[j].LastLatencyMs
		if li <= 0 {
			li = math.MaxInt64
		}
		if lj <= 0 {
			lj = math.MaxInt64
		}
		if li != lj {
			return li < lj
		}
		return available[i].ActiveConnections < available[j].ActiveConnections
	})

	if topN > len(available) {
		topN = len(available)
	}

	// Build proxy URL prefix from multi-port config.
	proxyHost := "0.0.0.0"
	proxyProto := "http"
	s.cfgMu.RLock()
	c := s.cfgSrc
	s.cfgMu.RUnlock()
	if c != nil {
		c.RLock()
		if c.MultiPort.Address != "" {
			proxyHost = c.MultiPort.Address
		}
		if c.MultiPort.Protocol != "" {
			proxyProto = c.MultiPort.Protocol
		}
		c.RUnlock()
	}

	nodes := make([]map[string]any, 0, topN)
	for _, snap := range available[:topN] {
		nodes = append(nodes, map[string]any{
			"name":               snap.Name,
			"tag":                snap.Tag,
			"proxy_url":          fmt.Sprintf("%s://%s:%d", proxyProto, proxyHost, snap.Port),
			"port":               snap.Port,
			"availability_score": snap.AvailabilityScore,
			"last_latency_ms":    snap.LastLatencyMs,
			"active_connections": snap.ActiveConnections,
			"region":             snap.Region,
		})
	}

	writeJSON(w, map[string]any{
		"nodes":           nodes,
		"total_available": len(available),
	})
}

func (s *Server) handleNodeAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/nodes/"), "/")
	if len(parts) < 1 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	tag := parts[0]
	if tag == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch action {
	case "probe":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		latency, err := s.mgr.Probe(ctx, tag)
		if err != nil {
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		latencyMs := latency.Milliseconds()
		if latencyMs == 0 && latency > 0 {
			latencyMs = 1 // Round up sub-millisecond latencies to 1ms
		}
		writeJSON(w, map[string]any{"message": "探测成功", "latency_ms": latencyMs})
	case "release":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := s.mgr.Release(tag); err != nil {
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"message": "已解除拉黑"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// handleProbeAll probes all nodes in batches and returns results via SSE
func (s *Server) handleProbeAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	// Get all nodes
	snapshots := s.mgr.Snapshot()
	total := len(snapshots)
	if total == 0 {
		fmt.Fprintf(w, "data: %s\n\n", `{"type":"complete","total":0,"success":0,"failed":0}`)
		flusher.Flush()
		return
	}

	// Send start event
	fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(`{"type":"start","total":%d}`, total))
	flusher.Flush()

	// Budget for every queued node to receive its own 10-second probe window,
	// even on the minimum supported worker count.
	ctx, cancel := context.WithTimeout(r.Context(), probeAllRoundTimeout(total))
	defer cancel()

	// Probe all nodes with semaphore control
	type probeResult struct {
		tag     string
		name    string
		latency int64
		err     string
	}
	results := make(chan probeResult, total)
	var wg sync.WaitGroup

	// Launch probes with semaphore control
	for _, snap := range snapshots {
		wg.Add(1)
		go func(snap Snapshot) {
			defer wg.Done()

			// Acquire semaphore permit
			if err := s.probeSem.Acquire(ctx, 1); err != nil {
				results <- probeResult{
					tag:  snap.Tag,
					name: snap.Name,
					err:  "probe cancelled: " + err.Error(),
				}
				return
			}
			defer s.probeSem.Release(1)

			// Execute probe
			probeCtx, probeCancel := context.WithTimeout(ctx, 10*time.Second)
			defer probeCancel()

			latency, err := s.mgr.Probe(probeCtx, snap.Tag)
			if err != nil {
				results <- probeResult{
					tag:     snap.Tag,
					name:    snap.Name,
					latency: -1,
					err:     err.Error(),
				}
			} else {
				results <- probeResult{
					tag:     snap.Tag,
					name:    snap.Name,
					latency: latency.Milliseconds(),
					err:     "",
				}
			}
		}(snap)
	}

	// Wait for all probes to complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	successCount := 0
	failedCount := 0
	count := 0

	for result := range results {
		count++
		if result.err != "" {
			failedCount++
		} else {
			successCount++
		}

		progress := float64(count) / float64(total) * 100
		status := "success"
		if result.err != "" {
			status = "error"
		}

		eventPayload := map[string]any{
			"type":     "progress",
			"tag":      result.tag,
			"name":     result.name,
			"latency":  result.latency,
			"status":   status,
			"error":    result.err,
			"current":  count,
			"total":    total,
			"progress": math.Round(progress*10) / 10,
		}
		eventData, _ := json.Marshal(eventPayload)
		fmt.Fprintf(w, "data: %s\n\n", eventData)
		flusher.Flush()
	}

	// Send complete event
	fmt.Fprintf(w, "data: %s\n\n", fmt.Sprintf(`{"type":"complete","total":%d,"success":%d,"failed":%d}`, total, successCount, failedCount))
	flusher.Flush()
}

func probeAllRoundTimeout(total int) time.Duration {
	const (
		minimumWorkers = 10
		perNodeTimeout = 10 * time.Second
		minimumBudget  = 2 * time.Minute
		maximumBudget  = 10 * time.Minute
	)
	waves := (total + minimumWorkers - 1) / minimumWorkers
	budget := 30*time.Second + time.Duration(waves)*perNodeTimeout
	if budget < minimumBudget {
		return minimumBudget
	}
	if budget > maximumBudget {
		return maximumBudget
	}
	return budget
}

func (s *Server) handleTrafficStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	send := func(payload map[string]any) bool {
		data, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Initial snapshot
	initial := s.mgr.TrafficSummary(true)
	if !send(map[string]any{
		"type":           "traffic",
		"node_count":     initial.NodeCount,
		"total_upload":   initial.TotalUpload,
		"total_download": initial.TotalDownload,
		"upload_speed":   initial.UploadSpeed,
		"download_speed": initial.DownloadSpeed,
		"sampled_at":     initial.SampledAt,
		"nodes":          initial.Nodes,
	}) {
		return
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.done:
			return
		case <-ticker.C:
			summary := s.mgr.TrafficSummary(true)
			ok := send(map[string]any{
				"type":           "traffic",
				"node_count":     summary.NodeCount,
				"total_upload":   summary.TotalUpload,
				"total_download": summary.TotalDownload,
				"upload_speed":   summary.UploadSpeed,
				"download_speed": summary.DownloadSpeed,
				"sampled_at":     summary.SampledAt,
				"nodes":          summary.Nodes,
			})
			if !ok {
				return
			}
		}
	}
}
