package monitor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"easy_proxies/internal/config"
)

const listenerShutdownTimeout = 2 * time.Second

// ListenerTransition holds a pre-bound listener change. The old listener keeps
// serving until Finalize, which makes activation rollback-safe and prevents a
// reload handler from synchronously shutting down the server handling itself.
type ListenerTransition struct {
	server *Server

	oldServer   *http.Server
	oldListener net.Listener
	oldListen   string

	newServer   *http.Server
	newListener net.Listener
	newListen   string
	enabled     bool
	noChange    bool

	oldConfigSource  *config.Config
	oldRuntimeConfig Config
	targetConfig     *config.Config
	targetRuntime    persistedServerConfig
	hasTargetConfig  bool
	configApplied    bool

	mu               sync.Mutex
	activated        bool
	done             bool
	configUpdateHeld bool
}

func (t *ListenerTransition) releaseConfigUpdateLocked() {
	if t == nil || !t.configUpdateHeld || t.server == nil {
		return
	}
	t.configUpdateHeld = false
	t.server.configUpdateMu.Unlock()
}

// PrepareListener synchronously binds a replacement address without disturbing
// the active listener. Bind failures therefore leave the old endpoint intact.
func (s *Server) PrepareListener(enabled bool, listen string, targetConfigs ...*config.Config) (*ListenerTransition, error) {
	if s == nil {
		return nil, errors.New("monitor server is nil")
	}
	listen = strings.TrimSpace(listen)
	if enabled && listen == "" {
		return nil, errors.New("monitor listen address is empty")
	}
	s.configUpdateMu.Lock()
	var targetConfig *config.Config
	if len(targetConfigs) > 0 {
		targetConfig = targetConfigs[0]
	}
	targetRuntime, hasTargetConfig := snapshotPersistedServerConfig(targetConfig)
	s.cfgMu.RLock()
	oldConfigSource := s.cfgSrc
	oldRuntimeConfig := cloneRuntimeConfig(s.cfg)
	s.cfgMu.RUnlock()
	password := oldRuntimeConfig.Password
	if hasTargetConfig {
		password = targetRuntime.password
	}
	if err := validateManagementListenerAuth(enabled, listen, password); err != nil {
		s.configUpdateMu.Unlock()
		return nil, err
	}

	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.shutdown {
		s.configUpdateMu.Unlock()
		return nil, errors.New("monitor server is shut down")
	}
	transition := &ListenerTransition{
		server:           s,
		oldServer:        s.srv,
		oldListener:      s.listener,
		oldListen:        s.listen,
		enabled:          enabled,
		newListen:        listen,
		oldConfigSource:  oldConfigSource,
		oldRuntimeConfig: oldRuntimeConfig,
		targetConfig:     targetConfig,
		targetRuntime:    targetRuntime,
		hasTargetConfig:  hasTargetConfig,
		configUpdateHeld: true,
	}
	if enabled && s.srv != nil && s.listen == listen {
		transition.noChange = true
		return transition, nil
	}
	if !enabled {
		return transition, nil
	}

	listener, err := net.Listen("tcp", listen)
	if err != nil {
		s.configUpdateMu.Unlock()
		return nil, fmt.Errorf("bind monitor listener %s: %w", listen, err)
	}
	transition.newListener = listener
	transition.newServer = &http.Server{Addr: listen, Handler: s.handler}
	return transition, nil
}

