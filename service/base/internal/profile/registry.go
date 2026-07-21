package profile

import (
	"errors"
	"net/netip"
	"sort"
	"strings"
)

type CredentialSnapshot struct {
	Username   string
	Password   string
	Generation uint64
}

type RequestIdentity struct {
	ExplicitDeviceID string
	PeerIP           netip.Addr
}

type IdentitySource string

const (
	IdentityExplicit       IdentitySource = "explicit"
	IdentityIPMapping      IdentitySource = "ip_mapping"
	IdentitySharedFallback IdentitySource = "shared_fallback"
)

type IPMapping struct {
	MappingID string
	Prefix    netip.Prefix
	DeviceID  string
	Priority  int
}

type Resolution struct {
	DeviceID        string
	Source          IdentitySource
	ProfileID       string
	ProfileRevision int64
	Profile         *CompiledProfile
}

type Registry struct {
	shared      *CompiledProfile
	devices     map[string]*CompiledProfile
	mappings    []IPMapping
	credentials CredentialSnapshot
	revision    uint64
}

func NewRegistry(shared *CompiledProfile, devices map[string]*CompiledProfile, mappings []IPMapping, credentials CredentialSnapshot, revision uint64) *Registry {
	clonedDevices := make(map[string]*CompiledProfile, len(devices))
	for deviceID, profile := range devices {
		clonedDevices[normalizeDeviceID(deviceID)] = profile
	}
	clonedMappings := cloneMappings(mappings)
	sortMappings(clonedMappings)
	return &Registry{
		shared:      shared,
		devices:     clonedDevices,
		mappings:    clonedMappings,
		credentials: credentials,
		revision:    revision,
	}
}

func (r *Registry) SharedProfile() *CompiledProfile {
	if r == nil {
		return nil
	}
	return r.shared
}

func (r *Registry) DeviceProfile(deviceID string) *CompiledProfile {
	if r == nil {
		return nil
	}
	return r.devices[normalizeDeviceID(deviceID)]
}

func (r *Registry) Credentials() CredentialSnapshot {
	if r == nil {
		return CredentialSnapshot{}
	}
	return r.credentials
}

func (r *Registry) Revision() uint64 {
	if r == nil {
		return 0
	}
	return r.revision
}

func (r *Registry) ProfileCount() int {
	if r == nil {
		return 0
	}
	count := len(r.devices)
	if r.shared != nil {
		count++
	}
	return count
}

func (r *Registry) MappingCount() int {
	if r == nil {
		return 0
	}
	return len(r.mappings)
}

func (r *Registry) Resolve(identity RequestIdentity) Resolution {
	if r == nil {
		return Resolution{}
	}
	explicitID := normalizeDeviceID(identity.ExplicitDeviceID)
	if explicitID != "" {
		profile := r.devices[explicitID]
		if profile == nil {
			profile = r.shared
		}
		return resolutionForProfile(explicitID, IdentityExplicit, profile)
	}

	if identity.PeerIP.IsValid() {
		for _, mapping := range r.mappings {
			if mapping.Prefix.Contains(identity.PeerIP) {
				profile := r.devices[mapping.DeviceID]
				if profile == nil {
					profile = r.shared
				}
				return resolutionForProfile(mapping.DeviceID, IdentityIPMapping, profile)
			}
		}
	}

	return resolutionForProfile("", IdentitySharedFallback, r.shared)
}

func (r *Registry) CloneReplacingDevice(deviceID string, profile *CompiledProfile) *Registry {
	if r == nil {
		return nil
	}
	clonedDevices := cloneDeviceMap(r.devices)
	normalizedDeviceID := normalizeDeviceID(deviceID)
	if profile == nil {
		delete(clonedDevices, normalizedDeviceID)
	} else {
		clonedDevices[normalizedDeviceID] = profile
	}
	return &Registry{
		shared:      r.shared,
		devices:     clonedDevices,
		mappings:    cloneMappings(r.mappings),
		credentials: r.credentials,
		revision:    r.revision + 1,
	}
}

