package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"easy_proxies/internal/config"
)

func (s *Server) handleSubscriptionStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	subRefresher := s.subscriptionRefresherSnapshot()
	if subRefresher == nil {
		// No subscription manager — read config directly to provide accurate status
		s.cfgMu.RLock()
		c := s.cfgSrc
		s.cfgMu.RUnlock()

		resp := map[string]any{
			"enabled":           false,
			"has_subscriptions": false,
			"message":           "订阅管理器未初始化",
		}
		if c != nil {
			c.RLock()
			resp["enabled"] = c.SubscriptionRefresh.Enabled
			resp["has_subscriptions"] = len(c.Subscriptions) > 0
			c.RUnlock()
		}
		writeJSON(w, resp)
		return
	}

	status := subRefresher.Status()
	writeJSON(w, map[string]any{
		"enabled":           status.Enabled,
		"has_subscriptions": status.HasSubscriptions,
		"last_refresh":      status.LastRefresh,
		"next_refresh":      status.NextRefresh,
		"node_count":        status.NodeCount,
		"last_error":        status.LastError,
		"refresh_count":     status.RefreshCount,
		"is_refreshing":     status.IsRefreshing,
	})
}

// handleSubscriptionRefresh triggers an immediate subscription refresh.
func (s *Server) handleSubscriptionRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	subRefresher := s.subscriptionRefresherSnapshot()
	if subRefresher == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]any{"error": "订阅管理器未初始化，请重启程序"})
		return
	}

	if err := subRefresher.RefreshNow(); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}

	status := subRefresher.Status()
	writeJSON(w, map[string]any{
		"message":    "刷新成功",
		"node_count": status.NodeCount,
	})
}

func (s *Server) handleSourceSyncStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	sourceSync := s.sourceSyncSnapshot()
	if sourceSync == nil {
		writeJSON(w, SourceSyncStatus{})
		return
	}

	writeJSON(w, sourceSync.SourceSyncStatus())
}

func (s *Server) handleRoutingStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if manager := s.profileManagerSnapshot(); manager != nil && manager.LocalServerEnabled() {
		writeJSON(w, s.localServerRoutingStatus(manager))
		return
	}
	routing := s.routingSnapshot()
	if routing == nil {
		writeJSON(w, RoutingStatus{Enabled: false})
		return
	}
	writeJSON(w, routing.RoutingStatus())
}

func (s *Server) handleGatewayStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	reporter := s.gatewaySnapshot()
	if reporter == nil {
		writeJSON(w, map[string]any{"enabled": false, "applied": false})
		return
	}
	writeJSON(w, reporter.GatewayStatus())
}

// routingConfigPayload is the editable smart-routing configuration exchanged by
// GET/PUT /api/routing/config. Durations are expressed as Go duration strings
// (e.g. "2h", "10m") for human-friendly editing in the UI.
type routingConfigPayload struct {
	Enabled            bool                    `json:"enabled"`
	Listen             string                  `json:"listen"`
	DefaultStrategy    string                  `json:"default_strategy"`
	UseDefaultRules    bool                    `json:"use_default_rules"`
	FinalPolicy        string                  `json:"final_policy"`
	Rules              []string                `json:"rules"`
	RuleProviders      []routingProviderConfig `json:"rule_providers"`
	LongLivedMinUptime string                  `json:"long_lived_min_uptime"`
	LongLivedMinRate   float64                 `json:"long_lived_min_success_rate"`
	SessionTTL         string                  `json:"session_ttl"`
}

type routingProviderConfig struct {
	URL      string `json:"url"`
	Policy   string `json:"policy"`
	Behavior string `json:"behavior"`
	Interval string `json:"interval"`
}

