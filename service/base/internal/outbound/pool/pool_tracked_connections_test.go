package pool

import (
	"net"
	"testing"

	"easy_proxies/internal/monitor"
)

func TestTrackedConnRecordsTrafficSuccessOnlyAfterDownload(t *testing.T) {
	ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	entry := mgr.Register(monitor.NodeInfo{Tag: "traffic-node", Name: "Traffic Node"})
	shared := acquireSharedState("traffic-node")
	shared.attachEntry(entry)

	server, client := net.Pipe()
	defer server.Close()

	conn := &trackedConn{
		Conn:    client,
		release: func() {},
		onConfirmedSuccess: func() {
			shared.recordSuccess("example.com:443")
		},
	}
	defer conn.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = server.Write([]byte("hello"))
	}()

	buf := make([]byte, 5)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("trackedConn.Read() error = %v", err)
	}
	<-done

	snaps := mgr.Snapshot()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	if snaps[0].TrafficSuccessCount != 1 {
		t.Fatalf("expected traffic success count to be 1 after download, got %d", snaps[0].TrafficSuccessCount)
	}
	if snaps[0].LastTrafficSuccessAt.IsZero() {
		t.Fatal("expected last traffic success timestamp to be set")
	}
}

func TestTrackedConnWriteOnlyDoesNotRecordTrafficSuccess(t *testing.T) {
	ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	entry := mgr.Register(monitor.NodeInfo{Tag: "write-only-node", Name: "Write Only Node"})
	shared := acquireSharedState("write-only-node")
	shared.attachEntry(entry)

	server, client := net.Pipe()
	defer server.Close()

	conn := &trackedConn{
		Conn:    client,
		release: func() {},
		onConfirmedSuccess: func() {
			shared.recordSuccess("example.com:443")
		},
	}
	defer conn.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4)
		_, _ = server.Read(buf)
	}()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("trackedConn.Write() error = %v", err)
	}
	<-done

	snaps := mgr.Snapshot()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	if snaps[0].TrafficSuccessCount != 0 {
		t.Fatalf("expected traffic success count to remain 0 without download, got %d", snaps[0].TrafficSuccessCount)
	}
	if !snaps[0].LastTrafficSuccessAt.IsZero() {
		t.Fatal("expected last traffic success timestamp to remain unset without download")
	}
}