func (r *Registry) CloneReplacingMappings(mappings []IPMapping) *Registry {
	if r == nil {
		return nil
	}
	clonedMappings := cloneMappings(mappings)
	sortMappings(clonedMappings)
	return &Registry{
		shared:      r.shared,
		devices:     cloneDeviceMap(r.devices),
		mappings:    clonedMappings,
		credentials: r.credentials,
		revision:    r.revision + 1,
	}
}

func (r *Registry) CloneReplacingMapping(mappingID string, mapping *IPMapping) *Registry {
	if r == nil {
		return nil
	}
	normalizedID := strings.TrimSpace(mappingID)
	clonedMappings := make([]IPMapping, 0, len(r.mappings)+1)
	for _, current := range r.mappings {
		if current.MappingID != normalizedID {
			clonedMappings = append(clonedMappings, current)
		}
	}
	if mapping != nil {
		clonedMappings = append(clonedMappings, *mapping)
	}
	clonedMappings = cloneMappings(clonedMappings)
	sortMappings(clonedMappings)
	return &Registry{
		shared:      r.shared,
		devices:     cloneDeviceMap(r.devices),
		mappings:    clonedMappings,
		credentials: r.credentials,
		revision:    r.revision + 1,
	}
}

func (r *Registry) CloneReplacingShared(shared *CompiledProfile) *Registry {
	if r == nil {
		return nil
	}
	return &Registry{
		shared:      shared,
		devices:     cloneDeviceMap(r.devices),
		mappings:    cloneMappings(r.mappings),
		credentials: r.credentials,
		revision:    r.revision + 1,
	}
}

func (r *Registry) CloneReplacingCredentials(credentials CredentialSnapshot) *Registry {
	if r == nil {
		return nil
	}
	return &Registry{
		shared:      r.shared,
		devices:     cloneDeviceMap(r.devices),
		mappings:    cloneMappings(r.mappings),
		credentials: credentials,
		revision:    r.revision + 1,
	}
}

func cloneDeviceMap(devices map[string]*CompiledProfile) map[string]*CompiledProfile {
	if devices == nil {
		return map[string]*CompiledProfile{}
	}
	cloned := make(map[string]*CompiledProfile, len(devices))
	for deviceID, profile := range devices {
		cloned[normalizeDeviceID(deviceID)] = profile
	}
	return cloned
}

func cloneMappings(mappings []IPMapping) []IPMapping {
	if mappings == nil {
		return nil
	}
	cloned := make([]IPMapping, len(mappings))
	for idx, mapping := range mappings {
		mapping.MappingID = strings.TrimSpace(mapping.MappingID)
		mapping.DeviceID = normalizeDeviceID(mapping.DeviceID)
		if mapping.Prefix.IsValid() {
			mapping.Prefix = mapping.Prefix.Masked()
		}
		cloned[idx] = mapping
	}
	return cloned
}

func sortMappings(mappings []IPMapping) {
	sort.SliceStable(mappings, func(i, j int) bool {
		left := mappings[i]
		right := mappings[j]
		if left.Prefix.Bits() != right.Prefix.Bits() {
			return left.Prefix.Bits() > right.Prefix.Bits()
		}
		if left.Priority != right.Priority {
			return left.Priority > right.Priority
		}
		return left.MappingID < right.MappingID
	})
}

func resolutionForProfile(deviceID string, source IdentitySource, profile *CompiledProfile) Resolution {
	resolution := Resolution{
		DeviceID: deviceID,
		Source:   source,
		Profile:  profile,
	}
	if profile != nil {
		resolution.ProfileID = profile.ID()
		resolution.ProfileRevision = profile.Revision()
	}
	return resolution
}

// NormalizeDeviceID canonicalizes a device identifier for persistence and
// request handling.
func NormalizeDeviceID(deviceID string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(deviceID))
	if len(normalized) == 0 || len(normalized) > 64 {
		return "", errors.New("device_id length must be 1-64")
	}
	for _, r := range normalized {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-') {
			return "", errors.New("device_id contains invalid characters")
		}
	}
	return normalized, nil
}

func normalizeDeviceID(deviceID string) string {
	normalized, err := NormalizeDeviceID(deviceID)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(deviceID))
	}
	return normalized
}
