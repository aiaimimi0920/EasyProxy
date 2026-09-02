package pool

import (
	"errors"
	"testing"
	"time"

	"easy_proxies/internal/monitor"

	N "github.com/sagernet/sing/common/network"
)

func TestUDPBlacklistDoesNotBlacklistTCP(t *testing.T) {
	ResetSharedStateStore()
	defer ResetSharedStateStore()
	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()
	state := acquireSharedState("dual-stack-node")
	entry := mgr.Register(monitor.NodeInfo{Tag: "dual-stack-node"})
	state.attachEntry(entry)

	state.recordTransportFailure(N.NetworkUDP, errors.New("udp unavailable"), 1, time.Hour, "example.com:443")
	if !state.isTransportBlacklisted(N.NetworkUDP, time.Now()) {
		t.Fatal("UDP failure did not blacklist the UDP transport")
	}
	if state.isTransportBlacklisted(N.NetworkTCP, time.Now()) {
		t.Fatal("UDP failure incorrectly blacklisted TCP")
	}
	if snap := entry.Snapshot(); snap.Blacklisted || snap.FailureCount != 1 {
		t.Fatalf("UDP failure monitor state = %+v", snap)
	}
}

func TestUDPReleaseDoesNotClearTCPBlacklist(t *testing.T) {
	state := &sharedMemberState{}
	state.recordFailure(errors.New("tcp unavailable"), 1, time.Hour, "example.com:443")
	state.recordTransportFailure(N.NetworkUDP, errors.New("udp unavailable"), 1, time.Hour, "example.com:443")
	state.forceReleaseTransport(N.NetworkUDP)
	if state.isTransportBlacklisted(N.NetworkUDP, time.Now()) {
		t.Fatal("UDP release retained the UDP blacklist")
	}
	if !state.isTransportBlacklisted(N.NetworkTCP, time.Now()) {
		t.Fatal("UDP release cleared the TCP blacklist")
	}
}

func TestTCPReleaseDoesNotClearUDPBlacklist(t *testing.T) {
	state := &sharedMemberState{}
	state.recordFailure(errors.New("tcp unavailable"), 1, time.Hour, "example.com:443")
	state.recordTransportFailure(N.NetworkUDP, errors.New("udp unavailable"), 1, time.Hour, "example.com:443")
	state.forceReleaseTransport(N.NetworkTCP)
	if state.isTransportBlacklisted(N.NetworkTCP, time.Now()) {
		t.Fatal("TCP release retained the TCP blacklist")
	}
	if !state.isTransportBlacklisted(N.NetworkUDP, time.Now()) {
		t.Fatal("TCP release cleared the UDP blacklist")
	}
}
