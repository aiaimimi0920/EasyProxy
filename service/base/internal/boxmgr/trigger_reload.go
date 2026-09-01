package boxmgr

import (
	"context"
	"errors"
	"fmt"

	"easy_proxies/internal/config"
	"easy_proxies/internal/store"
)

func (m *Manager) TriggerReload(ctx context.Context) error {
	return m.triggerReload(ctx, nil, false)
}

// TriggerReloadWithEphemeralNodes reloads from the persistent configuration
// using an explicit runtime-node candidate. The candidate is published only
// if the reload or idle transition commits successfully.
func (m *Manager) TriggerReloadWithEphemeralNodes(ctx context.Context, ephemeralNodes []config.NodeConfig) error {
	return m.triggerReload(ctx, ephemeralNodes, true)
}

func (m *Manager) triggerReload(
	ctx context.Context,
	candidateEphemeralNodes []config.NodeConfig,
	publishEphemeral bool,
) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	intentCtx := ctx
	if intentCtx == nil {
		intentCtx = context.Background()
	}
	intent, err := m.BeginReloadIntent(intentCtx)
	if err != nil {
		return err
	}
	defer intent.End()
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	m.mu.RLock()
	portMap := m.cfg.BuildPortMap() // Preserve existing port assignments
	oldMode := m.lastAppliedMode
	oldBasePort := m.lastAppliedBasePort
	cfgPath := ""
	if m.cfg != nil {
		cfgPath = m.cfg.FilePath()
	}
	m.mu.RUnlock()

	// Re-read config from disk using LoadForReload (only gets inline nodes + settings)
	var newCfg *config.Config
	if cfgPath != "" {
		var loadErr error
		newCfg, loadErr = config.LoadForReload(cfgPath)
		if loadErr != nil {
			m.logger.Warnf("failed to reload config from disk: %v, falling back to in-memory copy", loadErr)
			m.mu.RLock()
			newCfg = m.copyConfigLocked()
			m.mu.RUnlock()
		} else {
			m.logger.Infof("reloaded config from disk: %s", cfgPath)
		}
	} else {
		m.mu.RLock()
		newCfg = m.copyConfigLocked()
		m.mu.RUnlock()
	}

	if newCfg == nil {
		return errConfigUnavailable
	}

	// Merge inline nodes (from config.yaml) with persistent local store nodes.
	// Inline nodes take priority; store nodes are added if their URI is not already present.
	if m.store != nil {
		storeNodes, err := m.store.ListNodes(ctx, store.NodeFilter{})
		if err != nil {
			m.logger.Warnf("failed to list nodes from store during reload: %v", err)
		} else if len(storeNodes) > 0 {
			// Build set of URIs already present from inline nodes
			inlineURIs := make(map[string]bool, len(newCfg.Nodes))
			for _, n := range newCfg.Nodes {
				inlineURIs[n.URI] = true
			}

			// Merge store nodes, skipping duplicates and disabled nodes
			for _, n := range storeNodes {
				if !store.IsPersistentNodeSource(n.Source) {
					continue
				}
				if !n.Enabled {
					continue
				}
				if inlineURIs[n.URI] {
					continue // inline node takes priority
				}
				newCfg.Nodes = append(newCfg.Nodes, config.NodeConfig{
					Name:     n.Name,
					URI:      n.URI,
					Port:     n.Port,
					Username: n.Username,
					Password: n.Password,
					Source:   config.NodeSource(n.Source),
				})
			}
			m.logger.Infof("merged nodes for reload: %d inline + store nodes = %d total", len(inlineURIs), len(newCfg.Nodes))
		}
	}

	ephemeralNodes := cloneNodes(candidateEphemeralNodes)
	if !publishEphemeral {
		m.mu.RLock()
		ephemeralNodes = cloneNodes(m.ephemeralNodes)
		m.mu.RUnlock()
	}
	if len(ephemeralNodes) > 0 && hasRuntimeSourceRefs(newCfg) {
		existing := make(map[string]struct{}, len(newCfg.Nodes))
		for _, node := range newCfg.Nodes {
			existing[node.URI] = struct{}{}
		}
		for _, node := range ephemeralNodes {
			if _, ok := existing[node.URI]; ok {
				continue
			}
			newCfg.Nodes = append(newCfg.Nodes, node)
		}
	}

	// If no enabled nodes available after merging, enter idle state:
	// stop the running box gracefully so disabled nodes are no longer served.
	if len(newCfg.Nodes) == 0 && !tunDirectFallback(newCfg) {
		return m.enterIdleLockedWithEphemeralNodes(newCfg, ephemeralNodes, publishEphemeral)
	}

	// Detect mode or base port changes — if either changed, discard old port
	// assignments so all nodes get fresh ports from the new BasePort.
	modeChanged := newCfg.Mode != oldMode
	basePortChanged := newCfg.MultiPort.BasePort != oldBasePort
	if modeChanged || basePortChanged {
		m.logger.Infof("mode/base-port changed (mode: %s→%s, base: %d→%d), reassigning all ports",
			oldMode, newCfg.Mode, oldBasePort, newCfg.MultiPort.BasePort)
		portMap = nil // Discard old port map
		for idx := range newCfg.Nodes {
			newCfg.Nodes[idx].Port = 0 // Clear all ports for reassignment
		}
	}

	return m.reloadWithPortMapAndEphemeralNodesLocked(newCfg, portMap, ephemeralNodes, publishEphemeral)
}

