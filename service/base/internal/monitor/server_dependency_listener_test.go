package monitor

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"easy_proxies/internal/config"
)

func TestSetConfigDoesNotHoldCfgMuWhileWaitingForConfigRead(t *testing.T) {
	s := &Server{}
	cfg := &config.Config{}
	cfg.Lock()
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		s.SetConfig(cfg)
		close(done)
	}()
	<-started

	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !s.cfgMu.TryRLock() {
			cfg.Unlock()
			<-done
			t.Fatal("SetConfig acquired cfgMu before it could read the config")
		}
		s.cfgMu.RUnlock()
		runtime.Gosched()
	}
	cfg.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SetConfig did not complete after the config read lock was released")
	}
}

func TestNodeHandlerKeepsDependencySnapshotAcrossManagerReplacement(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()
	server := NewServer(Config{}, mgr, log.New(io.Discard, "", 0))
	if server == nil {
		t.Fatal("NewServer() returned nil")
	}
	original := &swappingNodeManager{server: server}
	replacement := &swappingNodeManager{}
	original.replacement = replacement
	server.SetNodeManager(original)

	req := httptest.NewRequest(http.MethodPatch, "/api/nodes/config/node", strings.NewReader(`{"enabled":true}`))
	rec := httptest.NewRecorder()
	server.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := original.reloadCalls.Load(); got != 1 {
		t.Fatalf("original manager reload calls = %d, want 1", got)
	}
	if got := replacement.reloadCalls.Load(); got != 0 {
		t.Fatalf("replacement manager reload calls = %d, want 0", got)
	}
}

func TestConnectorHandlerKeepsDependencySnapshotAcrossManagerReplacement(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()
	server := NewServer(Config{}, mgr, log.New(io.Discard, "", 0))
	if server == nil {
		t.Fatal("NewServer() returned nil")
	}
	original := &swappingConnectorManager{server: server}
	replacement := &swappingConnectorManager{}
	original.replacement = replacement
	server.SetConnectorManager(original)

	req := httptest.NewRequest(http.MethodPost, "/api/connectors/config", strings.NewReader(`{"name":"connector","input":"https://example.com/sub"}`))
	rec := httptest.NewRecorder()
	server.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := original.createCalls.Load(); got != 1 {
		t.Fatalf("original manager create calls = %d, want 1", got)
	}
	if got := original.refreshCalls.Load(); got != 1 {
		t.Fatalf("original manager refresh calls = %d, want 1", got)
	}
	if got := replacement.refreshCalls.Load(); got != 0 {
		t.Fatalf("replacement manager refresh calls = %d, want 0", got)
	}
}

func TestConnectorMutationIsRejectedDuringReloadWindow(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()
	server := NewServer(Config{}, mgr, log.New(io.Discard, "", 0))
	connectorMgr := &swappingConnectorManager{}
	server.SetConnectorManager(connectorMgr)
	server.BeginReloadWindow()
	defer server.EndReloadWindow()

	req := httptest.NewRequest(http.MethodPost, "/api/connectors/config", strings.NewReader(`{"name":"connector","input":"https://example.com/sub"}`))
	rec := httptest.NewRecorder()
	server.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if got := connectorMgr.createCalls.Load(); got != 0 {
		t.Fatalf("connector manager create calls = %d, want 0", got)
	}
}

func TestListenerTransitionAppliesTargetConfigBeforeActivationAndRestoresOnRollback(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()
	oldListen := freeLifecycleListen(t)
	newListen := freeLifecycleListenDifferent(t, oldListen)
	enabled := true
	oldCfg := &config.Config{Management: config.ManagementConfig{
		Enabled:  &enabled,
		Listen:   oldListen,
		Password: "old-password",
	}}
	server := NewServer(Config{Enabled: true, Listen: oldListen, Password: "old-password"}, mgr, log.New(io.Discard, "", 0))
	server.SetConfig(oldCfg)
	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer shutdownLifecycleServer(server)

	targetCfg := &config.Config{Management: config.ManagementConfig{
		Enabled:  &enabled,
		Listen:   newListen,
		Password: "new-password",
	}}
	transition, err := server.PrepareListener(true, newListen, targetCfg)
	if err != nil {
		t.Fatalf("PrepareListener() error = %v", err)
	}
	if err := transition.Activate(context.Background()); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	waitLifecycleListen(t, newListen, true)
	if got := monitorRequestStatus(t, newListen, ""); got != http.StatusUnauthorized {
		t.Fatalf("new listener without auth status = %d, want 401", got)
	}
	if got := monitorRequestStatus(t, newListen, "old-password"); got != http.StatusUnauthorized {
		t.Fatalf("new listener with old password status = %d, want 401", got)
	}
	if got := monitorRequestStatus(t, newListen, "new-password"); got != http.StatusOK {
		t.Fatalf("new listener with target password status = %d, want 200", got)
	}
	if err := transition.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	waitLifecycleListen(t, newListen, false)
	waitLifecycleListen(t, oldListen, true)
	if got := monitorRequestStatus(t, oldListen, "new-password"); got != http.StatusUnauthorized {
		t.Fatalf("rolled-back listener with target password status = %d, want 401", got)
	}
	if got := monitorRequestStatus(t, oldListen, "old-password"); got != http.StatusOK {
		t.Fatalf("rolled-back listener with old password status = %d, want 200", got)
	}
}
