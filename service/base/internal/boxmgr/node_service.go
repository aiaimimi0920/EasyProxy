package boxmgr

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
	"easy_proxies/internal/store"
)

var errConfigUnavailable = errors.New("config is not initialized")

// ListConfigNodes returns a copy of all configured nodes.
// If a Store is available, it merges the disabled status from the store
// and also includes disabled nodes that are not in the active config.
// Port numbers are taken from the active config (m.cfg.Nodes) since they
// are dynamically assigned by NormalizeWithPortMap and may not be in the Store.
func (m *Manager) ListConfigNodes(ctx context.Context) ([]config.NodeConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.cfg == nil {
		return nil, errConfigUnavailable
	}
	m.cfg.RLock()
	defer m.cfg.RUnlock()

	// If no store, just return active nodes
	if m.store == nil {
		return filterPersistentConfigNodes(m.cfg.Nodes), nil
	}

	// Build a lookup from URI → runtime port from the active config.
	// These ports are dynamically assigned by NormalizeWithPortMap and
	// reflect the actual listening ports in the current sing-box instance.
	runtimePorts := make(map[string]uint16, len(m.cfg.Nodes))
	for _, n := range m.cfg.Nodes {
		if n.Port > 0 {
			runtimePorts[n.URI] = n.Port
		}
	}

	// Fetch all nodes from store (including disabled ones)
	storeNodes, err := m.store.ListNodes(ctx, store.NodeFilter{})
	if err != nil {
		// Fallback to config nodes if store fails
		m.logger.Warnf("failed to list nodes from store: %v, falling back to config", err)
		return cloneNodes(m.cfg.Nodes), nil
	}

	// Build result from store nodes (preserves disabled status)
	// Merge runtime port assignments from active config
	result := make([]config.NodeConfig, 0, len(storeNodes))
	for _, n := range storeNodes {
		if !store.IsPersistentNodeSource(n.Source) {
			continue
		}
		port := n.Port
		// Prefer runtime port from active config (dynamically assigned)
		if runtimePort, ok := runtimePorts[n.URI]; ok && runtimePort > 0 {
			port = runtimePort
		}
		result = append(result, config.NodeConfig{
			Name:     n.Name,
			URI:      n.URI,
			Port:     port,
			Username: n.Username,
			Password: n.Password,
			Source:   config.NodeSource(n.Source),
			Disabled: !n.Enabled,
		})
	}

	return result, nil
}

// CreateNode adds a new node and persists it to the Store.
// Nodes added via the WebUI are always marked as "manual" source.
func (m *Manager) CreateNode(ctx context.Context, node config.NodeConfig) (config.NodeConfig, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return config.NodeConfig{}, err
		}
	}
	if err := m.lockConfigMutation(ctx); err != nil {
		return config.NodeConfig{}, err
	}
	defer m.unlockConfigMutation()

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return config.NodeConfig{}, errConfigUnavailable
	}
	m.cfg.Lock()
	defer m.cfg.Unlock()

	normalized, err := m.prepareNodeLocked(node, "")
	if err != nil {
		return config.NodeConfig{}, err
	}

	normalized.Source = config.NodeSourceManual

	// Persist to Store if available
	if m.store != nil {
		storeNode := &store.Node{
			URI:      normalized.URI,
			Name:     normalized.Name,
			Source:   string(normalized.Source),
			Port:     normalized.Port,
			Username: normalized.Username,
			Password: normalized.Password,
			Enabled:  true,
		}
		if err := m.store.CreateNode(ctx, storeNode); err != nil {
			return config.NodeConfig{}, fmt.Errorf("save to store: %w", err)
		}
	}

	m.cfg.Nodes = append(m.cfg.Nodes, normalized)
	return normalized, nil
}

