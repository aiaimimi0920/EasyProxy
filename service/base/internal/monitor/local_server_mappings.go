package monitor

import (
	"fmt"
	"net/http"

	"easy_proxies/internal/store"
)

func (s *Server) handleIPMappings(w http.ResponseWriter, r *http.Request) {
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, apiError{Error: "profile_manager_unavailable"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		mappings, err := profiles.ListIPMappings(r.Context())
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		writeJSON(w, map[string]any{"mappings": ipMappingResponses(mappings)})
	case http.MethodPost:
		var req mappingMutationRequest
		if err := decodeJSONBody(r, &req); err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		expected, err := expectedRevision(r, req.ExpectedRevision)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		if expected != 0 {
			s.writeLocalServerError(w, fmt.Errorf("%w: mapping create requires expected_revision 0", errLocalServerValidation))
			return
		}
		cidr, err := normalizeMappingCIDR(req.CIDR)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		mappingID, err := newMappingID()
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		release, err := s.beginLocalServerMutation(r.Context())
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		mapping, registryRevision, err := profiles.PutIPMapping(r.Context(), store.DeviceIPMapping{
			MappingID: mappingID,
			CIDR:      cidr,
			DeviceID:  req.DeviceID,
			Priority:  req.Priority,
			Enabled:   req.Enabled,
		}, 0)
		release()
		if err != nil {
			s.writeLocalServerError(w, classifyLocalServerValidation(err))
			return
		}
		writeJSON(w, mutationEnvelope{
			Revision:         mapping.Revision,
			RegistryRevision: registryRevision,
			NeedReload:       false,
			Resource:         ipMappingResponseFromStore(mapping),
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleIPMappingResource(w http.ResponseWriter, r *http.Request) {
	mappingID, err := parseMappingResourcePath(r)
	if err != nil {
		s.writeLocalServerError(w, err)
		return
	}
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, apiError{Error: "profile_manager_unavailable"})
		return
	}
	switch r.Method {
	case http.MethodPut:
		var req mappingMutationRequest
		if err := decodeJSONBody(r, &req); err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		expected, err := expectedRevision(r, req.ExpectedRevision)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		cidr, err := normalizeMappingCIDR(req.CIDR)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		release, err := s.beginLocalServerMutation(r.Context())
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		mapping, registryRevision, err := profiles.PutIPMapping(r.Context(), store.DeviceIPMapping{
			MappingID: mappingID,
			CIDR:      cidr,
			DeviceID:  req.DeviceID,
			Priority:  req.Priority,
			Enabled:   req.Enabled,
		}, expected)
		release()
		if err != nil {
			s.writeLocalServerError(w, classifyLocalServerValidation(err))
			return
		}
		writeJSON(w, mutationEnvelope{
			Revision:         mapping.Revision,
			RegistryRevision: registryRevision,
			NeedReload:       false,
			Resource:         ipMappingResponseFromStore(mapping),
		})
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
		current, err := profiles.GetIPMapping(r.Context(), mappingID)
		if err != nil {
			release()
			s.writeLocalServerError(w, err)
			return
		}
		registryRevision, err := profiles.DeleteIPMapping(r.Context(), mappingID, expected)
		release()
		if err != nil {
			s.writeLocalServerError(w, classifyLocalServerValidation(err))
			return
		}
		revision := expected
		var resource any
		if current != nil {
			revision = current.Revision
			resource = ipMappingResponseFromStore(*current)
		}
		writeJSON(w, mutationEnvelope{
			Revision:         revision,
			RegistryRevision: registryRevision,
			NeedReload:       false,
			Resource:         resource,
			Message:          "mapping deleted",
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
