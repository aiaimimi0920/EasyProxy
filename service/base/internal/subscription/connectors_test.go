package subscription

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"easy_proxies/internal/config"

	"gopkg.in/yaml.v3"
)

type fakeConnectorRuntime struct {
	got      []RuntimeSource
	returned []RuntimeSource
	err      error
}

type connectorRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn connectorRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type connectorTimeoutError struct{}

func (connectorTimeoutError) Error() string { return "temporary timeout" }

func (connectorTimeoutError) Timeout() bool { return true }

func (connectorTimeoutError) Temporary() bool { return true }

func (f *fakeConnectorRuntime) Reconcile(_ *config.Config, sources []RuntimeSource) ([]RuntimeSource, error) {
	f.got = append([]RuntimeSource(nil), sources...)
	return append([]RuntimeSource(nil), f.returned...), f.err
}

func (f *fakeConnectorRuntime) StopAll() error {
	return nil
}

func preferredIPTestConfig(t *testing.T, name, accessToken string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{
		Connectors: []config.ConnectorSourceConfig{
			{
				Name:          name,
				Input:         "https://ech.example.com/connect",
				Enabled:       false,
				TemplateOnly:  true,
				ConnectorType: connectorTypeECHWorker,
				ConnectorConfig: map[string]any{
					"access_token": accessToken,
				},
			},
		},
	}
	cfg.SetFilePath(path)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return cfg
}

func cloneConnectors(connectors []config.ConnectorSourceConfig) []config.ConnectorSourceConfig {
	cloned := make([]config.ConnectorSourceConfig, len(connectors))
	for index, connector := range connectors {
		cloned[index] = cloneConnectorConfig(connector)
	}
	return cloned
}

func assertPreferredConnectors(t *testing.T, cfg *config.Config, want []config.ConnectorSourceConfig) {
	t.Helper()
	cfg.RLock()
	defer cfg.RUnlock()
	var got []config.ConnectorSourceConfig
	for _, connector := range cfg.Connectors {
		if strings.HasPrefix(connector.Name, preferredConnectorNamePrefix("Old Template")) {
			got = append(got, cloneConnectorConfig(connector))
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("preferred connectors = %#v, want %#v", got, want)
	}
}
