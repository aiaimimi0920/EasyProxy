package config

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"strings"
)

func (c *Config) normalizeInternal(skipSubscriptionFetch bool) error {
	if err := c.applyDefaults(); err != nil {
		return err
	}
	if err := c.normalizeLocalServer(); err != nil {
		return err
	}

	// Mark inline nodes with source
	for idx := range c.Nodes {
		c.Nodes[idx].Source = NodeSourceInline
	}

	if skipSubscriptionFetch {
		// ---- Reload mode ----
		// Nodes persisted in SQLite are merged by the caller (app.go / boxmgr).
		// Keep reload offline by skipping remote subscription fetches, but still
		// re-read local nodes_file content so on-disk edits take effect.
		if c.NodesFile != "" && len(c.Subscriptions) == 0 {
			fileNodes, err := loadNodesFromFile(c.NodesFile)
			if err != nil {
				return fmt.Errorf("load nodes from file %q: %w", c.NodesFile, err)
			}
			for idx := range fileNodes {
				fileNodes[idx].Source = NodeSourceFile
			}
			c.Nodes = append(c.Nodes, fileNodes...)
		}
		log.Printf("[config] reload mode: %d inline/file nodes from disk", len(c.Nodes))
	} else {
		// ---- Initial load mode ----

		// Load nodes from file if specified (but NOT if subscriptions exist - subscription takes priority)
		if c.NodesFile != "" && len(c.Subscriptions) == 0 {
			fileNodes, err := loadNodesFromFile(c.NodesFile)
			if err != nil {
				return fmt.Errorf("load nodes from file %q: %w", c.NodesFile, err)
			}
			for idx := range fileNodes {
				fileNodes[idx].Source = NodeSourceFile
			}
			c.Nodes = append(c.Nodes, fileNodes...)
		}

		// Load nodes from subscriptions (fetched into memory, persisted to Store by app.go)
		if len(c.Subscriptions) > 0 {
			var subNodes []NodeConfig
			subTimeout := c.SubscriptionRefresh.Timeout
			cacheDir := c.SubscriptionCacheDir()
			cacheTTL := c.SubscriptionRefresh.Interval
			for _, subURL := range c.Subscriptions {
				nodes, err := FetchSubscriptionNodes(subURL, subTimeout, cacheDir, cacheTTL)
				if err != nil {
					log.Printf("⚠️ Failed to load subscription %q: %v (skipping)", subURL, err)
					continue
				}
				log.Printf("✅ Loaded %d nodes from subscription", len(nodes))
				subNodes = append(subNodes, nodes...)
			}
			for idx := range subNodes {
				subNodes[idx].Source = NodeSourceSubscription
			}
			c.Nodes = append(c.Nodes, subNodes...)
		}
	}

	// Note: Manual nodes are loaded from SQLite Store by app.go, not from files.

	if len(c.Nodes) == 0 {
		// Not fatal — Store may have nodes from a previous run.
		log.Printf("⚠️ No nodes in config (inline/subscription/file). Will check Store for existing nodes.")
	}
	portCursor := c.MultiPort.BasePort
	for idx := range c.Nodes {
		c.Nodes[idx].Name = strings.TrimSpace(c.Nodes[idx].Name)
		c.Nodes[idx].URI = NormalizeProxyURIInput(strings.TrimSpace(c.Nodes[idx].URI), c.SourceSync.DefaultDirectProxyScheme)

		if c.Nodes[idx].URI == "" {
			return fmt.Errorf("node %d is missing uri", idx)
		}

		// Auto-extract name from URI fragment (#name) if not provided
		if c.Nodes[idx].Name == "" {
			if parsed, err := url.Parse(c.Nodes[idx].URI); err == nil && parsed.Fragment != "" {
				// URL decode the fragment to handle encoded characters
				if decoded, err := url.QueryUnescape(parsed.Fragment); err == nil {
					c.Nodes[idx].Name = decoded
				} else {
					c.Nodes[idx].Name = parsed.Fragment
				}
			}
		}

		// Fallback to default name if still empty
		if c.Nodes[idx].Name == "" {
			c.Nodes[idx].Name = fmt.Sprintf("node-%d", idx)
		}

		// Auto-assign port in multi-port/hybrid mode, skip occupied ports
		if c.Nodes[idx].Port == 0 && (c.Mode == "multi-port" || c.Mode == "hybrid") {
			for !isPortAvailable(c.MultiPort.Address, portCursor) {
				log.Printf("⚠️  Port %d is in use, trying next port", portCursor)
				portCursor++
				if portCursor > 65535 {
					return fmt.Errorf("no available ports found starting from %d", c.MultiPort.BasePort)
				}
			}
			c.Nodes[idx].Port = portCursor
			portCursor++
		} else if c.Nodes[idx].Port == 0 {
			c.Nodes[idx].Port = portCursor
			portCursor++
		}

		if c.Mode == "multi-port" || c.Mode == "hybrid" {
			if c.Nodes[idx].Username == "" {
				c.Nodes[idx].Username = c.MultiPort.Username
				c.Nodes[idx].Password = c.MultiPort.Password
			}
		}
	}
	if c.DatabasePath == "" {
		c.DatabasePath = "data/data.db"
	}
	// Resolve database path relative to config file directory
	if c.filePath != "" && !filepath.IsAbs(c.DatabasePath) {
		c.DatabasePath = filepath.Join(filepath.Dir(c.filePath), c.DatabasePath)
	}

	// Auto-fix port conflicts in hybrid mode (pool port vs multi-port)
	if c.Mode == "hybrid" {
		poolPort := c.Listener.Port
		usedPorts := make(map[uint16]bool)
		usedPorts[poolPort] = true
		for idx := range c.Nodes {
			usedPorts[c.Nodes[idx].Port] = true
		}
		for idx := range c.Nodes {
			if c.Nodes[idx].Port == poolPort {
				// Find next available port
				newPort := c.Nodes[idx].Port + 1
				for usedPorts[newPort] || !isPortAvailable(c.MultiPort.Address, newPort) {
					newPort++
					if newPort > 65535 {
						return fmt.Errorf("no available port for node %q after conflict with pool port %d", c.Nodes[idx].Name, poolPort)
					}
				}
				log.Printf("⚠️  Node %q port %d conflicts with pool port, reassigned to %d", c.Nodes[idx].Name, poolPort, newPort)
				usedPorts[newPort] = true
				c.Nodes[idx].Port = newPort
			}
		}
	}

	return nil
}

