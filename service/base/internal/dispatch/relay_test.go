package dispatch

import (
	"net"
	"testing"
	"time"
)

func TestRelayStopsWhenEitherSideCloses(t *testing.T) {
	clientA, relayA := net.Pipe()
	clientB, relayB := net.Pipe()
	defer clientA.Close()
	defer clientB.Close()

	done := make(chan struct{})
	go func() {
		relay(relayA, relayB)
		close(done)
	}()

	if err := clientA.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		relayA.Close()
		relayB.Close()
		t.Fatal("relay did not stop after one side closed")
	}
}
