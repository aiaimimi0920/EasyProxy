package profile

import (
	"context"
	"errors"
	"strings"

	"easy_proxies/internal/config"
)

func (m *Manager) prepareConfigSnapshot(cfg *config.Config) (*preparedConfigSnapshot, error) {
	if cfg == nil {
		return nil, errors.New("config is nil")
	}
	sharedRevision := cfg.LocalServer.SharedRevision
	if sharedRevision <= 0 {
		sharedRevision = 1
	}
	shared, err := Compile(sharedProfileID, KindShared, sharedRevision, DefinitionFromRouting(cfg.Routing), m.lookup)
	if err != nil {
		return nil, err
	}

	credentials := CredentialSnapshot{
		Username:   strings.TrimSpace(cfg.LocalServer.Auth.Username),
		Password:   cfg.LocalServer.Auth.Password,
		Generation: cfg.LocalServer.CredentialGeneration,
	}
	if credentials.Username == "" {
		credentials.Username = "easyproxy"
	}
	if credentials.Generation == 0 {
		credentials.Generation = 1
	}

	return &preparedConfigSnapshot{
		cfg:                cfg,
		shared:             shared,
		credentials:        credentials,
		localServerEnabled: cfg.LocalServer.Enabled,
	}, nil
}

func (m *Manager) publishPreparedConfigLocked(prepared *preparedConfigSnapshot) error {
	if prepared == nil {
		return errors.New("prepared config is nil")
	}
	current := m.snapshot()
	if current == nil {
		return errors.New("registry is not initialized")
	}
	next := &Registry{
		shared:      prepared.shared,
		devices:     cloneDeviceMap(current.devices),
		mappings:    cloneMappings(current.mappings),
		credentials: prepared.credentials,
		revision:    current.revision + 1,
	}
	m.registry.Store(next)
	m.localServerEnabled.Store(prepared.localServerEnabled)
	m.prepared = nil
	m.restartProviderLocked(next.SharedProfile())
	m.cleanupSessionsBeforeGeneration(prepared.credentials.Generation)
	return nil
}

func (m *Manager) cleanupSessionsBeforeGeneration(generation uint64) {
	if m == nil || m.store == nil || generation == 0 {
		return
	}
	storeRef := m.store
	go func() {
		_ = storeRef.DeleteSessionsBeforeGeneration(context.Background(), generation)
	}()
}
