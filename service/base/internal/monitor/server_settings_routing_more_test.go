package monitor

import (
	"bytes"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"easy_proxies/internal/config"
)

func TestUpdateRoutingConfigReloadMatrix(t *testing.T) {
	baseRequest := routingConfigPayload{
		Enabled:            true,
		DefaultStrategy:    "stable",
		UseDefaultRules:    true,
		FinalPolicy:        "PROXY",
		LongLivedMinUptime: "2h",
		LongLivedMinRate:   0.9,
		SessionTTL:         "10m",
	}

	tests := []struct {
		name        string
		mutate      func(*routingConfigPayload)
		wantReload  bool
		wantApply   bool
		wantUptime  time.Duration
		wantRate    float64
		wantSession time.Duration
	}{
		{
			name: "uptime threshold hot applies",
			mutate: func(req *routingConfigPayload) {
				req.LongLivedMinUptime = "45m"
			},
			wantApply:   true,
			wantUptime:  45 * time.Minute,
			wantRate:    0.9,
			wantSession: 10 * time.Minute,
		},
		{
			name: "success rate threshold hot applies",
			mutate: func(req *routingConfigPayload) {
				req.LongLivedMinRate = 0.75
			},
			wantApply:   true,
			wantUptime:  2 * time.Hour,
			wantRate:    0.75,
			wantSession: 10 * time.Minute,
		},
		{
			name: "zero thresholds restore runtime defaults without reload",
			mutate: func(req *routingConfigPayload) {
				req.LongLivedMinUptime = ""
				req.LongLivedMinRate = 0
			},
			wantApply:   true,
			wantUptime:  0,
			wantRate:    0,
			wantSession: 10 * time.Minute,
		},
		{
			name: "enabled change requires reload",
			mutate: func(req *routingConfigPayload) {
				req.Enabled = false
			},
			wantReload:  true,
			wantUptime:  2 * time.Hour,
			wantRate:    0.9,
			wantSession: 10 * time.Minute,
		},
		{
			name: "listen change requires reload",
			mutate: func(req *routingConfigPayload) {
				req.Listen = "127.0.0.1:24444"
			},
			wantReload:  true,
			wantUptime:  2 * time.Hour,
			wantRate:    0.9,
			wantSession: 10 * time.Minute,
		},
		{
			name: "session ttl change requires reload",
			mutate: func(req *routingConfigPayload) {
				req.SessionTTL = "20m"
			},
			wantReload:  true,
			wantUptime:  2 * time.Hour,
			wantRate:    0.9,
			wantSession: 20 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useDefaults := true
			cfg := &config.Config{}
			cfg.Routing.Enabled = true
			cfg.Routing.DefaultStrategy = "stable"
			cfg.Routing.UseDefaultRules = &useDefaults
			cfg.Routing.FinalPolicy = "PROXY"
			cfg.Routing.LongLived.MinUptime = 2 * time.Hour
			cfg.Routing.LongLived.MinSuccessRate = 0.9
			cfg.Routing.Session.TTL = 10 * time.Minute

			configPath := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(configPath, []byte("{}\n"), 0o644); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			cfg.SetFilePath(configPath)

			routing := &recordingRoutingController{applyResult: true}
			server := &Server{
				cfgSrc:  cfg,
				routing: routing,
				logger:  log.New(io.Discard, "", 0),
			}

			req := baseRequest
			tt.mutate(&req)
			gotReload, err := server.updateRoutingConfig(req)
			if err != nil {
				t.Fatalf("updateRoutingConfig() error = %v", err)
			}
			if gotReload != tt.wantReload {
				t.Fatalf("reload = %v, want %v", gotReload, tt.wantReload)
			}
			if gotApply := routing.applyCalls > 0; gotApply != tt.wantApply {
				t.Fatalf("hot apply = %v (%d calls), want %v", gotApply, routing.applyCalls, tt.wantApply)
			}
			if cfg.Routing.LongLived.MinUptime != tt.wantUptime {
				t.Fatalf("uptime = %s, want %s", cfg.Routing.LongLived.MinUptime, tt.wantUptime)
			}
			if cfg.Routing.LongLived.MinSuccessRate != tt.wantRate {
				t.Fatalf("rate = %.2f, want %.2f", cfg.Routing.LongLived.MinSuccessRate, tt.wantRate)
			}
			if cfg.Routing.Session.TTL != tt.wantSession {
				t.Fatalf("session ttl = %s, want %s", cfg.Routing.Session.TTL, tt.wantSession)
			}
		})
	}
}

