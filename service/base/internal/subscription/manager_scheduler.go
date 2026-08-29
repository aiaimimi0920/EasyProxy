package subscription

import (
	"strings"
	"time"

	"easy_proxies/internal/config"
)

func (m *Manager) refreshLoop() {
	interval := m.currentInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Set initial next refresh time
	m.mu.Lock()
	if m.isEnabledLocked() {
		m.status.NextRefresh = time.Now().Add(interval)
	}
	m.mu.Unlock()

	for {
		select {
		case <-m.ctx.Done():
			return

		case <-ticker.C:
			// Dynamically adjust interval if config changed
			newInterval := m.currentInterval()
			if newInterval != interval {
				m.logger.Infof("subscription refresh interval changed: %s → %s", interval, newInterval)
				interval = newInterval
				ticker.Reset(interval)
			}

			// Only auto-refresh when the configured refresh modes are enabled
			if m.isEnabled() {
				m.doRefresh()
			}

			m.mu.Lock()
			if m.isEnabledLocked() {
				m.status.NextRefresh = time.Now().Add(interval)
			} else {
				m.status.NextRefresh = time.Time{}
			}
			m.mu.Unlock()

		case <-m.manualRefresh:
			// Manual refresh always executes (caller already verified subscriptions exist)
			m.doRefresh()
			// Reset ticker and recalculate interval after manual refresh
			newInterval := m.currentInterval()
			if newInterval != interval {
				interval = newInterval
			}
			ticker.Reset(interval)
			m.mu.Lock()
			m.status.NextRefresh = time.Now().Add(interval)
			m.mu.Unlock()

		case <-m.configChanged:
			// Config was updated (e.g., after reload), recalculate interval
			newInterval := m.currentInterval()
			if newInterval != interval {
				m.logger.Infof("subscription refresh interval changed: %s → %s", interval, newInterval)
				interval = newInterval
				ticker.Reset(interval)
			}
			m.mu.Lock()
			if m.isEnabledLocked() {
				m.status.NextRefresh = time.Now().Add(interval)
			} else {
				m.status.NextRefresh = time.Time{}
			}
			m.mu.Unlock()
		}
	}
}

// isEnabled checks if auto-refresh should run (acquires read lock).
func (m *Manager) isEnabled() bool {
	m.mu.RLock()
	baseCfg := m.baseCfg
	m.mu.RUnlock()
	if baseCfg == nil {
		return false
	}
	baseCfg.RLock()
	defer baseCfg.RUnlock()
	return isEnabledConfig(baseCfg)
}

func (m *Manager) shouldStartImmediateRefresh() bool {
	m.mu.RLock()
	baseCfg := m.baseCfg
	lastRefreshZero := m.status.LastRefresh.IsZero()
	m.mu.RUnlock()
	return lastRefreshZero && baseCfg != nil && func() bool {
		baseCfg.RLock()
		defer baseCfg.RUnlock()
		return isEnabledConfig(baseCfg)
	}()
}

// isEnabledLocked checks if auto-refresh should run (caller must hold mu).
func (m *Manager) isEnabledLocked() bool {
	if m.baseCfg == nil {
		return false
	}
	m.baseCfg.RLock()
	defer m.baseCfg.RUnlock()
	return isEnabledConfig(m.baseCfg)
}

func isEnabledConfig(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	localSubscriptionsEnabled := cfg.SubscriptionRefresh.Enabled && len(cfg.Subscriptions) > 0
	localConnectorsEnabled := hasEnabledLocalConnectors(cfg.Connectors)
	sourceSyncEnabled := cfg.SourceSync.Enabled &&
		(strings.TrimSpace(cfg.SourceSync.ManifestURL) != "" || len(cfg.SourceSync.FallbackSubscriptions) > 0)
	return localSubscriptionsEnabled || localConnectorsEnabled || sourceSyncEnabled
}

// currentInterval returns the configured refresh interval (acquires read lock).
func (m *Manager) currentInterval() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentIntervalLocked()
}

// currentIntervalLocked returns the configured refresh interval (caller must hold mu).
func (m *Manager) currentIntervalLocked() time.Duration {
	if m.baseCfg == nil {
		return time.Hour
	}
	m.baseCfg.RLock()
	defer m.baseCfg.RUnlock()
	intervals := make([]time.Duration, 0, 2)
	if m.baseCfg.SubscriptionRefresh.Enabled && len(m.baseCfg.Subscriptions) > 0 {
		intervals = append(intervals, m.baseCfg.SubscriptionRefresh.Interval)
	}
	if m.baseCfg.SourceSync.Enabled {
		intervals = append(intervals, m.baseCfg.SourceSync.RefreshInterval)
	}
	if len(intervals) == 0 {
		return 1 * time.Hour
	}
	interval := intervals[0]
	for _, candidate := range intervals[1:] {
		if candidate > 0 && candidate < interval {
			interval = candidate
		}
	}
	if interval <= 0 {
		interval = 1 * time.Hour
	}
	return interval
}

// doRefresh performs a single refresh operation.
// It rebuilds the in-memory runtime source set and keeps remote/fallback nodes
// out of the persistent local store.