// handleRoutingConfig reads (GET) or updates (PUT) the smart-routing config.
//
// On PUT the config is validated, persisted to YAML, and then applied with the
// cheapest sufficient mechanism: rule/strategy/final-policy edits are hot-applied
// to the running engine; enable/disable or listen-address edits set need_reload
// so the client can trigger a full reload (which rebinds the entry port and
// rebuilds sing-box).
func (s *Server) handleRoutingConfig(w http.ResponseWriter, r *http.Request) {
	if manager := s.profileManagerSnapshot(); manager != nil && manager.LocalServerEnabled() {
		s.handleLocalServerRoutingConfig(w, r, manager)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.getRoutingConfig())
	case http.MethodPut:
		var req routingConfigPayload
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "无效的请求体: " + err.Error()})
			return
		}
		needReload, err := s.updateRoutingConfig(req)
		if err != nil {
			if errors.Is(err, errReloadInProgress) {
				w.WriteHeader(http.StatusConflict)
				writeJSON(w, map[string]any{"error": "配置正在重载，请稍后重试", "need_reload": true})
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{
			"message":     "分流配置已保存",
			"need_reload": needReload,
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// getRoutingConfig snapshots the current routing config into the editable payload.
func (s *Server) getRoutingConfig() routingConfigPayload {
	s.cfgMu.RLock()
	c := s.cfgSrc
	s.cfgMu.RUnlock()
	if c == nil {
		return routingConfigPayload{}
	}
	c.RLock()
	defer c.RUnlock()

	providers := make([]routingProviderConfig, 0, len(c.Routing.RuleProviders))
	for _, p := range c.Routing.RuleProviders {
		providers = append(providers, routingProviderConfig{
			URL:      p.URL,
			Policy:   p.Policy,
			Behavior: p.Behavior,
			Interval: p.Interval.String(),
		})
	}
	return routingConfigPayload{
		Enabled:            c.Routing.Enabled,
		Listen:             c.Routing.Listen,
		DefaultStrategy:    c.Routing.DefaultStrategy,
		UseDefaultRules:    c.RoutingUseDefaultRules(),
		FinalPolicy:        c.Routing.FinalPolicy,
		Rules:              append([]string(nil), c.Routing.Rules...),
		RuleProviders:      providers,
		LongLivedMinUptime: c.Routing.LongLived.MinUptime.String(),
		LongLivedMinRate:   c.Routing.LongLived.MinSuccessRate,
		SessionTTL:         c.Routing.Session.TTL.String(),
	}
}

// updateRoutingConfig validates, persists, and applies a routing config edit.
// Returns whether a full reload is required for the change to fully take effect.
func (s *Server) updateRoutingConfig(req routingConfigPayload) (bool, error) {
	// Parse/validate durations up front so a bad value fails cleanly before we
	// mutate anything.
	var llUptime, sessionTTL time.Duration
	if v := strings.TrimSpace(req.LongLivedMinUptime); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return false, fmt.Errorf("长效判定时长无效: %q", req.LongLivedMinUptime)
		}
		llUptime = d
	}
	if v := strings.TrimSpace(req.SessionTTL); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return false, fmt.Errorf("会话 TTL 无效: %q", req.SessionTTL)
		}
		sessionTTL = d
	}
	if req.LongLivedMinRate < 0 || req.LongLivedMinRate > 1 {
		return false, fmt.Errorf("长效成功率需在 0~1 之间: %v", req.LongLivedMinRate)
	}
	providers := make([]config.RuleProvider, 0, len(req.RuleProviders))
	for _, p := range req.RuleProviders {
		if strings.TrimSpace(p.URL) == "" {
			continue
		}
		var interval time.Duration
		if v := strings.TrimSpace(p.Interval); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d < 0 {
				return false, fmt.Errorf("规则订阅刷新间隔无效: %q", p.Interval)
			}
			interval = d
		}
		providers = append(providers, config.RuleProvider{
			URL:      strings.TrimSpace(p.URL),
			Policy:   strings.TrimSpace(p.Policy),
			Behavior: strings.TrimSpace(p.Behavior),
			Interval: interval,
		})
	}
	if err := config.ValidateRuleProviders(providers); err != nil {
		return false, err
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

	// Determine whether the change needs a full reload BEFORE mutating config:
	// enable/disable and listen-address edits change whether the pool inbound
	// binds the entry port, while session TTL changes rebuild sticky state.
	// Long-lived thresholds are propagated directly to existing monitor entries.
	c.Lock()
	reloadNeeded := c.Routing.Enabled != req.Enabled ||
		strings.TrimSpace(c.Routing.Listen) != strings.TrimSpace(req.Listen) ||
		c.Routing.Session.TTL != sessionTTL
	candidate := c.Clone()
	candidate.Routing.Enabled = req.Enabled
	candidate.Routing.Listen = strings.TrimSpace(req.Listen)
	candidate.Routing.DefaultStrategy = strings.TrimSpace(req.DefaultStrategy)
	useDefaults := req.UseDefaultRules
	candidate.Routing.UseDefaultRules = &useDefaults
	candidate.Routing.FinalPolicy = strings.TrimSpace(req.FinalPolicy)
	candidate.Routing.Rules = append([]string(nil), req.Rules...)
	candidate.Routing.RuleProviders = providers
	candidate.Routing.LongLived.MinUptime = llUptime
	candidate.Routing.LongLived.MinSuccessRate = req.LongLivedMinRate
	candidate.Routing.Session.TTL = sessionTTL
	candidate.Lock()
	err := candidate.SaveSettings()
	candidate.Unlock()
	if err != nil {
		c.Unlock()
		return false, fmt.Errorf("保存配置失败: %w", err)
	}
	c.Routing = candidate.Routing
	c.Unlock()

	// Hot-apply rules, strategy, final policy, providers, and long-lived
	// thresholds when no structural change forces a reload.
	routing := s.routingSnapshot()
	if !reloadNeeded && routing != nil {
		if applied := routing.ApplyHot(c); applied {
			return false, nil
		}
		// Not hot-appliable (e.g. routing running-state mismatch) → reload.
		return true, nil
	}
	return reloadNeeded, nil
}

func (s *Server) handleSourceSyncSourceHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if s.mgr == nil {
		writeJSON(w, map[string]any{
			"sources":       []SourceHealthState{},
			"total_sources": 0,
		})
		return
	}

	sourceRef := strings.TrimSpace(r.URL.Query().Get("source_ref"))
	if sourceRef == "" {
		sourceRef = strings.TrimSpace(r.URL.Query().Get("ref"))
	}

	grouped := s.mgr.SourceHealthStates()
	if sourceRef != "" {
		state, ok := grouped[sourceRef]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]any{
				"error":      "source_ref not found",
				"source_ref": sourceRef,
			})
			return
		}
		writeJSON(w, map[string]any{
			"sources":       []SourceHealthState{state},
			"total_sources": 1,
			"source_ref":    sourceRef,
		})
		return
	}

	sources := make([]SourceHealthState, 0, len(grouped))
	for _, state := range grouped {
		sources = append(sources, state)
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].SelectionExcluded != sources[j].SelectionExcluded {
			return !sources[i].SelectionExcluded && sources[j].SelectionExcluded
		}
		if sources[i].EffectiveAvailableNodes != sources[j].EffectiveAvailableNodes {
			return sources[i].EffectiveAvailableNodes > sources[j].EffectiveAvailableNodes
		}
		if sources[i].TotalNodes != sources[j].TotalNodes {
			return sources[i].TotalNodes > sources[j].TotalNodes
		}
		return sources[i].Ref < sources[j].Ref
	})

	writeJSON(w, map[string]any{
		"sources":       sources,
		"total_sources": len(sources),
	})
}

// nodePayload is the JSON request body for node CRUD operations.
