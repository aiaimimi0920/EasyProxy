package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
)

func (m *Manager) loadDeviceProfiles(ctx context.Context) (map[string]*CompiledProfile, error) {
	rows, err := m.store.ListDeviceProfiles(ctx)
	if err != nil {
		return nil, err
	}
	profiles := make(map[string]*CompiledProfile, len(rows))
	for _, row := range rows {
		var definition Definition
		if err := json.Unmarshal(row.ProfileJSON, &definition); err != nil {
			return nil, fmt.Errorf("decode device profile %q: %w", row.DeviceID, err)
		}
		compiled, err := Compile(deviceProfileID(row.DeviceID), KindDevice, row.Revision, definition, m.lookup)
		if err != nil {
			return nil, fmt.Errorf("compile device profile %q: %w", row.DeviceID, err)
		}
		profiles[normalizeDeviceID(row.DeviceID)] = compiled
	}
	return profiles, nil
}

func (m *Manager) loadMappings(ctx context.Context) ([]IPMapping, error) {
	rows, err := m.store.ListDeviceIPMappings(ctx)
	if err != nil {
		return nil, err
	}
	mappings := make([]IPMapping, 0, len(rows))
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		prefix, err := netip.ParsePrefix(strings.TrimSpace(row.CIDR))
		if err != nil {
			return nil, fmt.Errorf("parse device ip mapping %q: %w", row.MappingID, err)
		}
		deviceID, err := NormalizeDeviceID(row.DeviceID)
		if err != nil {
			return nil, fmt.Errorf("normalize device ip mapping %q: %w", row.MappingID, err)
		}
		mappings = append(mappings, IPMapping{
			MappingID: strings.TrimSpace(row.MappingID),
			Prefix:    prefix.Masked(),
			DeviceID:  deviceID,
			Priority:  row.Priority,
		})
	}
	return mappings, nil
}