// ReloadWithPortMap gracefully switches to a new configuration, preserving port assignments.
func (m *Manager) ReloadWithPortMap(newCfg *config.Config, portMap map[string]uint16) error {
	if newCfg == nil {
		return errors.New("new config is nil")
	}
	intent, err := m.BeginReloadIntent(context.Background())
	if err != nil {
		return err
	}
	defer intent.End()
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	return m.reloadWithPortMapLocked(newCfg, portMap)
}

// ReloadWithPortMapAndEphemeralNodes reloads one runtime-generated candidate
// and publishes its ephemeral nodes only after the reload commits. A rejected
// candidate leaves the previously published ephemeral set unchanged.
func (m *Manager) ReloadWithPortMapAndEphemeralNodes(
	newCfg *config.Config,
	portMap map[string]uint16,
	ephemeralNodes []config.NodeConfig,
) error {
	if newCfg == nil {
		return errors.New("new config is nil")
	}
	intent, err := m.BeginReloadIntent(context.Background())
	if err != nil {
		return err
	}
	defer intent.End()
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	if err := m.reloadWithPortMapAndEphemeralNodesLocked(newCfg, portMap, ephemeralNodes, true); err != nil {
		return err
	}
	return nil
}

func (m *Manager) reloadWithPortMapLocked(newCfg *config.Config, portMap map[string]uint16) error {
	return m.reloadWithPortMapAndEphemeralNodesLocked(newCfg, portMap, nil, false)
}

func (m *Manager) reloadWithPortMapAndEphemeralNodesLocked(
	newCfg *config.Config,
	portMap map[string]uint16,
	ephemeralNodes []config.NodeConfig,
	publishEphemeral bool,
) error {
	if newCfg == nil {
		return errors.New("new config is nil")
	}

	// Always normalize config (apply defaults, assign ports, etc.).
	// If portMap is provided, existing nodes keep their ports; otherwise all ports are reassigned.
	if portMap == nil {
		portMap = make(map[string]uint16)
	}
	if err := newCfg.NormalizeWithPortMap(portMap); err != nil {
		return fmt.Errorf("normalize config with port map: %w", err)
	}

	return m.reloadLockedWithEphemeralNodes(newCfg, ephemeralNodes, publishEphemeral)
}

// enterIdle stops the running sing-box instance when there are 0 enabled nodes.
// The manager enters an idle state and can be resumed by TriggerReload when
// nodes are re-enabled.
