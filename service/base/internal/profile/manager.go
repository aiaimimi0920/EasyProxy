package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/routerule"
	"easy_proxies/internal/store"
)

const (
	sharedProfileID   = "shared"
	deviceProfilePref = "device:"
)

type Option func(*Manager)

func WithProviderFactory(factory ProviderFactory) Option {
	return func(manager *Manager) {
		manager.providerFactory = factory
	}
}

type preparedConfigSnapshot struct {
	cfg                *config.Config
	shared             *CompiledProfile
	credentials        CredentialSnapshot
	localServerEnabled bool
}

type Manager struct {
	ctx      context.Context
	cancel   context.CancelFunc
	store    store.Store
	lookup   routerule.CountryLookup
	activity *DeviceActivityTracker

	mutationMu             sync.Mutex
	registry               atomic.Pointer[Registry]
	localServerEnabled     atomic.Bool
	providerFactory        ProviderFactory
	providers              map[string]providerRuntime
	nextProviderGeneration uint64
	prepared               *preparedConfigSnapshot
}

type MutationResult struct {
	Revision         int64
	RegistryRevision uint64
	Profile          *CompiledProfile
}

type PreparedShared struct {
	ExpectedRevision int64
	Profile          *CompiledProfile
	Definition       Definition
}

func NewManager(ctx context.Context, cfg *config.Config, st store.Store, opts ...Option) (*Manager, error) {
	if cfg == nil {
		return nil, errors.New("config is required")
	}
	if st == nil {
		return nil, errors.New("store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	childCtx, cancel := context.WithCancel(ctx)
	manager := &Manager{
		ctx:             childCtx,
		cancel:          cancel,
		store:           st,
		activity:        NewDeviceActivityTracker(),
		providerFactory: defaultProviderFactory,
		providers:       make(map[string]providerRuntime),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(manager)
		}
	}
	if manager.providerFactory == nil {
		manager.providerFactory = defaultProviderFactory
	}

	prepared, err := manager.prepareConfigSnapshot(cfg)
	if err != nil {
		cancel()
		return nil, err
	}
	devices, err := manager.loadDeviceProfiles(childCtx)
	if err != nil {
		cancel()
		return nil, err
	}
	mappings, err := manager.loadMappings(childCtx)
	if err != nil {
		cancel()
		return nil, err
	}

	registry := NewRegistry(prepared.shared, devices, mappings, prepared.credentials, 1)
	manager.registry.Store(registry)
	manager.localServerEnabled.Store(prepared.localServerEnabled)

	manager.mutationMu.Lock()
	manager.restartAllProvidersLocked(registry)
	manager.mutationMu.Unlock()

	return manager, nil
}

func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.cancel()
	m.mutationMu.Lock()
	for profileID, runtime := range m.providers {
		if runtime.runner != nil {
			runtime.runner.Stop()
		}
		delete(m.providers, profileID)
	}
	m.prepared = nil
	m.mutationMu.Unlock()
}

func (m *Manager) snapshot() *Registry {
	if m == nil {
		return nil
	}
	return m.registry.Load()
}

func (m *Manager) LocalServerEnabled() bool {
	if m == nil {
		return false
	}
	return m.localServerEnabled.Load()
}

func (m *Manager) Credentials() CredentialSnapshot {
	if m == nil {
		return CredentialSnapshot{}
	}
	current := m.snapshot()
	if current == nil {
		return CredentialSnapshot{}
	}
	return current.Credentials()
}

func (m *Manager) Resolve(identity RequestIdentity) Resolution {
	if m == nil {
		return Resolution{}
	}
	current := m.snapshot()
	if current == nil {
		return Resolution{}
	}
	return current.Resolve(identity)
}

func (m *Manager) Observe(resolution Resolution, peer netip.Addr, at time.Time) {
	if m == nil {
		return
	}
	m.activity.Observe(resolution, peer, at)
}

func (m *Manager) PrepareConfig(cfg *config.Config) error {
	if m == nil {
		return errors.New("manager is nil")
	}
	prepared, err := m.prepareConfigSnapshot(cfg)
	if err != nil {
		return err
	}
	m.mutationMu.Lock()
	m.prepared = prepared
	m.mutationMu.Unlock()
	return nil
}

func (m *Manager) PublishConfigSnapshot(cfg *config.Config) error {
	if m == nil {
		return errors.New("manager is nil")
	}
	prepared, err := m.prepareConfigSnapshot(cfg)
	if err != nil {
		return err
	}
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	return m.publishPreparedConfigLocked(prepared)
}

func (m *Manager) OnConfigUpdate(cfg *config.Config) {
	if m == nil {
		return
	}
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()

	prepared := m.prepared
	if prepared != nil && prepared.cfg == cfg {
		_ = m.publishPreparedConfigLocked(prepared)
		return
	}
	fallback, err := m.prepareConfigSnapshot(cfg)
	if err != nil {
		return
	}
	_ = m.publishPreparedConfigLocked(fallback)
}

func (m *Manager) SharedProfile() *CompiledProfile {
	current := m.snapshot()
	if current == nil {
		return nil
	}
	return current.SharedProfile()
}

func (m *Manager) DeviceProfile(deviceID string) *CompiledProfile {
	current := m.snapshot()
	if current == nil {
		return nil
	}
	return current.DeviceProfile(deviceID)
}

