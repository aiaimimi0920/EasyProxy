package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"easy_proxies/internal/config"
	"easy_proxies/internal/profile"
)

func (s *Server) localServerSettingsMode() bool {
	return s.localServerConfigured() || s.localServerCompatEnabled()
}

type localServerConfigUpdate struct {
	Enabled      *bool   `json:"enabled,omitempty"`
	Listen       *string `json:"listen,omitempty"`
	AuthUsername *string `json:"auth_username,omitempty"`
	AuthPassword *string `json:"auth_password,omitempty"`
}

type localServerConfigView struct {
	Enabled              bool   `json:"enabled"`
	Listen               string `json:"listen"`
	AuthUsername         string `json:"auth_username"`
	PasswordSet          bool   `json:"password_set"`
	CredentialGeneration uint64 `json:"credential_generation"`
	SharedRevision       int64  `json:"shared_revision"`
}

func (s *Server) handleLocalServerConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.getLocalServerConfig())
	case http.MethodPut:
		var req localServerConfigUpdate
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			writeJSON(w, map[string]any{"error": "invalid request body: " + err.Error()})
			return
		}
		releaseBarrier, err := s.beginConfigMutation(r.Context())
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		view, needReload, err := s.updateLocalServerConfig(req)
		releaseBarrier()
		if err != nil {
			switch {
			case errors.Is(err, errReloadInProgress), errors.Is(err, errLocalServerCredentialConflict):
				w.WriteHeader(http.StatusConflict)
			case errors.Is(err, errInvalidLocalServerConfig):
				w.WriteHeader(http.StatusUnprocessableEntity)
			default:
				w.WriteHeader(http.StatusInternalServerError)
			}
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		registryRevision := uint64(0)
		if profiles := s.profileManagerSnapshot(); profiles != nil {
			registryRevision = profiles.RuntimeStatus().RegistryRevision
		}
		writeJSON(w, mutationEnvelope{
			Revision:         view.SharedRevision,
			RegistryRevision: registryRevision,
			NeedReload:       needReload,
			Resource:         view,
		})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) getLocalServerConfig() localServerConfigView {
	s.cfgMu.RLock()
	c := s.cfgSrc
	s.cfgMu.RUnlock()
	if c == nil {
		return localServerConfigView{}
	}
	c.RLock()
	defer c.RUnlock()
	return localServerConfigViewFromConfig(c)
}

func localServerConfigViewFromConfig(c *config.Config) localServerConfigView {
	if c == nil {
		return localServerConfigView{}
	}
	generation := c.LocalServer.CredentialGeneration
	if generation == 0 {
		generation = 1
	}
	sharedRevision := c.LocalServer.SharedRevision
	if sharedRevision == 0 {
		sharedRevision = 1
	}
	return localServerConfigView{
		Enabled:              c.LocalServer.Enabled,
		Listen:               strings.TrimSpace(c.LocalServer.Listen),
		AuthUsername:         strings.TrimSpace(c.LocalServer.Auth.Username),
		PasswordSet:          c.LocalServer.Auth.Password != "",
		CredentialGeneration: generation,
		SharedRevision:       sharedRevision,
	}
}

func (s *Server) updateLocalServerConfig(req localServerConfigUpdate) (localServerConfigView, bool, error) {
	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()

	s.cfgMu.RLock()
	c := s.cfgSrc
	s.cfgMu.RUnlock()
	if c == nil {
		return localServerConfigView{}, false, errors.New("config storage is not initialized")
	}
	if s.reloadWindowCount > 0 {
		return localServerConfigView{}, false, errReloadInProgress
	}

	c.Lock()
	pendingReload := s.localServerReloadPending
	currentEnabled := c.LocalServer.Enabled
	currentListen := strings.TrimSpace(c.LocalServer.Listen)
	currentUsername := strings.TrimSpace(c.LocalServer.Auth.Username)
	currentPassword := c.LocalServer.Auth.Password
	candidate := c.Clone()

	if req.Enabled != nil {
		candidate.LocalServer.Enabled = *req.Enabled
	}
	if req.Listen != nil {
		candidate.LocalServer.Listen = strings.TrimSpace(*req.Listen)
	}
	if req.AuthUsername != nil {
		candidate.LocalServer.Auth.Username = strings.TrimSpace(*req.AuthUsername)
	} else if candidate.LocalServer.Enabled && strings.TrimSpace(candidate.LocalServer.Auth.Username) == "" {
		legacyUsername := strings.TrimSpace(candidate.Listener.Username)
		if validLocalServerUsername(legacyUsername) {
			candidate.LocalServer.Auth.Username = legacyUsername
		} else {
			candidate.LocalServer.Auth.Username = "easyproxy"
		}
	}
	if req.AuthPassword != nil {
		if *req.AuthPassword == "" {
			c.Unlock()
			return localServerConfigView{}, false, fmt.Errorf("%w: auth_password must not be empty", errInvalidLocalServerConfig)
		}
		candidate.LocalServer.Auth.Password = *req.AuthPassword
	} else if candidate.LocalServer.Enabled && candidate.LocalServer.Auth.Password == "" {
		listenerPassword := candidate.Listener.Password
		managementPassword := candidate.Management.Password
		switch {
		case listenerPassword != "" && managementPassword != "" && listenerPassword != managementPassword:
			c.Unlock()
			return localServerConfigView{}, false, fmt.Errorf("%w: legacy listener and management passwords differ", errLocalServerCredentialConflict)
		case listenerPassword != "":
			candidate.LocalServer.Auth.Password = listenerPassword
		case managementPassword != "":
			candidate.LocalServer.Auth.Password = managementPassword
		}
	}

	if err := validateLocalServerConfigCandidate(candidate, req.AuthUsername != nil); err != nil {
		c.Unlock()
		return localServerConfigView{}, false, err
	}

	credentialChanged := currentUsername != candidate.LocalServer.Auth.Username ||
		currentPassword != candidate.LocalServer.Auth.Password
	enabledCredentialChange := !currentEnabled && candidate.LocalServer.Enabled
	if credentialChanged || enabledCredentialChange {
		generation := c.LocalServer.CredentialGeneration
		if generation == 0 {
			generation = 1
		}
		if generation == ^uint64(0) {
			c.Unlock()
			return localServerConfigView{}, false, fmt.Errorf("%w: credential generation overflow", errInvalidLocalServerConfig)
		}
		candidate.LocalServer.CredentialGeneration = generation + 1
	}

	if candidate.LocalServer.Auth.Username != "" {
		candidate.Listener.Username = candidate.LocalServer.Auth.Username
	}
	if candidate.LocalServer.Auth.Password != "" {
		candidate.Listener.Password = candidate.LocalServer.Auth.Password
		candidate.Management.Password = candidate.LocalServer.Auth.Password
	}

	listenChanged := currentListen != candidate.LocalServer.Listen
	needReload := pendingReload || currentEnabled != candidate.LocalServer.Enabled || listenChanged || (!currentEnabled && credentialChanged)

	candidate.Lock()
	err := candidate.SaveSettings()
	candidate.Unlock()
	if err != nil {
		c.Unlock()
		return localServerConfigView{}, false, fmt.Errorf("save config: %w", err)
	}
	commitLocalServerConfig(c, candidate)
	c.Unlock()
	s.localServerReloadPending = needReload

	if !needReload {
		runtimeCfg, _ := snapshotPersistedServerConfig(candidate)
		s.cfgMu.Lock()
		applyPersistedServerConfig(&s.cfg, runtimeCfg)
		s.cfgMu.Unlock()

		if currentEnabled && candidate.LocalServer.Enabled && credentialChanged {
			if profiles := s.profileManagerSnapshot(); profiles != nil {
				profiles.PublishCredentials(profile.CredentialSnapshot{
					Username:   candidate.LocalServer.Auth.Username,
					Password:   candidate.LocalServer.Auth.Password,
					Generation: candidate.LocalServer.CredentialGeneration,
				})
			}
			if routing := s.routingSnapshot(); routing != nil {
				_ = routing.ApplyHot(candidate)
			}
		}
	}

	return localServerConfigViewFromConfig(candidate), needReload, nil
}

