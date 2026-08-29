package monitor

import (
	"easy_proxies/internal/profile"
	"easy_proxies/internal/store"
)

func profileResponse(manager *profile.Manager, compiled *profile.CompiledProfile, scope, deviceID string) profileResourceResponse {
	status := manager.RuntimeStatus()
	profileID := "shared"
	if scope == "device" {
		profileID = deviceID
	}
	return profileResourceResponse{
		ProfileScope:     scope,
		DeviceID:         deviceID,
		Revision:         compiled.Revision(),
		RegistryRevision: status.RegistryRevision,
		NeedReload:       false,
		Profile:          profileDefinitionForAPI(compiled.Definition()),
		ProviderStatus:   manager.ProviderStatus(profileID),
	}
}

func profileDefinitionForAPI(definition profile.Definition) profile.Definition {
	if definition.Rules == nil {
		definition.Rules = []string{}
	}
	if definition.RuleProviders == nil {
		definition.RuleProviders = []profile.RuleProvider{}
	}
	if definition.NodeFilter.Countries == nil {
		definition.NodeFilter.Countries = []string{}
	}
	if definition.NodeFilter.Regions == nil {
		definition.NodeFilter.Regions = []string{}
	}
	return definition
}

func profileMutationResponse(manager *profile.Manager, result profile.MutationResult, scope, deviceID string) profileResourceResponse {
	compiled := result.Profile
	if compiled == nil {
		if scope == "device" {
			compiled = manager.DeviceProfile(deviceID)
		} else {
			compiled = manager.SharedProfile()
		}
	}
	if compiled == nil {
		return profileResourceResponse{ProfileScope: scope, DeviceID: deviceID, RegistryRevision: result.RegistryRevision}
	}
	response := profileResponse(manager, compiled, scope, deviceID)
	response.RegistryRevision = result.RegistryRevision
	return response
}

func profileMutationEnvelope(manager *profile.Manager, result profile.MutationResult, scope, deviceID string) mutationEnvelope {
	return mutationEnvelope{
		Revision:         result.Revision,
		RegistryRevision: result.RegistryRevision,
		NeedReload:       false,
		ProfileScope:     scope,
		Resource:         profileMutationResponse(manager, result, scope, deviceID),
	}
}

func buildDeviceSummary(manager *profile.Manager, device store.Device, mappings []store.DeviceIPMapping, activity map[string]profile.DeviceActivity) deviceSummaryResponse {
	compiled := manager.DeviceProfile(device.DeviceID)
	mode := "independent"
	if compiled == nil {
		compiled = manager.SharedProfile()
		mode = "shared"
	}
	response := deviceSummaryResponse{
		DeviceID:        device.DeviceID,
		DisplayName:     device.DisplayName,
		Revision:        device.Revision,
		ProfileMode:     mode,
		EffectiveState:  "DIRECT",
		ProfileRevision: 0,
	}
	if compiled != nil {
		response.ProfileRevision = compiled.Revision()
		response.EffectiveEnabled = compiled.Enabled()
		if compiled.Enabled() {
			response.EffectiveState = "PROFILE"
		}
	}
	for _, mapping := range mappings {
		if mapping.DeviceID == device.DeviceID {
			response.MappingCount++
		}
	}
	if observed, ok := activity[device.DeviceID]; ok {
		response.IdentitySource = string(observed.Source)
		if observed.LastSeenIP.IsValid() {
			response.LastSeenIP = observed.LastSeenIP.String()
		}
		if !observed.LastSeenAt.IsZero() {
			seenAt := observed.LastSeenAt
			response.LastSeenAt = &seenAt
		}
	}
	return response
}

func buildDeviceResource(manager *profile.Manager, device store.Device, mappings []store.DeviceIPMapping, activity map[string]profile.DeviceActivity) deviceResourceResponse {
	response := deviceResourceResponse{
		deviceSummaryResponse: buildDeviceSummary(manager, device, mappings, activity),
		Mappings:              make([]ipMappingResponse, 0),
	}
	selected := manager.DeviceProfile(device.DeviceID)
	scope := "device"
	if selected == nil {
		selected = manager.SharedProfile()
		scope = "shared"
	}
	if selected != nil {
		profileResource := profileResponse(manager, selected, scope, device.DeviceID)
		if scope == "shared" {
			profileResource.DeviceID = ""
		}
		response.Profile = &profileResource
	}
	for _, mapping := range mappings {
		if mapping.DeviceID == device.DeviceID {
			response.Mappings = append(response.Mappings, ipMappingResponseFromStore(mapping))
		}
	}
	return response
}

func ipMappingResponses(mappings []store.DeviceIPMapping) []ipMappingResponse {
	responses := make([]ipMappingResponse, 0, len(mappings))
	for _, mapping := range mappings {
		responses = append(responses, ipMappingResponseFromStore(mapping))
	}
	return responses
}

func ipMappingResponseFromStore(mapping store.DeviceIPMapping) ipMappingResponse {
	return ipMappingResponse{
		MappingID: mapping.MappingID,
		CIDR:      mapping.CIDR,
		DeviceID:  mapping.DeviceID,
		Priority:  mapping.Priority,
		Enabled:   mapping.Enabled,
		Revision:  mapping.Revision,
	}
}
