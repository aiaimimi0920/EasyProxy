package profile

import (
	"errors"

	"easy_proxies/internal/store"
)

func (m *Manager) PrepareShared(definition Definition, expected int64) (*PreparedShared, error) {
	if m == nil {
		return nil, errors.New("manager is nil")
	}
	compiled, _, err := m.prepareDefinition(sharedProfileID, KindShared, nextProfileRevision(expected), definition)
	if err != nil {
		return nil, err
	}
	return &PreparedShared{
		ExpectedRevision: expected,
		Profile:          compiled,
		Definition:       compiled.Definition(),
	}, nil
}

func (m *Manager) ReserveShared(prepared *PreparedShared) error {
	if m == nil {
		return errors.New("manager is nil")
	}
	if prepared == nil || prepared.Profile == nil {
		return errors.New("prepared shared profile is required")
	}
	m.mutationMu.Lock()
	current := m.snapshot()
	currentRevision := int64(0)
	if current != nil && current.SharedProfile() != nil {
		currentRevision = current.SharedProfile().Revision()
	}
	if currentRevision != prepared.ExpectedRevision {
		m.mutationMu.Unlock()
		return &store.RevisionConflictError{CurrentRevision: currentRevision}
	}
	prepared.manager = m
	return nil
}

func (m *Manager) PublishShared(prepared *PreparedShared) MutationResult {
	if m == nil || prepared == nil || prepared.Profile == nil || prepared.manager != m {
		return MutationResult{}
	}
	defer prepared.releaseReservation()

	current := m.snapshot()
	next := current.CloneReplacingShared(prepared.Profile)
	m.registry.Store(next)
	m.restartProviderLocked(next.SharedProfile())
	return mutationResult(next, prepared.Profile.Revision(), prepared.Profile)
}

func (m *Manager) AbortShared(prepared *PreparedShared) {
	if prepared != nil && prepared.manager == m {
		prepared.releaseReservation()
	}
}

func (p *PreparedShared) releaseReservation() {
	if p == nil {
		return
	}
	p.release.Do(func() {
		if p.manager != nil {
			p.manager.mutationMu.Unlock()
		}
	})
}

func (m *Manager) PublishCredentials(snapshot CredentialSnapshot) uint64 {
	if m == nil {
		return 0
	}
	if snapshot.Generation == 0 {
		snapshot.Generation = 1
	}
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	current := m.snapshot()
	next := current.CloneReplacingCredentials(snapshot)
	m.registry.Store(next)
	m.cleanupSessionsBeforeGeneration(snapshot.Generation)
	return next.Revision()
}
