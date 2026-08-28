package topology

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validTopology = `schema_version: 1
deployment_name: easyproxy
release_channel: stable
components:
  aggregator: true
  misub: true
  ech_worker: true
  local_easyproxy: true
cloudflare:
  account_id_env: CLOUDFLARE_ACCOUNT_ID
  zone_id_env: ""
  use_pages_dev: true
  use_workers_dev: true
  resources:
    pages_project: ""
    d1_database: ""
    ech_worker: ""
    r2_bucket: ""
    r2_public_base_url: https://sub.example.com
aggregator:
  schedule: "0 */6 * * *"
misub:
  default_profile: easyproxies-ech-runtime
local:
  install_mode: docker
  access_mode: local_server
  release_channel: stable
secrets:
  cloudflare_api_token: CLOUDFLARE_API_TOKEN
  cloudflare_dns_token: ""
  misub_admin_password: MISUB_ADMIN_PASSWORD
  misub_cookie_secret: MISUB_COOKIE_SECRET
  misub_manifest_token: MISUB_MANIFEST_TOKEN
  misub_cron_secret: MISUB_CRON_SECRET
  ech_token: ECH_TOKEN
  r2_access_key_id: R2_ACCESS_KEY_ID
  r2_secret_access_key: R2_SECRET_ACCESS_KEY
`

func TestLoadValidTopology(t *testing.T) {
	loaded, err := Load(writeTopology(t, validTopology))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.DeploymentName != "easyproxy" || !loaded.Components.CloudEnabled() {
		t.Fatalf("unexpected topology: %#v", loaded)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	_, err := Load(writeTopology(t, validTopology+"unexpected: true\n"))
	if err == nil || !strings.Contains(err.Error(), "field unexpected not found") {
		t.Fatalf("Load() error = %v, want unknown-field error", err)
	}
}

func TestLoadRejectsMissingRequiredSwitch(t *testing.T) {
	input := strings.Replace(validTopology, "  ech_worker: true\n", "", 1)
	_, err := Load(writeTopology(t, input))
	if err == nil || !strings.Contains(err.Error(), "all component switches") {
		t.Fatalf("Load() error = %v, want required-switch error", err)
	}
}

func TestLoadRejectsResolvedSecret(t *testing.T) {
	input := strings.Replace(validTopology, "MISUB_ADMIN_PASSWORD", "a-literal-password", 1)
	_, err := Load(writeTopology(t, input))
	if err == nil || !strings.Contains(err.Error(), "environment variable name") {
		t.Fatalf("Load() error = %v, want environment-reference error", err)
	}
}

func writeTopology(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "topology.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
