package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"easy_proxies/internal/store"
)

func (m *Manager) prepareDefinition(profileID string, kind Kind, revision int64, definition Definition) (*CompiledProfile, []byte, error) {
	compiled, err := Compile(profileID, kind, revision, definition, m.lookup)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidDefinition, err)
	}
	encoded, err := json.Marshal(compiled.Definition())
	if err != nil {
		return nil, nil, fmt.Errorf("marshal profile definition: %w", err)
	}
	return compiled, encoded, nil
}

func (m *Manager) persistDeviceProfileCAS(ctx context.Context, deviceID string, encoded []byte, expected int64) (store.DeviceProfile, error) {
	return m.store.PutDeviceProfile(ctx, store.DeviceProfile{
		DeviceID:      deviceID,
		ProfileJSON:   encoded,
		SchemaVersion: 1,
	}, expected)
}

func (m *Manager) restartProviderLocked(profile *CompiledProfile) {
	if m == nil || profile == nil {
		return
	}
	profileID := profile.ID()
	m.stopProviderLocked(profileID)
	specs := profile.ProviderSpecs()
	if len(specs) == 0 {
		return
	}
	m.nextProviderGeneration++
	generation := m.nextProviderGeneration
	revision := profile.Revision()
	runner := m.providerFactory(specs, func(rules []string) {
		m.applyProviderRules(profileID, revision, generation, rules)
	}, func(status ProviderStatus) {
		m.applyProviderStatus(profileID, revision, generation, status)
	})
	m.providers[profileID] = providerRuntime{
		revision:   revision,
		generation: generation,
		runner:     runner,
	}
	go runner.Start(m.ctx)
}

func (m *Manager) stopProviderLocked(profileID string) {
	if m == nil || profileID == "" {
		return
	}
	runtime, ok := m.providers[profileID]
	if !ok {
		return
	}
	if runtime.runner != nil {
		runtime.runner.Stop()
	}
	delete(m.providers, profileID)
}

func (m *Manager) restartAllProvidersLocked(registry *Registry) {
	if registry == nil {
		return
	}
	for profileID, runtime := range m.providers {
		if runtime.runner != nil {
			runtime.runner.Stop()
		}
		delete(m.providers, profileID)
	}
	m.restartProviderLocked(registry.SharedProfile())
	for _, profile := range registry.devices {
		m.restartProviderLocked(profile)
	}
}

func (m *Manager) applyProviderRules(profileID string, revision int64, generation uint64, rules []string) {
	if m == nil {
		return
	}
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()

	runtime, ok := m.providers[profileID]
	if !ok || runtime.revision != revision || runtime.generation != generation {
		return
	}

	current := m.snapshot()
	profile := profileByID(current, profileID)
	if profile == nil || profile.Revision() != revision {
		return
	}

	updated := cloneProfileWithProviderRules(profile, rules, m.lookup)
	if updated == nil {
		return
	}

	var next *Registry
	if profile.Kind() == KindShared {
		next = current.CloneReplacingShared(updated)
	} else {
		next = current.CloneReplacingDevice(deviceIDFromProfileID(profileID), updated)
	}
	m.registry.Store(next)
}

func (m *Manager) applyProviderStatus(profileID string, revision int64, generation uint64, status ProviderStatus) {
	if m == nil {
		return
	}
	if status.UpdatedAt.IsZero() {
		status.UpdatedAt = time.Now().UTC()
	}
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()

	runtime, ok := m.providers[profileID]
	if !ok || runtime.revision != revision || runtime.generation != generation {
		return
	}
	runtime.status = status
	m.providers[profileID] = runtime
}