// UpdateNode updates an existing node by name and persists to the Store.
func (m *Manager) UpdateNode(ctx context.Context, ref string, node config.NodeConfig) (config.NodeConfig, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return config.NodeConfig{}, err
		}
	}
	if err := m.lockConfigMutation(ctx); err != nil {
		return config.NodeConfig{}, err
	}
	defer m.unlockConfigMutation()

	ref = strings.TrimSpace(ref)
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return config.NodeConfig{}, errConfigUnavailable
	}
	m.cfg.Lock()
	defer m.cfg.Unlock()

	idx := m.nodeIndexByRefLocked(ref)
	var existingStore *store.Node
	var err error
	if m.store != nil {
		existingStore, err = m.lookupStoreNodeLocked(ctx, ref, idx)
		if err != nil {
			return config.NodeConfig{}, fmt.Errorf("lookup in store: %w", err)
		}
	}
	if idx == -1 && existingStore == nil {
		return config.NodeConfig{}, monitor.ErrNodeNotFound
	}

	currentName := ""
	if idx >= 0 {
		currentName = m.cfg.Nodes[idx].Name
	}
	normalized, err := m.prepareNodeLocked(node, currentName)
	if err != nil {
		return config.NodeConfig{}, err
	}

	// Preserve the original source
	if idx >= 0 {
		normalized.Source = m.cfg.Nodes[idx].Source
	}

	// Persist to Store if available
	if existingStore != nil {
		existingStore.URI = normalized.URI
		existingStore.Name = normalized.Name
		existingStore.Port = normalized.Port
		existingStore.Username = normalized.Username
		existingStore.Password = normalized.Password
		if err := m.store.UpdateNode(ctx, existingStore); err != nil {
			return config.NodeConfig{}, fmt.Errorf("update in store: %w", err)
		}
	}

	if idx >= 0 {
		m.cfg.Nodes[idx] = normalized
	} else if existingStore != nil && existingStore.Enabled {
		m.cfg.Nodes = append(m.cfg.Nodes, normalized)
	}
	return normalized, nil
}

// SetNodeEnabled enables or disables a node by name.
// This only updates the store; a reload is needed for changes to take effect.
func (m *Manager) SetNodeEnabled(ctx context.Context, ref string, enabled bool) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := m.lockConfigMutation(ctx); err != nil {
		return err
	}
	defer m.unlockConfigMutation()

	ref = strings.TrimSpace(ref)
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return errConfigUnavailable
	}
	m.cfg.Lock()
	defer m.cfg.Unlock()

	// Update in Store
	idx := m.nodeIndexByRefLocked(ref)
	if m.store != nil {
		existing, err := m.lookupStoreNodeLocked(ctx, ref, idx)
		if err != nil {
			return fmt.Errorf("lookup in store: %w", err)
		}
		if existing == nil && idx == -1 {
			return monitor.ErrNodeNotFound
		}
		if existing != nil {
			existing.Enabled = enabled
			if err := m.store.UpdateNode(ctx, existing); err != nil {
				return fmt.Errorf("update in store: %w", err)
			}
		}
	} else if idx == -1 {
		// No store — just check the node exists in config
		return monitor.ErrNodeNotFound
	}

	// If disabling, remove from active config nodes
	if !enabled {
		if idx != -1 {
			m.cfg.Nodes = append(m.cfg.Nodes[:idx], m.cfg.Nodes[idx+1:]...)
		}
	}

	return nil
}

// DeleteNode removes a node by name and deletes it from the Store.
func (m *Manager) DeleteNode(ctx context.Context, ref string) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := m.lockConfigMutation(ctx); err != nil {
		return err
	}
	defer m.unlockConfigMutation()

	ref = strings.TrimSpace(ref)
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cfg == nil {
		return errConfigUnavailable
	}
	m.cfg.Lock()
	defer m.cfg.Unlock()

	idx := m.nodeIndexByRefLocked(ref)

	// Delete from Store if available
	if m.store != nil {
		existing, err := m.lookupStoreNodeLocked(ctx, ref, idx)
		if err != nil {
			return fmt.Errorf("lookup in store: %w", err)
		}
		if existing == nil && idx == -1 {
			return monitor.ErrNodeNotFound
		}
		if existing != nil {
			if err := m.store.DeleteNode(ctx, existing.ID); err != nil {
				return fmt.Errorf("delete from store: %w", err)
			}
		}
	} else if idx == -1 {
		return monitor.ErrNodeNotFound
	}

	if idx != -1 {
		m.cfg.Nodes = append(m.cfg.Nodes[:idx], m.cfg.Nodes[idx+1:]...)
	}
	return nil
}

func (m *Manager) lookupStoreNodeLocked(ctx context.Context, ref string, activeIdx int) (*store.Node, error) {
	if m.store == nil {
		return nil, nil
	}

	if activeIdx >= 0 && activeIdx < len(m.cfg.Nodes) {
		activeURI := strings.TrimSpace(m.cfg.Nodes[activeIdx].URI)
		if activeURI != "" {
			node, err := m.store.GetNodeByURI(ctx, activeURI)
			if err != nil {
				return nil, err
			}
			if node != nil {
				return node, nil
			}
		}
	}

	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	if node, err := m.store.GetNodeByURI(ctx, ref); err != nil {
		return nil, err
	} else if node != nil {
		return node, nil
	}
	return m.store.GetNodeByName(ctx, ref)
}

// TriggerReload reloads the sing-box instance by re-reading config from disk
// and loading nodes from the SQLite Store.
