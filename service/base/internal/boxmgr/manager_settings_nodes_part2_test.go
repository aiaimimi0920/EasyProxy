package boxmgr

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"testing"
	"time"

	"easy_proxies/internal/config"
	"easy_proxies/internal/monitor"
)

func TestSyncMonitorServerLifecycleRebindsSameServerOnListenChange(t *testing.T) {
	getFreeListenPair := func(t *testing.T) (string, string) {
		t.Helper()
		first, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("net.Listen() error = %v", err)
		}
		second, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			_ = first.Close()
			t.Fatalf("net.Listen() error = %v", err)
		}
		firstAddr := first.Addr().String()
		secondAddr := second.Addr().String()
		_ = first.Close()
		_ = second.Close()
		return firstAddr, secondAddr
	}

	waitReachable := func(t *testing.T, addr string, want bool) {
		t.Helper()
		var lastErr error
		for i := 0; i < 20; i++ {
			conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				if want {
					return
				}
			} else {
				lastErr = err
				if !want {
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		if want {
			t.Fatalf("expected %s to become reachable, last error: %v", addr, lastErr)
		}
		t.Fatalf("expected %s to stop listening", addr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	oldListen, newListen := getFreeListenPair(t)

	monitorMgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	prevCfg := monitor.Config{Enabled: true, Listen: oldListen}
	currentServer := monitor.NewServer(prevCfg, monitorMgr, log.New(io.Discard, "", 0))
	if currentServer == nil {
		t.Fatal("expected initial monitor server to be created")
	}
	if err := currentServer.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitReachable(t, oldListen, true)

	manager := &Manager{
		monitorMgr:    monitorMgr,
		monitorServer: currentServer,
		monitorCfg: monitor.Config{
			Enabled: true,
			Listen:  newListen,
		},
		logger: defaultLogger{},
	}

	enabled := true
	activeCfg := &config.Config{}
	activeCfg.Management.Enabled = &enabled
	activeCfg.Management.Listen = newListen

	if err := manager.syncMonitorServerLifecycle(ctx, prevCfg, activeCfg); err != nil {
		t.Fatalf("syncMonitorServerLifecycle() error = %v", err)
	}

	waitReachable(t, newListen, true)
	waitReachable(t, oldListen, false)

	if manager.monitorServer == nil {
		t.Fatal("expected monitor server to remain available after restart")
	}
	if manager.monitorServer != currentServer {
		t.Fatal("listen change replaced the monitor server runtime")
	}

	manager.monitorServer.Shutdown(context.Background())
}

func TestSyncMonitorServerLifecycleBindFailureKeepsOldListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldListen := fmt.Sprintf("127.0.0.1:%d", reserveFreePort(t))
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy replacement listen: %v", err)
	}
	defer occupied.Close()
	monitorMgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer monitorMgr.Stop()
	prevCfg := monitor.Config{Enabled: true, Listen: oldListen}
	currentServer := monitor.NewServer(prevCfg, monitorMgr, log.New(io.Discard, "", 0))
	if err := currentServer.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer currentServer.Shutdown(context.Background())
	waitReachableAddress(t, oldListen, true)

	manager := &Manager{
		monitorMgr:    monitorMgr,
		monitorServer: currentServer,
		monitorCfg: monitor.Config{
			Enabled: true,
			Listen:  occupied.Addr().String(),
		},
		logger: defaultLogger{},
	}
	enabled := true
	activeCfg := &config.Config{Management: config.ManagementConfig{
		Enabled: &enabled,
		Listen:  occupied.Addr().String(),
	}}
	if err := manager.syncMonitorServerLifecycle(ctx, prevCfg, activeCfg); err == nil {
		t.Fatal("syncMonitorServerLifecycle() error = nil, want bind failure")
	}
	if manager.monitorServer != currentServer {
		t.Fatal("bind failure replaced the monitor server runtime")
	}
	waitReachableAddress(t, oldListen, true)
}

func TestPrepareMonitorServerTransitionStagesTargetPasswordBeforeActivation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	oldListen := fmt.Sprintf("127.0.0.1:%d", reserveFreePort(t))
	monitorMgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer monitorMgr.Stop()
	currentServer := monitor.NewServer(
		monitor.Config{Enabled: true, Listen: oldListen},
		monitorMgr,
		log.New(io.Discard, "", 0),
	)
	if err := currentServer.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer currentServer.Shutdown(context.Background())

	targetListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve target listen: %v", err)
	}
	targetListen := targetListener.Addr().String()
	if err := targetListener.Close(); err != nil {
		t.Fatalf("release target listen: %v", err)
	}
	manager := &Manager{
		monitorMgr:    monitorMgr,
		monitorServer: currentServer,
		logger:        defaultLogger{},
	}
	enabled := true
	targetCfg := &config.Config{Management: config.ManagementConfig{
		Enabled:  &enabled,
		Listen:   targetListen,
		Password: "target-password",
	}}
	transition, err := manager.prepareMonitorServerTransition(targetCfg)
	if err != nil {
		t.Fatalf("prepareMonitorServerTransition() error = %v", err)
	}
	if err := transition.Activate(ctx); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	defer transition.Abort()

	request, err := http.NewRequest(http.MethodGet, "http://"+targetListen+"/api/nodes", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	response, err := (&http.Client{Timeout: time.Second}).Do(request)
	if err != nil {
		t.Fatalf("GET target monitor listener: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("target listener status = %d, want 401 before transaction commit", response.StatusCode)
	}
}

func TestPrepareMonitorBindFailureCleansCreatedRuntime(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy management listen: %v", err)
	}
	defer occupied.Close()
	enabled := true
	cfg := &config.Config{Management: config.ManagementConfig{
		Enabled: &enabled,
		Listen:  occupied.Addr().String(),
	}}
	manager := New(cfg, monitor.Config{Enabled: true, Listen: occupied.Addr().String()})
	if err := manager.PrepareMonitor(context.Background()); err == nil {
		t.Fatal("PrepareMonitor() error = nil, want bind failure")
	}
	manager.mu.RLock()
	monitorServer := manager.monitorServer
	monitorMgr := manager.monitorMgr
	manager.mu.RUnlock()
	if monitorServer != nil || monitorMgr != nil {
		t.Fatalf("bind failure retained monitor runtime: server=%p manager=%p", monitorServer, monitorMgr)
	}
}
