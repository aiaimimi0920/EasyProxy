package monitor

import "testing"

func TestUpdateProbeTargetPreservesHTTPSDefaultPort(t *testing.T) {
	manager, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if err := manager.UpdateProbeTarget("https://example.com/generate_204"); err != nil {
		t.Fatalf("UpdateProbeTarget() error = %v", err)
	}

	destination, ok := manager.DestinationForProbe()
	if !ok {
		t.Fatal("expected probe destination to be ready")
	}
	if destination.Fqdn != "example.com" {
		t.Fatalf("unexpected probe host: %s", destination.Fqdn)
	}
	if destination.Port != 443 {
		t.Fatalf("unexpected probe port: %d", destination.Port)
	}
}