func (m *Manager) ProviderStatus(profileID string) ProviderStatus {
	if m == nil {
		return ProviderStatus{}
	}
	normalized := normalizeProfileID(profileID)
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	runtime, ok := m.providers[normalized]
	if !ok {
		return ProviderStatus{}
	}
	return runtime.status
}

func (m *Manager) ListDevices(ctx context.Context) ([]store.Device, error) {
	if m == nil {
		return nil, errors.New("manager is nil")
	}
	return m.store.ListDevices(ctx)
}

func (m *Manager) ListIPMappings(ctx context.Context) ([]store.DeviceIPMapping, error) {
	if m == nil {
		return nil, errors.New("manager is nil")
	}
	return m.store.ListDeviceIPMappings(ctx)
}

func (m *Manager) ActivitySnapshot() map[string]DeviceActivity {
	if m == nil {
		return map[string]DeviceActivity{}
	}
	return m.activity.Snapshot()
}

func (m *Manager) PutDevice(ctx context.Context, deviceID, displayName string, expected int64) (store.Device, error) {
	if m == nil {
		return store.Device{}, errors.New("manager is nil")
	}
	normalized, err := NormalizeDeviceID(deviceID)
	if err != nil {
		return store.Device{}, err
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
		return MutationResult{}, err
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
		return MutationResult{}, err
	}
	current, err := m.store.GetDeviceProfile(ctx, normalized)
	if err != nil {
		return MutationResult{}, err
	}
	if current == nil {
		return MutationResult{}, fmt.Errorf("device profile %q not found", normalized)
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
		return MutationResult{}, err
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
		return store.DeviceIPMapping{}, 0, err
	}
	mapping.DeviceID = normalizedDeviceID

	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()

	saved, err := m.store.PutDeviceIPMapping(ctx, mapping, expected)
	if err != nil {
		return store.DeviceIPMapping{}, 0, err
	}
	mappings, err := m.loadMappings(ctx)
	if err != nil {
		return store.DeviceIPMapping{}, 0, err
	}
	current := m.snapshot()
	next := current.CloneReplacingMappings(mappings)
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
	mappings, err := m.loadMappings(ctx)
	if err != nil {
		return 0, err
	}
	next := current.CloneReplacingMappings(mappings)
	m.registry.Store(next)
	return next.Revision(), nil
}

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

func (m *Manager) PublishShared(prepared *PreparedShared) MutationResult {
	if m == nil || prepared == nil || prepared.Profile == nil {
		return MutationResult{}
	}
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()

	current := m.snapshot()
	shared := current.SharedProfile()
	currentRevision := int64(0)
	if shared != nil {
		currentRevision = shared.Revision()
	}
	if currentRevision != prepared.ExpectedRevision {
		return mutationResult(current, currentRevision, shared)
	}
	next := current.CloneReplacingShared(prepared.Profile)
	m.registry.Store(next)
	m.restartProviderLocked(next.SharedProfile())
	return mutationResult(next, prepared.Profile.Revision(), prepared.Profile)
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
	return next.Revision()
}

func (m *Manager) prepareDefinition(profileID string, kind Kind, revision int64, definition Definition) (*CompiledProfile, []byte, error) {
	compiled, err := Compile(profileID, kind, revision, definition, m.lookup)
	if err != nil {
		return nil, nil, err
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
	return nil
}

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

func mutationResult(registry *Registry, revision int64, profile *CompiledProfile) MutationResult {
	result := MutationResult{
		Revision: revision,
		Profile:  profile,
	}
	if registry != nil {
		result.RegistryRevision = registry.Revision()
	}
	return result
}

func nextProfileRevision(expected int64) int64 {
	if expected <= 0 {
		return 1
	}
	return expected + 1
}

func deviceProfileID(deviceID string) string {
	return deviceProfilePref + normalizeDeviceID(deviceID)
}

func normalizeProfileID(profileID string) string {
	trimmed := strings.TrimSpace(profileID)
	switch {
	case trimmed == "":
		return ""
	case trimmed == sharedProfileID:
		return sharedProfileID
	case strings.HasPrefix(trimmed, deviceProfilePref):
		return deviceProfileID(strings.TrimPrefix(trimmed, deviceProfilePref))
	default:
		return deviceProfileID(trimmed)
	}
}

func deviceIDFromProfileID(profileID string) string {
	if strings.HasPrefix(profileID, deviceProfilePref) {
		return normalizeDeviceID(strings.TrimPrefix(profileID, deviceProfilePref))
	}
	return ""
}

func profileByID(registry *Registry, profileID string) *CompiledProfile {
	if registry == nil {
		return nil
	}
	if profileID == sharedProfileID {
		return registry.SharedProfile()
	}
	if deviceID := deviceIDFromProfileID(profileID); deviceID != "" {
		return registry.DeviceProfile(deviceID)
	}
	return nil
}

func cloneProfileWithProviderRules(profile *CompiledProfile, providerRules []string, lookup routerule.CountryLookup) *CompiledProfile {
	if profile == nil {
		return nil
	}
	combinedRules := cloneStringSlice(profile.baseRules)
	combinedRules = append(combinedRules, providerRules...)
	cloned := *profile
	cloned.baseRules = cloneStringSlice(profile.baseRules)
	cloned.providerSpecs = cloneProviderSpecs(profile.providerSpecs)
	finalPolicy := profile.finalPolicy
	if finalPolicy == "" {
		finalPolicy = profile.FinalPolicy()
	}
	cloned.engine = routerule.New(combinedRules, finalPolicy, lookup)
	return &cloned
}
