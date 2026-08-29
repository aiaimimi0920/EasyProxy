package monitor

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"easy_proxies/internal/profile"
	"easy_proxies/internal/store"
)

func (s *Server) handleSharedProfile(w http.ResponseWriter, r *http.Request) {
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, apiError{Error: "profile_manager_unavailable"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		shared := profiles.SharedProfile()
		if shared == nil {
			s.writeLocalServerError(w, errLocalServerNotFound)
			return
		}
		writeJSON(w, profileResponse(profiles, shared, "shared", ""))
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
		result, err := s.updateSharedProfile(r.Context(), *req.Profile, expected)
		if err != nil {
			s.writeLocalServerError(w, err)
			return
		}
		writeJSON(w, profileMutationEnvelope(profiles, result, "shared", ""))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) updateSharedProfile(ctx context.Context, definition profile.Definition, expected int64) (profile.MutationResult, error) {
	profiles := s.profileManagerSnapshot()
	if profiles == nil {
		return profile.MutationResult{}, errors.New("profile manager is unavailable")
	}
	prepared, err := profiles.PrepareShared(definition, expected)
	if err != nil {
		return profile.MutationResult{}, fmt.Errorf("%w: %v", errLocalServerValidation, err)
	}
	release, err := s.beginLocalServerMutation(ctx)
	if err != nil {
		return profile.MutationResult{}, err
	}
	defer release()
	if err := profiles.ReserveShared(prepared); err != nil {
		return profile.MutationResult{}, err
	}
	defer profiles.AbortShared(prepared)

	s.cfgMu.RLock()
	cfg := s.cfgSrc
	s.cfgMu.RUnlock()
	if cfg == nil {
		return profile.MutationResult{}, errors.New("config storage is not initialized")
	}
	cfg.Lock()
	currentRevision := cfg.LocalServer.SharedRevision
	if currentRevision <= 0 {
		currentRevision = 1
	}
	if currentRevision != expected {
		cfg.Unlock()
		return profile.MutationResult{}, &store.RevisionConflictError{CurrentRevision: currentRevision}
	}
	candidate := cfg.Clone()
	if err := profile.ApplyDefinitionToRouting(prepared.Definition, &candidate.Routing); err != nil {
		cfg.Unlock()
		return profile.MutationResult{}, fmt.Errorf("%w: %v", errLocalServerValidation, err)
	}
	candidate.LocalServer.SharedRevision = currentRevision + 1
	candidate.Lock()
	err = candidate.SaveSettings()
	candidate.Unlock()
	if err != nil {
		cfg.Unlock()
		return profile.MutationResult{}, fmt.Errorf("save shared profile: %w", err)
	}
	cfg.Routing = candidate.Routing
	cfg.LocalServer.SharedRevision = candidate.LocalServer.SharedRevision
	cfg.Unlock()

	result := profiles.PublishShared(prepared)
	if result.Revision != candidate.LocalServer.SharedRevision {
		return profile.MutationResult{}, errors.New("shared profile publication lost its prepared revision")
	}
	if routing := s.routingSnapshot(); routing != nil {
		_ = routing.ApplyHot(candidate)
	}
	return result, nil
}
