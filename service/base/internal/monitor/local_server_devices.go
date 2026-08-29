package monitor

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"easy_proxies/internal/profile"
	"easy_proxies/internal/store"
)

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, apiError{Error: "profile_manager_unavailable"})
		return
	}
	devices, err := profiles.ListDevices(r.Context())
	if err != nil {
		s.writeLocalServerError(w, err)
		return
	}
	mappings, err := profiles.ListIPMappings(r.Context())
	if err != nil {
		s.writeLocalServerError(w, err)
		return
	}
	activity := profiles.ActivitySnapshot()
	responses := make([]deviceSummaryResponse, 0, len(devices)+len(activity))
	persisted := make(map[string]struct{}, len(devices))
	for _, device := range devices {
		persisted[device.DeviceID] = struct{}{}
		responses = append(responses, buildDeviceSummary(profiles, device, mappings, activity))
	}
	activityOnly := make([]string, 0)
	for deviceID := range activity {
		if _, ok := persisted[deviceID]; !ok {
			activityOnly = append(activityOnly, deviceID)
		}
	}
	sort.Strings(activityOnly)
	for _, deviceID := range activityOnly {
		responses = append(responses, buildDeviceSummary(profiles, store.Device{
			DeviceID:    deviceID,
			DisplayName: deviceID,
		}, mappings, activity))
	}
	writeJSON(w, map[string]any{"devices": responses})
}

func (s *Server) handleDeviceResource(w http.ResponseWriter, r *http.Request) {
	deviceID, action, err := parseDeviceResourcePath(r)
	if err != nil {
		s.writeLocalServerError(w, err)
		return
	}
	switch action {
	case "":
		s.handleDeviceItem(w, r, deviceID)
	case "profile":
		s.handleDeviceProfile(w, r, deviceID)
	case "profile/enabled":
		s.handleDeviceProfileEnabled(w, r, deviceID)
	case "profile/copy-shared":
		s.handleDeviceProfileCopyShared(w, r, deviceID)
	default:
		s.writeLocalServerError(w, errLocalServerNotFound)
	}
}

func (s *Server) handleDeviceItem(w http.ResponseWriter, r *http.Request, deviceID string) {
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, apiError{Error: "profile_manager_unavailable"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		device, err := profiles.GetDevice(r.Context(), deviceID)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		activity := profiles.ActivitySnapshot()
		if device == nil {
			if _, ok := activity[deviceID]; !ok {
				s.writeLocalServerError(w, errLocalServerNotFound)
				return
			}
			device = &store.Device{DeviceID: deviceID, DisplayName: deviceID}
		}
		mappings, err := profiles.ListIPMappings(r.Context())
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		writeJSON(w, buildDeviceResource(profiles, *device, mappings, activity))
	case http.MethodPut:
		var req deviceMutationRequest
		if err := decodeJSONBody(r, &req); err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		expected, err := expectedRevision(r, req.ExpectedRevision)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		release, err := s.beginLocalServerMutation(r.Context())
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		mappings, err := profiles.ListIPMappings(r.Context())
		if err != nil {
			release()
			s.writeLocalServerError(w, err)
			return
		}
		device, err := profiles.PutDevice(r.Context(), deviceID, strings.TrimSpace(req.DisplayName), expected)
		release()
		if err != nil {
			s.writeLocalServerError(w, classifyLocalServerValidation(err))
			return
		}
		writeJSON(w, mutationEnvelope{
			Revision:         device.Revision,
			RegistryRevision: profiles.RuntimeStatus().RegistryRevision,
			NeedReload:       false,
			Resource:         buildDeviceResource(profiles, device, mappings, profiles.ActivitySnapshot()),
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleDeviceProfile(w http.ResponseWriter, r *http.Request, deviceID string) {
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, apiError{Error: "profile_manager_unavailable"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		selected := profiles.DeviceProfile(deviceID)
		scope := "device"
		if selected == nil {
			selected = profiles.SharedProfile()
			scope = "shared"
		}
		if selected == nil {
			s.writeLocalServerError(w, errLocalServerNotFound)
			return
		}
		writeJSON(w, profileResponse(profiles, selected, scope, deviceID))
	case http.MethodPut:
		var req profileMutationRequest
		if err := decodeJSONBody(r, &req); err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		if req.Profile == nil {
			s.writeLocalServerError(w, fmt.Errorf("%w: profile is required", errLocalServerValidation))
			return
		}
		expected, err := expectedRevision(r, req.ExpectedRevision)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		release, err := s.beginLocalServerMutation(r.Context())
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		result, err := profiles.PutDeviceProfile(r.Context(), deviceID, *req.Profile, expected)
		release()
		if err != nil {
			s.writeLocalServerError(w, classifyLocalServerValidation(err))
			return
		}
		writeJSON(w, profileMutationEnvelope(profiles, result, "device", deviceID))
	case http.MethodDelete:
		expected, err := expectedRevisionFromOptionalBody(r)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		release, err := s.beginLocalServerMutation(r.Context())
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		result, err := profiles.DeleteDeviceProfile(r.Context(), deviceID, expected)
		release()
		if err != nil {
			s.writeLocalServerError(w, classifyLocalServerValidation(err))
			return
		}
		shared := profiles.SharedProfile()
		if shared == nil {
			s.writeLocalServerError(w, errLocalServerNotFound)
			return
		}
		writeJSON(w, mutationEnvelope{
			Revision:         shared.Revision(),
			RegistryRevision: result.RegistryRevision,
			NeedReload:       false,
			ProfileScope:     "shared",
			Resource:         profileResponse(profiles, shared, "shared", ""),
			Message:          "device profile deleted",
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleDeviceProfileEnabled(w http.ResponseWriter, r *http.Request, deviceID string) {
	if r.Method != http.MethodPatch {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, apiError{Error: "profile_manager_unavailable"})
		return
	}
	var req enabledMutationRequest
	if err := decodeJSONBody(r, &req); err != nil {
		s.writeLocalServerError(w, err)
		return
	}
	if req.Enabled == nil {
		s.writeLocalServerError(w, fmt.Errorf("%w: enabled is required", errLocalServerValidation))
		return
	}
	expected, err := expectedRevision(r, req.ExpectedRevision)
	if err != nil {
		s.writeLocalServerError(w, err)
		return
	}
	release, err := s.beginLocalServerMutation(r.Context())
	if err != nil {
		s.writeLocalServerError(w, err)
		return
	}
	result, err := profiles.SetDeviceProfileEnabled(r.Context(), deviceID, *req.Enabled, expected)
	release()
	if err != nil {
		if errors.Is(err, profile.ErrDeviceProfileNotFound) {
			err = errLocalServerNotFound
		}
		s.writeLocalServerError(w, classifyLocalServerValidation(err))
		return
	}
	writeJSON(w, profileMutationEnvelope(profiles, result, "device", deviceID))
}

func (s *Server) handleDeviceProfileCopyShared(w http.ResponseWriter, r *http.Request, deviceID string) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, apiError{Error: "profile_manager_unavailable"})
		return
	}
	if current := profiles.DeviceProfile(deviceID); current != nil {
		s.writeLocalServerError(w, &store.RevisionConflictError{CurrentRevision: current.Revision()})
		return
	}
	release, err := s.beginLocalServerMutation(r.Context())
	if err != nil {
		s.writeLocalServerError(w, err)
		return
	}
	result, err := profiles.CopySharedProfile(r.Context(), deviceID)
	release()
	if err != nil {
		s.writeLocalServerError(w, classifyLocalServerValidation(err))
		return
	}
	writeJSON(w, profileMutationEnvelope(profiles, result, "device", deviceID))
}