func TestUpdateRoutingConfigRejectsMalformedProviderBeforePersistence(t *testing.T) {
	useDefaults := true
	cfg := &config.Config{}
	cfg.Routing.Enabled = true
	cfg.Routing.DefaultStrategy = "stable"
	cfg.Routing.UseDefaultRules = &useDefaults
	cfg.Routing.FinalPolicy = "PROXY"
	cfg.Routing.RuleProviders = []config.RuleProvider{{
		URL:      "https://rules.example/direct.txt",
		Policy:   "DIRECT",
		Behavior: "domain",
		Interval: time.Hour,
	}}
	cfg.Routing.LongLived.MinUptime = 2 * time.Hour
	cfg.Routing.LongLived.MinSuccessRate = 0.9
	cfg.Routing.Session.TTL = 10 * time.Minute

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	originalFile := []byte("{}\n")
	if err := os.WriteFile(configPath, originalFile, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg.SetFilePath(configPath)

	routing := &recordingRoutingController{applyResult: true}
	server := &Server{
		cfgSrc:  cfg,
		routing: routing,
		logger:  log.New(io.Discard, "", 0),
	}

	_, err := server.updateRoutingConfig(routingConfigPayload{
		Enabled:            true,
		DefaultStrategy:    "stable",
		UseDefaultRules:    true,
		FinalPolicy:        "PROXY",
		RuleProviders:      []routingProviderConfig{{URL: "://invalid", Policy: "DIRECT", Behavior: "domain", Interval: "1h"}},
		LongLivedMinUptime: "2h",
		LongLivedMinRate:   0.9,
		SessionTTL:         "10m",
	})
	if err == nil {
		t.Error("updateRoutingConfig() error = nil, want malformed provider rejection")
	}
	if routing.applyCalls != 0 {
		t.Errorf("hot apply calls = %d, want 0", routing.applyCalls)
	}

	cfg.RLock()
	providers := append([]config.RuleProvider(nil), cfg.Routing.RuleProviders...)
	cfg.RUnlock()
	if len(providers) != 1 || providers[0].URL != "https://rules.example/direct.txt" {
		t.Errorf("runtime providers mutated after rejected update: %+v", providers)
	}

	persisted, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if !bytes.Equal(persisted, originalFile) {
		t.Errorf("config file changed after rejected update:\n%s", persisted)
	}
}

func TestUpdateRoutingConfigSaveFailureDoesNotMutateRuntimeConfig(t *testing.T) {
	useDefaults := true
	cfg := &config.Config{Routing: config.RoutingConfig{
		Enabled:         true,
		DefaultStrategy: "stable",
		UseDefaultRules: &useDefaults,
		FinalPolicy:     "PROXY",
		Rules:           []string{"DOMAIN,old.example,DIRECT"},
		LongLived:       config.LongLivedConfig{MinUptime: 2 * time.Hour, MinSuccessRate: 0.9},
		Session:         config.SessionConfig{TTL: 10 * time.Minute},
	}}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "missing", "config.yaml"))
	routing := &recordingRoutingController{applyResult: true}
	server := &Server{cfgSrc: cfg, routing: routing, logger: log.New(io.Discard, "", 0)}

	_, err := server.updateRoutingConfig(routingConfigPayload{
		Enabled:            true,
		DefaultStrategy:    "session",
		UseDefaultRules:    false,
		FinalPolicy:        "DIRECT",
		Rules:              []string{"DOMAIN,new.example,PROXY"},
		LongLivedMinUptime: "45m",
		LongLivedMinRate:   0.8,
		SessionTTL:         "10m",
	})
	if err == nil {
		t.Fatal("updateRoutingConfig() error = nil, want persistence failure")
	}
	cfg.RLock()
	defer cfg.RUnlock()
	if cfg.Routing.DefaultStrategy != "stable" || cfg.Routing.FinalPolicy != "PROXY" || len(cfg.Routing.Rules) != 1 || cfg.Routing.Rules[0] != "DOMAIN,old.example,DIRECT" {
		t.Fatalf("failed save mutated routing config: %+v", cfg.Routing)
	}
	if routing.applyCalls != 0 {
		t.Fatalf("failed save hot-applied routing %d times", routing.applyCalls)
	}
}
