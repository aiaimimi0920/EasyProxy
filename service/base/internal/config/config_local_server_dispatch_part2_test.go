package config

import (
	"testing"
)

func TestDisabledLocalServerPreservesLegacyDispatchTopology(t *testing.T) {
	tests := []struct {
		name           string
		cfg            *Config
		wantListen     string
		wantOwnership  bool
		wantDispatcher bool
	}{
		{
			name: "routing disabled uses listener",
			cfg: &Config{
				Listener:    ListenerConfig{Address: "127.0.0.1", Port: 22323},
				LocalServer: LocalServerConfig{Listen: "127.0.0.1:30000"},
			},
			wantListen: "127.0.0.1:22323",
		},
		{
			name: "zero listener uses legacy defaults",
			cfg: &Config{
				LocalServer: LocalServerConfig{Listen: "127.0.0.1:30000"},
			},
			wantListen: "0.0.0.0:22323",
		},
		{
			name: "route A owns listener",
			cfg: &Config{
				Listener: ListenerConfig{Address: "127.0.0.1", Port: 22323},
				Routing:  RoutingConfig{Enabled: true},
			},
			wantListen:     "127.0.0.1:22323",
			wantOwnership:  true,
			wantDispatcher: true,
		},
		{
			name: "route B coexists on separate listen",
			cfg: &Config{
				Listener: ListenerConfig{Address: "127.0.0.1", Port: 22323},
				Routing: RoutingConfig{
					Enabled: true,
					Listen:  "127.0.0.1:22324",
				},
			},
			wantListen:     "127.0.0.1:22324",
			wantDispatcher: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.DispatchListen(); got != tt.wantListen {
				t.Fatalf("DispatchListen() = %q, want %q", got, tt.wantListen)
			}
			if got := tt.cfg.DispatchOwnsPrimaryInbound(); got != tt.wantOwnership {
				t.Fatalf("DispatchOwnsPrimaryInbound() = %v, want %v", got, tt.wantOwnership)
			}
			if got := tt.cfg.DispatchEnabled(); got != tt.wantDispatcher {
				t.Fatalf("DispatchEnabled() = %v, want %v", got, tt.wantDispatcher)
			}
		})
	}
}
