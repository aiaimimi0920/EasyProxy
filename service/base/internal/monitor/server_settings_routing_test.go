package monitor

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"easy_proxies/internal/config"
)

func TestUpdateAllSettingsPropagatesSkipCertVerifyToManager(t *testing.T) {
	cfg := &config.Config{}
	cfg.Mode = "pool"
	cfg.LogLevel = "info"
	cfg.Listener.Address = "0.0.0.0"
	cfg.Listener.Port = 8080
	cfg.Listener.Protocol = "http"
	cfg.MultiPort.Address = "0.0.0.0"
	cfg.MultiPort.BasePort = 10000
	cfg.MultiPort.Protocol = "http"
	cfg.Pool.Mode = "auto"
	cfg.Pool.BlacklistDuration = time.Minute
	cfg.SubscriptionRefresh.Interval = time.Minute
	cfg.SubscriptionRefresh.Timeout = 30 * time.Second
	cfg.SubscriptionRefresh.HealthCheckTimeout = 30 * time.Second
	cfg.SubscriptionRefresh.DrainTimeout = 10 * time.Second
	cfg.SourceSync.RefreshInterval = time.Minute
	cfg.SourceSync.RequestTimeout = 30 * time.Second
	cfg.GeoIP.AutoUpdateInterval = time.Hour
	cfg.Management.HealthCheckInterval = time.Minute
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg.SetFilePath(configPath)

	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	s := &Server{
		cfg:    Config{},
		cfgSrc: cfg,
		mgr:    mgr,
		logger: log.New(io.Discard, "", 0),
	}

	req := allSettingsRequest{
		Mode:                          "pool",
		LogLevel:                      "info",
		ListenerAddress:               "0.0.0.0",
		ListenerPort:                  8080,
		ListenerProtocol:              "http",
		MultiPortAddress:              "0.0.0.0",
		MultiPortBasePort:             10000,
		MultiPortProtocol:             "http",
		PoolMode:                      "auto",
		PoolBlacklistDuration:         "1m",
		SubRefreshInterval:            "1m",
		SubRefreshTimeout:             "30s",
		SubRefreshHealthCheckTimeout:  "30s",
		SubRefreshDrainTimeout:        "10s",
		SourceSyncRefreshInterval:     "1m",
		SourceSyncRequestTimeout:      "30s",
		GeoIPAutoUpdateInterval:       "1h",
		ManagementHealthCheckInterval: "1m",
		SkipCertVerify:                true,
	}

	if err := s.updateAllSettings(req); err != nil {
		t.Fatalf("updateAllSettings() error = %v", err)
	}
	if !mgr.SkipCertVerify() {
		t.Fatal("expected manager skip_cert_verify to update immediately")
	}

	req.SkipCertVerify = false
	if err := s.updateAllSettings(req); err != nil {
		t.Fatalf("second updateAllSettings() error = %v", err)
	}
	if mgr.SkipCertVerify() {
		t.Fatal("expected manager skip_cert_verify to reflect latest settings update")
	}
}

