package subscription

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"easy_proxies/internal/config"
)

func (m *Manager) doRefresh() {
	if !m.refreshMu.TryLock() {
		m.logger.Warnf("refresh already in progress, skipping")
		return
	}
	defer m.refreshMu.Unlock()

	m.mu.Lock()
	m.status.IsRefreshing = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.status.IsRefreshing = false
		m.status.RefreshCount++
		// Signal any waiters that the refresh is done, then replace the channel
		close(m.refreshDone)
		m.refreshDone = make(chan struct{})
		m.mu.Unlock()
	}()

	m.logger.Infof("starting subscription refresh")

	intentCtx := m.ctx
	if intentCtx == nil {
		intentCtx = context.Background()
	}
	m.mu.RLock()
	reloader := m.refreshReloader
	m.mu.RUnlock()
	if reloader == nil {
		err := errors.New("box manager is not configured")
		m.recordRefreshError(err)
		return
	}

	var snapshot activeSourceSnapshot
	var ephemeralNodes []config.NodeConfig
	var reloadIntent refreshReloadIntent
	for attempt := 1; attempt <= 3; attempt++ {
		configBefore := m.baseConfigSnapshot()
		var err error
		snapshot, err = m.buildActiveSourceSnapshot()
		if err != nil {
			m.logger.Errorf("build source snapshot failed: %v", err)
			m.recordRefreshError(err)
			return
		}

		subscriptionNodes, err := m.fetchSubscriptionSources(snapshot.SubscriptionSources)
		if err != nil {
			m.logger.Errorf("fetch subscriptions failed: %v", err)
			m.recordRefreshError(err)
			return
		}

		ephemeralNodes = append(subscriptionNodes, m.materializeProxySources(snapshot.EphemeralProxySources)...)

		reloadIntent, err = reloader.BeginReloadIntent(intentCtx)
		if err != nil {
			m.logger.Errorf("begin reload intent failed: %v", err)
			m.recordRefreshError(err)
			return
		}
		if reflect.DeepEqual(configBefore, m.baseConfigSnapshot()) {
			break
		}
		reloadIntent.End()
		reloadIntent = nil
		if attempt == 3 {
			err = errors.New("configuration changed repeatedly during subscription refresh")
			m.logger.Errorf("source snapshot did not stabilize: %v", err)
			m.recordRefreshError(err)
			return
		}
		m.logger.Warnf("configuration changed during source fetch; retrying with the latest snapshot")
	}
	defer reloadIntent.End()

	newCfg := m.createNewConfig(ephemeralNodes)
	if runtimeNodeSetsEqual(ephemeralNodes, reloader.CurrentEphemeralNodes()) {
		m.logger.Infof("runtime source set unchanged; skipped sing-box reload")
	} else {
		portMap := reloader.CurrentPortMap()
		if err := reloader.ReloadWithPortMapAndEphemeralNodes(newCfg, portMap, ephemeralNodes); err != nil {
			m.logger.Errorf("reload failed: %v", err)
			m.mu.Lock()
			m.status.LastError = err.Error()
			m.status.LastRefresh = time.Now()
			m.mu.Unlock()
			return
		}
	}

	if err := m.syncRuntimeNodesToStore(newCfg.Nodes); err != nil {
		m.logger.Warnf("failed to sync runtime nodes to store: %v", err)
	}

	totalNodes := len(newCfg.Nodes)
	m.mu.Lock()
	m.status.LastRefresh = time.Now()
	m.status.NodeCount = totalNodes
	m.status.LastError = ""
	m.sourceSyncStatus.FallbackActive = snapshot.FallbackActive
	m.sourceSyncStatus.LocalSourceCount = snapshot.LocalSourceCount
	m.sourceSyncStatus.ManifestSourceCount = snapshot.ManifestSourceCount
	m.sourceSyncStatus.FallbackSourceCount = snapshot.FallbackSourceCount
	m.sourceSyncStatus.ConnectorSourceCount = snapshot.ConnectorSourceCount
	m.sourceSyncStatus.ConnectorInstanceCount = snapshot.ConnectorInstanceCount
	m.mu.Unlock()

	m.logger.Infof("subscription refresh completed, %d total nodes active (%d runtime-generated)", totalNodes, len(ephemeralNodes))
}

// runtimeNodeSetsEqual ignores publication order and runtime-assigned ports.
// Every source-owned field remains part of the comparison, including embedded
// credentials and source metadata.
func runtimeNodeSetsEqual(left, right []config.NodeConfig) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, node := range left {
		node.Port = 0
		encoded, err := json.Marshal(node)
		if err != nil {
			return false
		}
		counts[string(encoded)]++
	}
	for _, node := range right {
		node.Port = 0
		encoded, err := json.Marshal(node)
		if err != nil {
			return false
		}
		key := string(encoded)
		if counts[key] == 0 {
			return false
		}
		counts[key]--
	}
	return true
}

func (m *Manager) recordRefreshError(err error) {
	m.mu.Lock()
	m.status.LastError = err.Error()
	m.status.LastRefresh = time.Now()
	m.mu.Unlock()
}

// OnConfigUpdate is called by boxmgr after a successful reload.
// It updates the subscription manager's reference to the latest config
// so that subsequent refreshes use updated subscription URLs and settings.
func (m *Manager) OnConfigUpdate(cfg *config.Config) {
	if cfg == nil {
		return
	}
	m.mu.Lock()
	m.baseCfg = cfg
	m.mu.Unlock()
	m.logger.Infof("subscription manager config updated after reload")

	// Notify the refresh loop about config changes so it can
	// recalculate interval and enable/disable auto-refresh dynamically.
	select {
	case m.configChanged <- struct{}{}:
	default:
	}
}

// CheckNodesModified always returns false — with SQLite Store,
// node modifications are tracked in the database, not via file hashes.
func (m *Manager) CheckNodesModified() bool {
	return false
}

// MarkNodesModified updates the modification status.
func (m *Manager) MarkNodesModified() {
	m.mu.Lock()
	m.status.NodesModified = true
	m.mu.Unlock()
}