// BuildPortMap creates a mapping from node URI to port for existing nodes.
// This is used to preserve port assignments when reloading configuration.
func (c *Config) BuildPortMap() map[string]uint16 {
	if c == nil {
		return nil
	}
	c.RLock()
	defer c.RUnlock()
	portMap := make(map[string]uint16)
	for _, node := range c.Nodes {
		if node.Port > 0 {
			portMap[node.NodeKey()] = node.Port
		}
	}
	return portMap
}

// NormalizeWithPortMap applies defaults and validation, preserving port assignments
// for nodes that exist in the provided port map.
func (c *Config) NormalizeWithPortMap(portMap map[string]uint16) error {
	if err := c.applyDefaults(); err != nil {
		return err
	}
	if err := c.normalizeLocalServer(); err != nil {
		return err
	}

	if len(c.Nodes) == 0 {
		return errors.New("config.nodes cannot be empty (no inline, subscription, or manual nodes available)")
	}

	multiPortMode := c.Mode == "multi-port" || c.Mode == "hybrid"

	// Reserve the pool listener before claiming node ports.
	usedPorts := make(map[uint16]bool)
	if c.Mode == "hybrid" {
		usedPorts[c.Listener.Port] = true
	}
	preservedPorts := make([]bool, len(c.Nodes))

	// First pass: normalize node identities and claim preserved ports. Preserved
	// assignments take priority over stale ports carried by the new config.
	for idx := range c.Nodes {
		c.Nodes[idx].Name = strings.TrimSpace(c.Nodes[idx].Name)
		c.Nodes[idx].URI = NormalizeProxyURIInput(strings.TrimSpace(c.Nodes[idx].URI), c.SourceSync.DefaultDirectProxyScheme)
		if c.Nodes[idx].URI == "" {
			return fmt.Errorf("node %d is missing uri", idx)
		}

		// Extract name from URI fragment if not provided
		if c.Nodes[idx].Name == "" {
			if parsed, err := url.Parse(c.Nodes[idx].URI); err == nil && parsed.Fragment != "" {
				if decoded, err := url.QueryUnescape(parsed.Fragment); err == nil {
					c.Nodes[idx].Name = decoded
				} else {
					c.Nodes[idx].Name = parsed.Fragment
				}
			}
		}
		if c.Nodes[idx].Name == "" {
			c.Nodes[idx].Name = fmt.Sprintf("node-%d", idx)
		}

		if multiPortMode {
			nodeKey := c.Nodes[idx].NodeKey()
			if existingPort, ok := portMap[nodeKey]; ok && existingPort > 0 {
				if usedPorts[existingPort] {
					c.Nodes[idx].Port = 0
					log.Printf("Preserved port %d for node %q conflicts with an existing listener or node; reassigning", existingPort, c.Nodes[idx].Name)
				} else {
					c.Nodes[idx].Port = existingPort
					usedPorts[existingPort] = true
					preservedPorts[idx] = true
					log.Printf("✅ Preserved port %d for node %q", existingPort, c.Nodes[idx].Name)
				}
			}
		}
	}

	// Second pass: claim non-preserved ports carried by the new config. A
	// duplicate is cleared so the normal allocator can give it a fresh port.
	if multiPortMode {
		for idx := range c.Nodes {
			port := c.Nodes[idx].Port
			if preservedPorts[idx] || port == 0 {
				continue
			}
			if usedPorts[port] {
				log.Printf("Node %q port %d conflicts with a preserved or earlier node port; reassigning", c.Nodes[idx].Name, port)
				c.Nodes[idx].Port = 0
				continue
			}
			usedPorts[port] = true
		}
	}

	// Third pass: assign new ports for nodes without preserved ports.
	portCursor := c.MultiPort.BasePort
	for idx := range c.Nodes {
		if c.Nodes[idx].Port == 0 && multiPortMode {
			// Find next available port that's not used
			for usedPorts[portCursor] || !isPortAvailable(c.MultiPort.Address, portCursor) {
				portCursor++
				if portCursor > 65535 {
					return fmt.Errorf("no available ports found starting from %d", c.MultiPort.BasePort)
				}
			}
			c.Nodes[idx].Port = portCursor
			usedPorts[portCursor] = true
			log.Printf("📌 Assigned new port %d for node %q", portCursor, c.Nodes[idx].Name)
			portCursor++
		} else if c.Nodes[idx].Port == 0 {
			c.Nodes[idx].Port = portCursor
			portCursor++
		}

		// Apply default credentials
		if c.Mode == "multi-port" || c.Mode == "hybrid" {
			if c.Nodes[idx].Username == "" {
				c.Nodes[idx].Username = c.MultiPort.Username
				c.Nodes[idx].Password = c.MultiPort.Password
			}
		}
	}

	return nil
}