func TestUpdateAllSettingsSaveFailureDoesNotMutateRuntimeConfig(t *testing.T) {
	cfg := &config.Config{
		Mode:      "pool",
		LogLevel:  "info",
		Listener:  config.ListenerConfig{Address: "0.0.0.0", Port: 8080, Protocol: "http"},
		MultiPort: config.MultiPortConfig{Address: "0.0.0.0", BasePort: 10000, Protocol: "http"},
		Pool:      config.PoolConfig{Mode: "auto", BlacklistDuration: time.Minute},
		SubscriptionRefresh: config.SubscriptionRefreshConfig{
			Interval:           time.Minute,
			Timeout:            30 * time.Second,
			HealthCheckTimeout: 30 * time.Second,
			DrainTimeout:       10 * time.Second,
		},
		SourceSync: config.SourceSyncConfig{RefreshInterval: time.Minute, RequestTimeout: 30 * time.Second},
		GeoIP:      config.GeoIPConfig{AutoUpdateInterval: time.Hour},
		Management: config.ManagementConfig{HealthCheckInterval: time.Minute},
	}
	cfg.SetFilePath(filepath.Join(t.TempDir(), "missing", "config.yaml"))
	mgr, err := NewManager(Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	server := &Server{cfgSrc: cfg, mgr: mgr, logger: log.New(io.Discard, "", 0)}

	_, err = server.updateAllSettingsWithReload(allSettingsRequest{
		Mode:                          "hybrid",
		LogLevel:                      "debug",
		ListenerAddress:               "127.0.0.1",
		ListenerPort:                  18080,
		ListenerProtocol:              "http",
		MultiPortAddress:              "127.0.0.1",
		MultiPortBasePort:             20000,
		MultiPortProtocol:             "http",
		PoolMode:                      "random",
		PoolBlacklistDuration:         "2m",
		SubRefreshInterval:            "2m",
		SubRefreshTimeout:             "40s",
		SubRefreshHealthCheckTimeout:  "40s",
		SubRefreshDrainTimeout:        "20s",
		SourceSyncRefreshInterval:     "2m",
		SourceSyncRequestTimeout:      "40s",
		GeoIPAutoUpdateInterval:       "2h",
		ManagementHealthCheckInterval: "2m",
		SkipCertVerify:                true,
	})
	if err == nil {
		t.Fatal("updateAllSettingsWithReload() error = nil, want persistence failure")
	}
	cfg.RLock()
	defer cfg.RUnlock()
	if cfg.Mode != "pool" || cfg.LogLevel != "info" || cfg.Listener.Port != 8080 || cfg.SkipCertVerify {
		t.Fatalf("failed save mutated runtime config: mode=%q log=%q port=%d skip=%v", cfg.Mode, cfg.LogLevel, cfg.Listener.Port, cfg.SkipCertVerify)
	}
	if mgr.SkipCertVerify() {
		t.Fatal("failed save mutated monitor manager TLS behavior")
	}
}

func TestHandleSettingsReportsReloadRequirement(t *testing.T) {
	makeServer := func(t *testing.T, initialMode string, initialSkip bool) (*Server, *config.Config) {
		t.Helper()

		cfg := &config.Config{}
		cfg.Mode = initialMode
		cfg.LogLevel = "info"
		cfg.Listener.Address = "0.0.0.0"
		cfg.Listener.Port = 8080
		cfg.Listener.Protocol = "http"
		cfg.MultiPort.Address = "0.0.0.0"
		cfg.MultiPort.BasePort = 10000
		cfg.MultiPort.Protocol = "http"
		cfg.Pool.Mode = "auto"
		cfg.Pool.BlacklistDuration = time.Minute
		cfg.SubscriptionRefresh.Interval = time.Minute
		cfg.SubscriptionRefresh.Timeout = 30 * time.Second
		cfg.SubscriptionRefresh.HealthCheckTimeout = 30 * time.Second
		cfg.SubscriptionRefresh.DrainTimeout = 10 * time.Second
		cfg.SourceSync.Enabled = false
		cfg.SourceSync.ManifestURL = ""
		cfg.SourceSync.ManifestToken = ""
		cfg.SourceSync.RefreshInterval = time.Minute
		cfg.SourceSync.RequestTimeout = 30 * time.Second
		cfg.SourceSync.DefaultDirectProxyScheme = "http"
		cfg.GeoIP.AutoUpdateInterval = time.Hour
		cfg.Management.HealthCheckInterval = time.Minute
		cfg.Management.Listen = "0.0.0.0:9888"
		cfg.Management.ProbeTarget = ""
		cfg.Management.ProbeTargets = nil
		cfg.Management.Password = ""
		cfg.SkipCertVerify = initialSkip

		configPath := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(configPath, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		cfg.SetFilePath(configPath)

		mgr, err := NewManager(Config{})
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}

		s := &Server{
			cfg:    Config{},
			cfgSrc: cfg,
			mgr:    mgr,
			logger: log.New(io.Discard, "", 0),
		}
		return s, cfg
	}

	type testCase struct {
		name        string
		initialMode string
		initialSkip bool
		req         allSettingsRequest
		wantReload  bool
	}

	baseReq := allSettingsRequest{
		Mode:                          "pool",
		LogLevel:                      "info",
		ListenerAddress:               "0.0.0.0",
		ListenerPort:                  8080,
		ListenerProtocol:              "http",
		MultiPortAddress:              "0.0.0.0",
		MultiPortBasePort:             10000,
		MultiPortProtocol:             "http",
		PoolMode:                      "auto",
		PoolBlacklistDuration:         "1m",
		SubRefreshInterval:            "1m",
		SubRefreshTimeout:             "30s",
		SubRefreshHealthCheckTimeout:  "30s",
		SubRefreshDrainTimeout:        "10s",
		SourceSyncRefreshInterval:     "1m",
		SourceSyncRequestTimeout:      "30s",
		GeoIPAutoUpdateInterval:       "1h",
		ManagementListen:              "0.0.0.0:9888",
		ManagementHealthCheckInterval: "1m",
	}

	cases := []testCase{
		{
			name:        "skip cert verify requires box reload",
			initialMode: "pool",
			initialSkip: false,
			req: func() allSettingsRequest {
				r := baseReq
				r.SkipCertVerify = true
				return r
			}(),
			wantReload: true,
		},
		{
			name:        "mode change",
			initialMode: "pool",
			initialSkip: false,
			req: func() allSettingsRequest {
				r := baseReq
				r.Mode = "multi-port"
				return r
			}(),
			wantReload: true,
		},
		{
			name:        "management listen change",
			initialMode: "pool",
			initialSkip: false,
			req: func() allSettingsRequest {
				r := baseReq
				r.ManagementListen = "127.0.0.1:9999"
				return r
			}(),
			wantReload: true,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := makeServer(t, tt.initialMode, tt.initialSkip)
			body, err := json.Marshal(tt.req)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}

			req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body))
			rec := httptest.NewRecorder()

			s.handleSettings(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
			}

			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			if got := payload["need_reload"]; got != tt.wantReload {
				t.Fatalf("expected need_reload=%v, got %v", tt.wantReload, got)
			}
		})
	}
}
