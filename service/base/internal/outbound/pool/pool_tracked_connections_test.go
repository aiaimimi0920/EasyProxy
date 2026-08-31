package pool

import (
	"net"
	"testing"
	"time"

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
		onUnconfirmedFailure: func(cause error) {
			shared.recordFailure(cause, 1, time.Minute, "example.com:443")
		},
	}
	defer conn.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = server.Write([]byte("hello"))
		_ = server.Close()
	}()

	buf := make([]byte, 5)
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("trackedConn.Read() error = %v", err)
	}
	<-done
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected EOF after the confirmed response")
	}

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
	if snaps[0].FailureCount != 0 || snaps[0].Blacklisted {
		t.Fatalf("confirmed response followed by EOF was treated as a failure: %+v", snaps[0])
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

func TestTrackedConnRecordsFailureWhenFirstResponseNeverArrives(t *testing.T) {
	ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	entry := mgr.Register(monitor.NodeInfo{Tag: "early-eof-node", Name: "Early EOF Node"})
	shared := acquireSharedState("early-eof-node")
	shared.attachEntry(entry)

	server, client := net.Pipe()
	conn := &trackedConn{
		Conn:    client,
		release: func() {},
		onUnconfirmedFailure: func(cause error) {
			shared.recordFailure(cause, 1, time.Minute, "example.com:443")
		},
	}
	defer conn.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4)
		_, _ = server.Read(buf)
		_ = server.Close()
	}()

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("trackedConn.Write() error = %v", err)
	}
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected the peer to close before returning a response")
	}
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected repeated read after peer close to fail")
	}
	<-done

	snaps := mgr.Snapshot()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	if snaps[0].FailureCount != 1 || !snaps[0].Blacklisted {
		t.Fatalf("unconfirmed response failure was not recorded: %+v", snaps[0])
	}
}

func TestTrackedConnLocalCloseDoesNotRecordFailure(t *testing.T) {
	ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	entry := mgr.Register(monitor.NodeInfo{Tag: "local-close-node", Name: "Local Close Node"})
	shared := acquireSharedState("local-close-node")
	shared.attachEntry(entry)

	server, client := net.Pipe()
	defer server.Close()
	conn := &trackedConn{
		Conn:    client,
		release: func() {},
		onUnconfirmedFailure: func(cause error) {
			shared.recordFailure(cause, 1, time.Minute, "example.com:443")
		},
	}

	uploadRead := make(chan struct{})
	go func() {
		buf := make([]byte, 4)
		_, _ = server.Read(buf)
		close(uploadRead)
	}()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("trackedConn.Write() error = %v", err)
	}
	<-uploadRead

	readStarted := make(chan struct{})
	readDone := make(chan error, 1)
	go func() {
		close(readStarted)
		buf := make([]byte, 1)
		_, err := conn.Read(buf)
		readDone <- err
	}()
	<-readStarted
	if err := conn.Close(); err != nil {
		t.Fatalf("trackedConn.Close() error = %v", err)
	}
	if err := <-readDone; err == nil {
		t.Fatal("expected the locally closed read to return an error")
	}

	snaps := mgr.Snapshot()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	if snaps[0].FailureCount != 0 || snaps[0].Blacklisted {
		t.Fatalf("local close was treated as an upstream failure: %+v", snaps[0])
	}
}

func TestTrackedConnZeroByteWriteErrorDoesNotRecordFailure(t *testing.T) {
	ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	entry := mgr.Register(monitor.NodeInfo{Tag: "zero-write-node", Name: "Zero Write Node"})
	shared := acquireSharedState("zero-write-node")
	shared.attachEntry(entry)

	server, client := net.Pipe()
	_ = server.Close()
	conn := &trackedConn{
		Conn:    client,
		release: func() {},
		onUnconfirmedFailure: func(cause error) {
			shared.recordFailure(cause, 1, time.Minute, "example.com:443")
		},
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("ping")); err == nil {
		t.Fatal("expected write to the closed peer to fail")
	}

	snaps := mgr.Snapshot()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	if snaps[0].FailureCount != 0 || snaps[0].Blacklisted {
		t.Fatalf("zero-byte write failure was treated as confirmed upstream traffic: %+v", snaps[0])
	}
}
