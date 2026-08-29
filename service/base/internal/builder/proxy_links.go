package builder

import (
	"fmt"
	"log"
	"sort"

	"easy_proxies/internal/config"
	poolout "easy_proxies/internal/outbound/pool"
)

func printProxyLinks(cfg *config.Config, metadata map[string]poolout.MemberMeta) {
	log.Println("")
	log.Println("📡 Proxy Links:")
	log.Println("═══════════════════════════════════════════════════════════════")

	showPoolEntry := cfg.Mode == "pool" || cfg.Mode == "hybrid"
	showMultiPort := cfg.Mode == "multi-port" || cfg.Mode == "hybrid"

	if showPoolEntry {
		// Pool mode: single entry point for all nodes
		var auth string
		if cfg.Listener.Username != "" {
			auth = fmt.Sprintf("%s:%s@", cfg.Listener.Username, cfg.Listener.Password)
		}
		proxyURL := fmt.Sprintf("http://%s%s:%d", auth, cfg.Listener.Address, cfg.Listener.Port)
		log.Printf("🌐 Pool Entry Point:")
		log.Printf("   %s [%s]", proxyURL, cfg.Pool.Mode)

		// Print extra listeners
		for _, extra := range cfg.ExtraListeners {
			if extra.Port == 0 {
				continue
			}
			var extraAuth string
			if extra.Username != "" {
				extraAuth = fmt.Sprintf("%s:%s@", extra.Username, extra.Password)
			}
			addr := extra.Address
			if addr == "" {
				addr = cfg.Listener.Address
			}
			mode := extra.PoolMode
			if mode == "" {
				mode = cfg.Pool.Mode
			}
			extraURL := fmt.Sprintf("http://%s%s:%d", extraAuth, addr, extra.Port)
			log.Printf("   %s [%s]", extraURL, mode)
		}

		log.Println("")
		log.Printf("   Nodes in pool (%d):", len(metadata))
		for _, meta := range metadata {
			log.Printf("   • %s", meta.Name)
		}
		if showMultiPort {
			log.Println("")
		}
	}

	if showMultiPort {
		// Multi-port mode: each node has its own port
		log.Printf("🔌 Multi-Port Entry Points (%d nodes):", len(metadata))
		log.Println("")
		entries := make([]poolout.MemberMeta, 0, len(metadata))
		for _, meta := range metadata {
			entries = append(entries, meta)
		}
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].Port != entries[j].Port {
				return entries[i].Port < entries[j].Port
			}
			return entries[i].Name < entries[j].Name
		})
		for _, meta := range entries {
			var auth string
			username := cfg.MultiPort.Username
			password := cfg.MultiPort.Password
			if username != "" {
				auth = fmt.Sprintf("%s:%s@", username, password)
			}
			proxyURL := fmt.Sprintf("http://%s%s:%d", auth, cfg.MultiPort.Address, meta.Port)
			log.Printf("   [%d] %s", meta.Port, meta.Name)
			log.Printf("       %s", proxyURL)
		}
	}

	log.Println("═══════════════════════════════════════════════════════════════")
	log.Println("")
}
