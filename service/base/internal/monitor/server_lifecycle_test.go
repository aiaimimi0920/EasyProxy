package monitor

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"testing"
	"time"

	"easy_proxies/internal/config"
)

func TestServerStartReturnsBindError(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy listen: %v", err)
	}
	defer occupied.Close()

	server := NewServer(Config{Enabled: true, Listen: occupied.Addr().String()}, mgr, log.New(io.Discard, "", 0))
	defer shutdownLifecycleServer(server)
	if err := server.Start(context.Background()); err == nil {
		t.Fatal("Start() error = nil, want bind failure")
	}
}

func TestListenerTransitionBindFailureKeepsOldListener(t *testing.T) {
	server, oldListen := startLifecycleTestServer(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy replacement listen: %v", err)
	}
	defer occupied.Close()

	if _, err := server.PrepareListener(true, occupied.Addr().String()); err == nil {
		t.Fatal("PrepareListener() error = nil, want bind failure")
	}
	waitLifecycleListen(t, oldListen, true)
}

func TestListenerTransitionRollbackKeepsOldAndClosesCandidate(t *testing.T) {
	server, oldListen := startLifecycleTestServer(t)
	newListen := freeLifecycleListenDifferent(t, oldListen)
	transition, err := server.PrepareListener(true, newListen)
	if err != nil {
		t.Fatalf("PrepareListener() error = %v", err)
	}
	if err := transition.Activate(context.Background()); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	waitLifecycleListen(t, oldListen, true)
	waitLifecycleListen(t, newListen, true)
	if err := transition.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	waitLifecycleListen(t, oldListen, true)
	waitLifecycleListen(t, newListen, false)
}

func TestListenerTransitionFinalizeReusesServerRuntimeState(t *testing.T) {
	server, oldListen := startLifecycleTestServer(t)
	compatState := server.proxyCompat
	newListen := freeLifecycleListenDifferent(t, oldListen)
	transition, err := server.PrepareListener(true, newListen)
	if err != nil {
		t.Fatalf("PrepareListener() error = %v", err)
	}
	if err := transition.Activate(context.Background()); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	transition.Finalize()

	waitLifecycleListen(t, newListen, true)
	waitLifecycleListen(t, oldListen, false)
	if server.proxyCompat != compatState {
		t.Fatal("listener transition replaced proxy compatibility runtime state")
	}
}

func TestReloadHandlerCanFinalizeItsOwnListenerTransition(t *testing.T) {
	server, oldListen := startLifecycleTestServer(t)
	newListen := freeLifecycleListenDifferent(t, oldListen)
	server.SetNodeManager(&lifecycleNodeManager{trigger: func(context.Context) error {
		transition, err := server.PrepareListener(true, newListen)
		if err != nil {
			return err
		}
		if err := transition.Activate(context.Background()); err != nil {
			transition.Abort()
			return err
		}
		transition.Finalize()
		return nil
	}})

	client := &http.Client{Timeout: 2 * time.Second}
	request, err := http.NewRequest(http.MethodPost, "http://"+oldListen+"/api/reload", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("reload request deadlocked or failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reload status = %d, want 200", response.StatusCode)
	}
	waitLifecycleListen(t, newListen, true)
	waitLifecycleListen(t, oldListen, false)
}

func TestDisabledServerCanBeWiredBeforeFirstListenerStart(t *testing.T) {
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()
	server := NewServer(Config{Enabled: false}, mgr, log.New(io.Discard, "", 0))
	if server == nil {
		t.Fatal("disabled monitor config did not create a reusable server runtime")
	}
	nodeManager := &lifecycleNodeManager{}
	server.SetNodeManager(nodeManager)

	listen := freeLifecycleListen(t)
	transition, err := server.PrepareListener(true, listen)
	if err != nil {
		t.Fatalf("PrepareListener() error = %v", err)
	}
	if err := transition.Activate(context.Background()); err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	transition.Finalize()
	defer shutdownLifecycleServer(server)
	waitLifecycleListen(t, listen, true)
	if server.nodeManagerSnapshot() != nodeManager {
		t.Fatal("first listener start lost pre-wired dependencies")
	}
}

func startLifecycleTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	listen := freeLifecycleListen(t)
	server := NewServer(Config{Enabled: true, Listen: listen}, mgr, log.New(io.Discard, "", 0))
	if err := server.Start(context.Background()); err != nil {
		mgr.Stop()
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		shutdownLifecycleServer(server)
		mgr.Stop()
	})
	waitLifecycleListen(t, listen, true)
	return server, listen
}

func shutdownLifecycleServer(server *Server) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	server.Shutdown(ctx)
}

func freeLifecycleListen(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listen: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release listen: %v", err)
	}
	return address
}

func freeLifecycleListenDifferent(t *testing.T, avoid string) string {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		address := freeLifecycleListen(t)
		if address != avoid {
			return address
		}
	}
	t.Fatalf("could not reserve a listener address different from %s", avoid)
	return ""
}

func waitLifecycleListen(t *testing.T, address string, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			if want {
				return
			}
		} else if !want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("listen %s reachable=%v, want %v (last error: %v)", address, err == nil, want, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type lifecycleNodeManager struct {
	trigger func(context.Context) error
}

func (*lifecycleNodeManager) ListConfigNodes(context.Context) ([]config.NodeConfig, error) {
	return nil, nil
}

func (*lifecycleNodeManager) CreateNode(_ context.Context, node config.NodeConfig) (config.NodeConfig, error) {
	return node, nil
}

func (*lifecycleNodeManager) UpdateNode(_ context.Context, _ string, node config.NodeConfig) (config.NodeConfig, error) {
	return node, nil
}

func (*lifecycleNodeManager) DeleteNode(context.Context, string) error { return nil }

func (*lifecycleNodeManager) SetNodeEnabled(context.Context, string, bool) error { return nil }

func (m *lifecycleNodeManager) TriggerReload(ctx context.Context) error {
	if m.trigger == nil {
		return nil
	}
	return m.trigger(ctx)
}