func validateManagementListenerAuth(enabled bool, listen, password string) error {
	if !enabled || password != "" {
		return nil
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return fmt.Errorf("parse management listen address %q: %w", listen, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if address, _, ok := strings.Cut(host, "%"); ok {
		host = address
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("management listener %q requires a password when bound to a non-loopback address", listen)
}

// Activate publishes the prepared listener while deliberately leaving the old
// listener alive. Call Finalize after the surrounding reload commits, or
// Rollback if a later transaction step fails.
func (t *ListenerTransition) Activate(ctx context.Context) error {
	if t == nil || t.server == nil {
		return errors.New("monitor listener transition is nil")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return errors.New("monitor listener transition already completed")
	}
	if t.activated {
		return nil
	}

	s := t.server
	s.lifecycleMu.Lock()
	if s.shutdown {
		s.lifecycleMu.Unlock()
		t.releaseConfigUpdateLocked()
		return errors.New("monitor server is shut down")
	}
	if s.srv != t.oldServer || s.listener != t.oldListener || s.listen != t.oldListen {
		s.lifecycleMu.Unlock()
		t.releaseConfigUpdateLocked()
		return errors.New("monitor listener changed after transition preparation")
	}
	if t.hasTargetConfig {
		s.applyPreparedConfigLocked(t.targetConfig, t.targetRuntime)
		t.configApplied = true
	}
	if !t.noChange {
		if t.enabled {
			s.srv = t.newServer
			s.listener = t.newListener
			s.listen = t.newListen
			s.serveLocked(t.newServer, t.newListener, t.newListen)
		} else {
			s.srv = nil
			s.listener = nil
			s.listen = ""
		}
	}
	s.watchContextLocked(ctx)
	s.lifecycleMu.Unlock()
	t.activated = true
	return nil
}

// Finalize commits the transition and drains the old HTTP server asynchronously
// with a bounded timeout, allowing a request served by the old listener to
// finish after triggering its own reload.
func (t *ListenerTransition) Finalize() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.done {
		t.mu.Unlock()
		return
	}
	if !t.activated {
		listener := t.newListener
		t.done = true
		t.releaseConfigUpdateLocked()
		t.mu.Unlock()
		if listener != nil {
			_ = listener.Close()
		}
		return
	}
	if t.noChange {
		t.done = true
		t.releaseConfigUpdateLocked()
		t.mu.Unlock()
		return
	}
	oldServer := t.oldServer
	newServer := t.newServer
	t.done = true
	t.releaseConfigUpdateLocked()
	t.mu.Unlock()
	if oldServer != nil && oldServer != newServer {
		shutdownHTTPServerAsync(oldServer)
	}
}

// Rollback restores the still-serving old listener and closes the prepared or
// activated candidate listener.
func (t *ListenerTransition) Rollback() error {
	if t == nil || t.server == nil {
		return errors.New("monitor listener transition is nil")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return errors.New("monitor listener transition already completed")
	}

	s := t.server
	if !t.activated {
		t.done = true
		t.releaseConfigUpdateLocked()
		if t.newListener != nil {
			_ = t.newListener.Close()
		}
		return nil
	}
	if t.activated {
		s.lifecycleMu.Lock()
		if !t.noChange {
			currentMatches := false
			if t.enabled {
				currentMatches = s.srv == t.newServer && s.listener == t.newListener && s.listen == t.newListen
			} else {
				currentMatches = s.srv == nil && s.listener == nil && s.listen == ""
			}
			if !currentMatches {
				s.lifecycleMu.Unlock()
				t.releaseConfigUpdateLocked()
				return errors.New("monitor listener changed after transition activation")
			}
			s.srv = t.oldServer
			s.listener = t.oldListener
			s.listen = t.oldListen
		}
		if t.configApplied {
			s.restorePreparedConfigLocked(t.oldConfigSource, t.oldRuntimeConfig)
		}
		s.lifecycleMu.Unlock()
	}
	t.done = true
	t.releaseConfigUpdateLocked()
	if t.newServer != nil && t.newServer != t.oldServer {
		shutdownHTTPServerAsync(t.newServer)
	} else if t.newListener != nil {
		_ = t.newListener.Close()
	}
	return nil
}

// Abort releases a prepared transition. It is safe before or after Activate.
func (t *ListenerTransition) Abort() {
	if t != nil {
		_ = t.Rollback()
	}
}

func (s *Server) serveLocked(server *http.Server, listener net.Listener, listen string) {
	if server == nil || listener == nil {
		return
	}
	s.logger.Printf("Starting monitor server on %s", listen)
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.logger.Printf("❌ Monitor server error: %v", err)
		}
	}()
	s.logger.Printf("✅ Monitor server started on http://%s", listen)
}

func (s *Server) watchContextLocked(ctx context.Context) {
	if ctx == nil || ctx.Done() == nil {
		return
	}
	s.watchOnce.Do(func() {
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), listenerShutdownTimeout)
			defer cancel()
			s.Shutdown(shutdownCtx)
		}()
	})
}

func shutdownHTTPServerAsync(server *http.Server) {
	if server == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), listenerShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			_ = server.Close()
		}
	}()
}

// SetSubscriptionRefresher sets the subscription refresher for API endpoints.