func (c *Config) normalizeLocalServer() error {
	if c == nil || !c.LocalServer.Enabled {
		return nil
	}
	if c.Mode != "pool" {
		return fmt.Errorf("local_server.enabled requires mode %q", "pool")
	}
	if c.Listener.Protocol != InboundProtocolMixed {
		return fmt.Errorf("local_server.enabled requires listener.protocol %q", InboundProtocolMixed)
	}
	if len(c.ExtraListeners) > 0 {
		return errors.New("local_server.enabled does not support extra_listeners")
	}

	localListen := strings.TrimSpace(c.LocalServer.Listen)
	routingListen := strings.TrimSpace(c.Routing.Listen)
	if localListen != "" && routingListen != "" && localListen != routingListen {
		return fmt.Errorf("local_server.listen %q conflicts with routing.listen %q", localListen, routingListen)
	}

	if c.LocalServer.SharedRevision == 0 {
		c.LocalServer.SharedRevision = 1
	}
	if c.LocalServer.CredentialGeneration == 0 {
		c.LocalServer.CredentialGeneration = 1
	}

	username := strings.TrimSpace(c.LocalServer.Auth.Username)
	if username != "" && !validIdentityToken(username) {
		return fmt.Errorf("local_server.auth.username %q is invalid", c.LocalServer.Auth.Username)
	}

	password := c.LocalServer.Auth.Password
	migratedPassword := false
	if password == "" {
		listenerPassword := c.Listener.Password
		managementPassword := c.Management.Password
		switch {
		case listenerPassword != "" && managementPassword != "" && listenerPassword != managementPassword:
			return errors.New("local_server.auth.password is missing and legacy listener/management passwords conflict")
		case listenerPassword != "":
			password = listenerPassword
		case managementPassword != "":
			password = managementPassword
		default:
			return errors.New("local_server.auth.password is required when local_server.enabled=true")
		}
		migratedPassword = true
	}
	if len(password) == 0 {
		return errors.New("local_server.auth.password is required when local_server.enabled=true")
	}
	if strings.IndexByte(password, 0) >= 0 {
		return errors.New("local_server.auth.password must not contain NUL")
	}
	if len(password) > 256 {
		return errors.New("local_server.auth.password must be at most 256 bytes")
	}

	if username == "" {
		listenerUsername := strings.TrimSpace(c.Listener.Username)
		if validIdentityToken(listenerUsername) {
			username = listenerUsername
		} else {
			username = "easyproxy"
		}
	}

	c.LocalServer.Auth.Username = username
	c.LocalServer.Auth.Password = password
	if migratedPassword && c.LocalServer.CredentialGeneration < 2 {
		c.LocalServer.CredentialGeneration = 2
	}

	c.Listener.Username = username
	c.Listener.Password = password
	c.Management.Password = password
	return nil
}

// ManagementEnabled reports whether the monitoring endpoint should run.