func commitLocalServerConfig(target, candidate *config.Config) {
	target.LocalServer = candidate.LocalServer
	target.Listener.Username = candidate.Listener.Username
	target.Listener.Password = candidate.Listener.Password
	target.Management.Password = candidate.Management.Password
}

func validateLocalServerConfigCandidate(candidate *config.Config, usernameExplicit bool) error {
	if candidate == nil {
		return fmt.Errorf("%w: config is nil", errInvalidLocalServerConfig)
	}
	username := strings.TrimSpace(candidate.LocalServer.Auth.Username)
	if usernameExplicit || candidate.LocalServer.Enabled || username != "" {
		if !validLocalServerUsername(username) {
			return fmt.Errorf("%w: auth_username must be 1-64 ASCII letters, digits, '.', '_' or '-'", errInvalidLocalServerConfig)
		}
	}
	candidate.LocalServer.Auth.Username = username

	password := candidate.LocalServer.Auth.Password
	if candidate.LocalServer.Enabled && password == "" {
		return fmt.Errorf("%w: auth_password is required while Local Server is enabled", errInvalidLocalServerConfig)
	}
	if strings.IndexByte(password, 0) >= 0 {
		return fmt.Errorf("%w: auth_password must not contain NUL", errInvalidLocalServerConfig)
	}
	if len(password) > 256 {
		return fmt.Errorf("%w: auth_password must be at most 256 bytes", errInvalidLocalServerConfig)
	}

	listen := strings.TrimSpace(candidate.LocalServer.Listen)
	if err := validateLocalServerListen(listen); err != nil {
		return fmt.Errorf("%w: %v", errInvalidLocalServerConfig, err)
	}
	candidate.LocalServer.Listen = listen

	if !candidate.LocalServer.Enabled {
		return nil
	}
	if candidate.Mode != "pool" {
		return fmt.Errorf("%w: enabled Local Server requires mode %q", errInvalidLocalServerConfig, "pool")
	}
	if candidate.Listener.Protocol != config.InboundProtocolMixed {
		return fmt.Errorf("%w: enabled Local Server requires listener.protocol %q", errInvalidLocalServerConfig, config.InboundProtocolMixed)
	}
	if len(candidate.ExtraListeners) > 0 {
		return fmt.Errorf("%w: enabled Local Server does not support extra_listeners", errInvalidLocalServerConfig)
	}
	routingListen := strings.TrimSpace(candidate.Routing.Listen)
	if listen != "" && routingListen != "" && listen != routingListen {
		return fmt.Errorf("%w: local_server.listen %q conflicts with routing.listen %q", errInvalidLocalServerConfig, listen, routingListen)
	}
	return nil
}

func validLocalServerUsername(username string) bool {
	if username == "" || len(username) > 64 {
		return false
	}
	for idx := 0; idx < len(username); idx++ {
		ch := username[idx]
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= 'A' && ch <= 'Z':
		case ch >= '0' && ch <= '9':
		case ch == '.', ch == '_', ch == '-':
		default:
			return false
		}
	}
	return true
}

func validateLocalServerListen(listen string) error {
	if listen == "" {
		return nil
	}
	_, portText, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("listen must be a valid host:port: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("listen port must be between 1 and 65535")
	}
	return nil
}

// handleSubscriptionStatus returns the current subscription refresh status.
// Works even when subRefresher is nil by reading config directly.
