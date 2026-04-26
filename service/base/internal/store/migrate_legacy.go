package store

import (
	"context"
	"easy_proxies/internal/config"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// LegacyNode represents a node from the old file-based storage.
type LegacyNode struct {
	Name     string
	URI      string
	Source   string // inline, nodes_file, subscription, manual
	Port     uint16
	Username string
	Password string
}

// MigrateLegacyData imports data from old file-based storage into the Store.
// It checks if the database already has nodes; if so, it skips migration.
// After successful migration, old files are renamed with .bak suffix.
//
// Parameters:
//   - configDir: directory containing config.yaml and node files
//   - inlineNodes: nodes defined directly in config.yaml
//   - nodesFilePath: path to nodes.txt (subscription/file nodes)
//   - subscriptions: list of subscription URLs (to determine source type)
func MigrateLegacyData(ctx context.Context, s Store, configDir string, inlineNodes []LegacyNode, nodesFilePath string, subscriptions []string) error {
	// Check if database already has nodes
	count, err := s.CountNodes(ctx, NodeFilter{})
	if err != nil {
		return fmt.Errorf("check existing nodes: %w", err)
	}
	if count > 0 {
		log.Printf("[store] database already has %d nodes, skipping legacy migration", count)
		return nil
	}

	log.Printf("[store] starting legacy data migration...")

	var totalImported int

	// 1. Import inline nodes from config.yaml
	if len(inlineNodes) > 0 {
		var nodes []Node
		for _, ln := range inlineNodes {
			name := ln.Name
			if name == "" {
				name = extractNameFromURI(ln.URI)
			}
			if name == "" {
				name = fmt.Sprintf("node-%d", len(nodes)+1)
			}
			nodes = append(nodes, Node{
				URI:      ln.URI,
				Name:     name,
				Source:   NodeSourceInline,
				Port:     ln.Port,
				Username: ln.Username,
				Password: ln.Password,
				Enabled:  true,
			})
		}
		if err := s.BulkUpsertNodes(ctx, nodes); err != nil {
			return fmt.Errorf("import inline nodes: %w", err)
		}
		totalImported += len(nodes)
		log.Printf("[store] imported %d inline nodes from config.yaml", len(nodes))
	}

	// 2. Import nodes from nodes.txt
	if nodesFilePath != "" {
		if _, statErr := os.Stat(nodesFilePath); statErr == nil {
			fileNodes, err := loadNodesFromLegacyFile(nodesFilePath)
			if err != nil {
				log.Printf("[store] ⚠️ failed to parse %s: %v (skipping)", nodesFilePath, err)
			} else if len(fileNodes) > 0 {
				// Determine source based on whether subscriptions are configured
				source := NodeSourceFile
				if len(subscriptions) > 0 {
					source = NodeSourceSubscription
				}

				var nodes []Node
				for _, uri := range fileNodes {
					name := extractNameFromURI(uri)
					if name == "" {
						name = fmt.Sprintf("node-%d", totalImported+len(nodes)+1)
					}
					nodes = append(nodes, Node{
						URI:     uri,
						Name:    name,
						Source:  source,
						Enabled: true,
					})
				}
				if err := s.BulkUpsertNodes(ctx, nodes); err != nil {
					return fmt.Errorf("import file nodes: %w", err)
				}
				totalImported += len(nodes)
				log.Printf("[store] imported %d nodes from %s (source: %s)", len(nodes), nodesFilePath, source)

				// Rename old file
				backupPath := nodesFilePath + ".bak"
				if err := os.Rename(nodesFilePath, backupPath); err != nil {
					log.Printf("[store] ⚠️ failed to rename %s to %s: %v", nodesFilePath, backupPath, err)
				} else {
					log.Printf("[store] renamed %s -> %s", nodesFilePath, backupPath)
				}
			}
		}
	}

	// 3. Import manual nodes from manual_nodes.txt
	manualPath := filepath.Join(configDir, "manual_nodes.txt")
	if _, statErr := os.Stat(manualPath); statErr == nil {
		manualURIs, err := loadNodesFromLegacyFile(manualPath)
		if err != nil {
			log.Printf("[store] ⚠️ failed to parse %s: %v (skipping)", manualPath, err)
		} else if len(manualURIs) > 0 {
			var nodes []Node
			for _, uri := range manualURIs {
				name := extractNameFromURI(uri)
				if name == "" {
					name = fmt.Sprintf("manual-%d", len(nodes)+1)
				}
				nodes = append(nodes, Node{
					URI:     uri,
					Name:    name,
					Source:  NodeSourceManual,
					Enabled: true,
				})
			}
			if err := s.BulkUpsertNodes(ctx, nodes); err != nil {
				return fmt.Errorf("import manual nodes: %w", err)
			}
			totalImported += len(nodes)
			log.Printf("[store] imported %d manual nodes from %s", len(nodes), manualPath)

			// Rename old file
			backupPath := manualPath + ".bak"
			if err := os.Rename(manualPath, backupPath); err != nil {
				log.Printf("[store] ⚠️ failed to rename %s to %s: %v", manualPath, backupPath, err)
			} else {
				log.Printf("[store] renamed %s -> %s", manualPath, backupPath)
			}
		}
	}

	if totalImported > 0 {
		log.Printf("[store] ✅ legacy migration completed: %d total nodes imported", totalImported)
	} else {
		log.Printf("[store] no legacy data found to migrate")
	}

	return nil
}

// loadNodesFromLegacyFile reads a file where each line is a proxy URI.
// Comments (#) and empty lines are skipped.
func loadNodesFromLegacyFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var uris []string
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if config.IsProxyURI(line) {
			uris = append(uris, line)
		}
	}
	return uris, nil
}

// extractNameFromURI extracts a name from the URI fragment (#name).
func extractNameFromURI(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	if parsed.Fragment == "" {
		return ""
	}
	decoded, err := url.QueryUnescape(parsed.Fragment)
	if err != nil {
		return parsed.Fragment
	}
	return decoded
}
