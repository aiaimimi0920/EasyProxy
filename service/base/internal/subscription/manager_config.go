package subscription

import (
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/store"
)

func (m *Manager) currentFetchTimeout() time.Duration {
	m.mu.RLock()
	baseCfg := m.baseCfg
	m.mu.RUnlock()
	if baseCfg == nil {
		return 30 * time.Second
	}
	baseCfg.RLock()
	defer baseCfg.RUnlock()
	timeout := baseCfg.SubscriptionRefresh.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if baseCfg.SourceSync.RequestTimeout > timeout {
		timeout = baseCfg.SourceSync.RequestTimeout
	}
	return timeout
}

func (m *Manager) currentSubscriptionCacheSettings() (string, time.Duration, time.Duration) {
	m.mu.RLock()
	baseCfg := m.baseCfg
	m.mu.RUnlock()
	if baseCfg == nil {
		return "", time.Hour, 5 * time.Minute
	}
	baseCfg.RLock()
	defer baseCfg.RUnlock()
	localCacheTTL := baseCfg.SubscriptionRefresh.Interval
	if localCacheTTL <= 0 {
		localCacheTTL = time.Hour
	}
	sourceSyncCacheTTL := baseCfg.SourceSync.RefreshInterval
	if sourceSyncCacheTTL <= 0 {
		sourceSyncCacheTTL = 5 * time.Minute
	}
	return baseCfg.SubscriptionCacheDir(), localCacheTTL, sourceSyncCacheTTL
}

// fetchSubscription fetches and parses a single subscription URL.
func (m *Manager) fetchSubscription(subURL string, timeout time.Duration, cacheDir string, cacheTTL time.Duration) ([]config.NodeConfig, error) {
	return config.FetchSubscriptionNodesWithClient(subURL, timeout, cacheDir, cacheTTL, m.httpClient)
}

// createNewConfig creates a new config with runtime-generated nodes while
// preserving local inline/file/manual nodes.
func (m *Manager) createNewConfig(ephemeralNodes []config.NodeConfig) *config.Config {
	// Deep copy the source config under its own read lock. The subscription
	// manager pointer can remain stable while the management API edits fields
	// in place during a reload intent.
	baseCfg := m.baseConfigSnapshot()
	if baseCfg == nil {
		return nil
	}
	newCfg := baseCfg

	// Start with persistent local nodes only.
	var allNodes []config.NodeConfig
	for _, node := range baseCfg.Nodes {
		if node.Source == config.NodeSourceInline || node.Source == config.NodeSourceFile {
			allNodes = append(allNodes, node)
		}
	}

	// Append runtime-generated subscription/manifest/fallback nodes.
	for idx := range ephemeralNodes {
		ephemeralNodes[idx].Name = strings.TrimSpace(ephemeralNodes[idx].Name)
		ephemeralNodes[idx].URI = strings.TrimSpace(ephemeralNodes[idx].URI)
		if ephemeralNodes[idx].Name == "" {
			ephemeralNodes[idx].Name = buildNodeName(ephemeralNodes[idx].URI, fmt.Sprintf("runtime-node-%d", idx+1))
		}
	}
	allNodes = append(allNodes, ephemeralNodes...)

	// Load manual nodes from Store
	if m.store != nil {
		storeManualNodes, err := m.store.ListNodes(m.ctx, store.NodeFilter{Source: store.NodeSourceManual})
		if err != nil {
			m.logger.Warnf("failed to load manual nodes from store: %v", err)
		} else if len(storeManualNodes) > 0 {
			for _, sn := range storeManualNodes {
				name := strings.TrimSpace(sn.Name)
				uri := strings.TrimSpace(sn.URI)
				if name == "" {
					if parsed, err := url.Parse(uri); err == nil && parsed.Fragment != "" {
						if decoded, err := url.QueryUnescape(parsed.Fragment); err == nil {
							name = decoded
						} else {
							name = parsed.Fragment
						}
					}
				}
				if name == "" {
					name = fmt.Sprintf("manual-%d", sn.ID)
				}
				allNodes = append(allNodes, config.NodeConfig{
					Name:     name,
					URI:      uri,
					Port:     sn.Port,
					Username: sn.Username,
					Password: sn.Password,
					Source:   config.NodeSourceManual,
				})
			}
			m.logger.Infof("preserved %d manual nodes from store during subscription refresh", len(storeManualNodes))
		}
	}

	// Assign port numbers to all nodes in multi-port-capable modes.
	if newCfg.Mode == "multi-port" || newCfg.Mode == "hybrid" {
		portCursor := newCfg.MultiPort.BasePort
		for i := range allNodes {
			allNodes[i].Port = portCursor
			portCursor++
			// Apply default credentials
			if allNodes[i].Username == "" {
				allNodes[i].Username = newCfg.MultiPort.Username
				allNodes[i].Password = newCfg.MultiPort.Password
			}
		}
	}

	newCfg.Nodes = allNodes
	return newCfg
}

func (m *Manager) baseConfigSnapshot() *config.Config {
	m.mu.RLock()
	baseCfg := m.baseCfg
	m.mu.RUnlock()
	if baseCfg == nil {
		return nil
	}
	baseCfg.RLock()
	defer baseCfg.RUnlock()
	return baseCfg.Clone()
}

type defaultLogger struct{}

func (defaultLogger) Infof(format string, args ...any) {
	log.Printf("[subscription] "+format, args...)
}

func (defaultLogger) Warnf(format string, args ...any) {
	log.Printf("[subscription] WARN: "+format, args...)
}

func (defaultLogger) Errorf(format string, args ...any) {
	log.Printf("[subscription] ERROR: "+format, args...)
}
