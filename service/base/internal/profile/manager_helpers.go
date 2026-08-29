package profile

import (
	"strings"

	"easy_proxies/internal/routerule"
)

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
