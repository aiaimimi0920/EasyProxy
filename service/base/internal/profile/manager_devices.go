package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"easy_proxies/internal/store"
)

func (m *Manager) PutDevice(ctx context.Context, deviceID, displayName string, expected int64) (store.Device, error) {
	if m == nil {
		return store.Device{}, errors.New("manager is nil")
	}
	normalized, err := NormalizeDeviceID(deviceID)
	if err != nil {
		return store.Device{}, fmt.Errorf("%w: %v", ErrInvalidDeviceID, err)
	}
	return m.store.PutDevice(ctx, store.Device{
		DeviceID:    normalized,
		DisplayName: displayName,
	}, expected)
}

func (m *Manager) PutDeviceProfile(ctx context.Context, deviceID string, definition Definition, expected int64) (MutationResult, error) {
	if m == nil {
		return MutationResult{}, errors.New("manager is nil")
	}
	normalized, err := NormalizeDeviceID(deviceID)
	if err != nil {
		return MutationResult{}, fmt.Errorf("%w: %v", ErrInvalidDeviceID, err)
	}

	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()

	compiled, encoded, err := m.prepareDefinition(deviceProfileID(normalized), KindDevice, nextProfileRevision(expected), definition)
	if err != nil {
		return MutationResult{}, err
	}
	persisted, err := m.persistDeviceProfileCAS(ctx, normalized, encoded, expected)
	if err != nil {
		return MutationResult{}, err
	}

	current := m.snapshot()
	published := compiled.WithRevision(persisted.Revision)
	next := current.CloneReplacingDevice(normalized, published)
	m.registry.Store(next)
	m.restartProviderLocked(next.DeviceProfile(normalized))
	return mutationResult(next, persisted.Revision, published), nil
}

func (m *Manager) CopySharedProfile(ctx context.Context, deviceID string) (MutationResult, error) {
	current := m.snapshot()
	if current == nil || current.SharedProfile() == nil {
		return MutationResult{}, errors.New("shared profile is unavailable")
	}
	return m.PutDeviceProfile(ctx, deviceID, current.SharedProfile().Definition(), 0)
}

func (m *Manager) SetDeviceProfileEnabled(ctx context.Context, deviceID string, enabled bool, expected int64) (MutationResult, error) {
	if m == nil {
		return MutationResult{}, errors.New("manager is nil")
	}
	normalized, err := NormalizeDeviceID(deviceID)
	if err != nil {
		return MutationResult{}, fmt.Errorf("%w: %v", ErrInvalidDeviceID, err)
	}
	current, err := m.store.GetDeviceProfile(ctx, normalized)
	if err != nil {
		return MutationResult{}, err
	}
	if current == nil {
		return MutationResult{}, fmt.Errorf("%w: %q", ErrDeviceProfileNotFound, normalized)
	}

	var definition Definition
	if err := json.Unmarshal(current.ProfileJSON, &definition); err != nil {
		return MutationResult{}, fmt.Errorf("decode device profile %q: %w", normalized, err)
	}
	definition.Enabled = enabled
	return m.PutDeviceProfile(ctx, normalized, definition, expected)
}

func (m *Manager) DeleteDeviceProfile(ctx context.Context, deviceID string, expected int64) (MutationResult, error) {
	if m == nil {
		return MutationResult{}, errors.New("manager is nil")
	}
	normalized, err := NormalizeDeviceID(deviceID)
	if err != nil {
		return MutationResult{}, fmt.Errorf("%w: %v", ErrInvalidDeviceID, err)
	}

	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()

	deleted, err := m.store.DeleteDeviceProfile(ctx, normalized, expected)
	if err != nil {
		return MutationResult{}, err
	}
	current := m.snapshot()
	if !deleted {
		return mutationResult(current, 0, current.DeviceProfile(normalized)), nil
	}
	next := current.CloneReplacingDevice(normalized, nil)
	m.registry.Store(next)
	m.stopProviderLocked(deviceProfileID(normalized))
	return mutationResult(next, 0, nil), nil
}

func (m *Manager) PutIPMapping(ctx context.Context, mapping store.DeviceIPMapping, expected int64) (store.DeviceIPMapping, uint64, error) {
	if m == nil {
		return store.DeviceIPMapping{}, 0, errors.New("manager is nil")
	}
	normalizedDeviceID, err := NormalizeDeviceID(mapping.DeviceID)
	if err != nil {
		return store.DeviceIPMapping{}, 0, fmt.Errorf("%w: %v", ErrInvalidDeviceID, err)
	}
	mapping.DeviceID = normalizedDeviceID
	mapping.MappingID = strings.TrimSpace(mapping.MappingID)
	if mapping.MappingID == "" {
		return store.DeviceIPMapping{}, 0, errors.New("mapping id is required")
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(mapping.CIDR))
	if err != nil {
		return store.DeviceIPMapping{}, 0, fmt.Errorf("invalid mapping CIDR %q: %w", mapping.CIDR, err)
	}
	prefix = prefix.Masked()
	mapping.CIDR = prefix.String()
	prepared := IPMapping{
		MappingID: mapping.MappingID,
		Prefix:    prefix,
		DeviceID:  normalizedDeviceID,
		Priority:  mapping.Priority,
	}

	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()

	saved, err := m.store.PutDeviceIPMapping(ctx, mapping, expected)
	if err != nil {
		return store.DeviceIPMapping{}, 0, err
	}
	current := m.snapshot()
	var active *IPMapping
	if saved.Enabled {
		prepared.MappingID = saved.MappingID
		prepared.DeviceID = saved.DeviceID
		prepared.Priority = saved.Priority
		active = &prepared
	}
	next := current.CloneReplacingMapping(saved.MappingID, active)
	m.registry.Store(next)
	return saved, next.Revision(), nil
}

func (m *Manager) DeleteIPMapping(ctx context.Context, mappingID string, expected int64) (uint64, error) {
	if m == nil {
		return 0, errors.New("manager is nil")
	}
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()

	deleted, err := m.store.DeleteDeviceIPMapping(ctx, mappingID, expected)
	if err != nil {
		return 0, err
	}
	current := m.snapshot()
	if !deleted {
		return current.Revision(), nil
	}
	next := current.CloneReplacingMapping(mappingID, nil)
	m.registry.Store(next)
	return next.Revision(), nil
}
