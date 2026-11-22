package boxmgr

import (
	"bytes"
	"encoding/json"
	"sort"

	"easy_proxies/internal/config"
)

func effectiveReloadConfigEqual(oldCfg, newCfg *config.Config) bool {
	if oldCfg == nil || newCfg == nil {
		return false
	}
	leftBytes, err := canonicalReloadConfigBytes(oldCfg)
	if err != nil {
		return false
	}
	rightBytes, err := canonicalReloadConfigBytes(newCfg)
	if err != nil {
		return false
	}
	return bytes.Equal(leftBytes, rightBytes)
}

func canonicalReloadConfigBytes(cfg *config.Config) ([]byte, error) {
	snap := snapshotConfig(cfg)
	if snap == nil {
		return nil, nil
	}

	if len(snap.Nodes) > 0 {
		sort.SliceStable(snap.Nodes, func(i, j int) bool {
			a, b := snap.Nodes[i], snap.Nodes[j]
			if a.URI != b.URI {
				return a.URI < b.URI
			}
			if a.Port != b.Port {
				return a.Port < b.Port
			}
			if a.Name != b.Name {
				return a.Name < b.Name
			}
			return a.SourceRef < b.SourceRef
		})
	} else {
		snap.Nodes = nil
	}

	normalizeNilSlice(&snap.Connectors)
	normalizeNilSlice(&snap.Subscriptions)
	normalizeNilSlice(&snap.ExtraListeners)
	normalizeNilSlice(&snap.Management.ProbeTargets)
	normalizeNilSlice(&snap.Pool.DetourSourceRefs)
	normalizeNilSlice(&snap.DNS.RemoteServers)
	normalizeNilSlice(&snap.Routing.SelectionTiers)
	normalizeNilSlice(&snap.Routing.Rules)
	normalizeNilSlice(&snap.Routing.RuleFiles)
	normalizeNilSlice(&snap.Routing.RuleProviders)
	normalizeNilSlice(&snap.Routing.NodeFilter.Countries)
	normalizeNilSlice(&snap.Routing.NodeFilter.Regions)
	normalizeNilSlice(&snap.Gateway.Ingress.Interfaces)
	normalizeNilSlice(&snap.Gateway.Ingress.InterfacePatterns)
	normalizeNilSlice(&snap.Gateway.Ingress.TrustedCIDRs)
	normalizeNilSlice(&snap.Gateway.Tun.Addresses)
	normalizeNilSlice(&snap.SourceSync.FallbackSubscriptions)
	if len(snap.Gateway.Devices) == 0 {
		snap.Gateway.Devices = nil
	}

	return json.Marshal(snap)
}

func normalizeNilSlice[T any](slice *[]T) {
	if slice != nil && len(*slice) == 0 {
		*slice = nil
	}
}
