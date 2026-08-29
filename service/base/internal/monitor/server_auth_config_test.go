package monitor

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"easy_proxies/internal/config"
	"easy_proxies/internal/profile"
)

func TestLocalServerAuthRequiresCanonicalPair(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 4)
	status := performJSONRequest(t, harness.server, http.MethodGet, "/api/auth", nil, nil)
	if status.Body["auth_mode"] != "canonical_pair" || status.Body["username_required"] != true {
		t.Fatalf("auth status = %#v", status.Body)
	}
	login := performJSONRequest(t, harness.server, http.MethodPost, "/api/auth", map[string]any{
		"username": "easyproxy", "password": "secret",
	}, nil)
	if login.Code != http.StatusOK || login.Body["token"] == "" {
		t.Fatalf("login = %#v", login)
	}
}

func TestLocalServerManagementAcceptsCanonicalBasicAuth(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	headers := make(http.Header)
	headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("easyproxy:secret")))
	response := performJSONRequest(t, harness.server, http.MethodGet, "/api/settings", nil, headers)
	if response.Code != http.StatusOK {
		t.Fatalf("Basic management auth status = %d body=%#v", response.Code, response.Body)
	}
}

func TestManagementDoesNotAcceptProxyAuthorization(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	headers := make(http.Header)
	headers.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("easyproxy:secret")))
	response := performJSONRequest(t, harness.server, http.MethodGet, "/api/settings", nil, headers)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("Proxy-Authorization status = %d", response.Code)
	}
}

func TestSessionGenerationInvalidatesOldSession(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "old", 2)
	session, err := harness.server.createSession()
	if err != nil {
		t.Fatal(err)
	}
	harness.profiles.PublishCredentials(profile.CredentialSnapshot{Username: "easyproxy", Password: "new", Generation: 3})
	if harness.server.validateSession(session.Token) {
		t.Fatal("old-generation session remained valid")
	}
}

func TestLocalServerConfigDoesNotReturnPassword(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	headers := make(http.Header)
	headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("easyproxy:secret")))
	response := performJSONRequest(t, harness.server, http.MethodGet, "/api/local-server/config", nil, headers)
	if response.Code != http.StatusOK || response.Body["auth_username"] != "easyproxy" || response.Body["password_set"] != true {
		t.Fatalf("Local Server config response = %#v", response)
	}
	if _, ok := response.Body["auth_password"]; ok {
		t.Fatalf("Local Server config leaked password: %#v", response.Body)
	}
}

func TestLocalServerSettingsDoNotExposeOrAcceptLegacyPasswords(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	harness.config.Lock()
	harness.config.MultiPort.Password = "legacy-multi-secret"
	harness.config.Unlock()
	settings := performAuthedJSON(t, harness.server, http.MethodGet, "/api/settings", nil)
	encoded, _ := json.Marshal(settings.Body)
	if settings.Code != http.StatusOK || bytes.Contains(encoded, []byte("listener_password")) || bytes.Contains(encoded, []byte("management_password")) || bytes.Contains(encoded, []byte("multi_port_password")) {
		t.Fatalf("legacy password leaked: %s", encoded)
	}
	if settings.Body["local_server_enabled"] != true || settings.Body["local_server_auth_username"] != "easyproxy" || settings.Body["local_server_password_set"] != true {
		t.Fatalf("canonical settings = %#v", settings.Body)
	}

	conflict := performAuthedJSON(t, harness.server, http.MethodPut, "/api/settings", map[string]any{
		"listener_password":   "legacy-secret",
		"management_password": "legacy-secret",
	})
	if conflict.Code != http.StatusConflict || conflict.Body["error"] != "credential_source_conflict" {
		t.Fatalf("legacy credential update = %#v", conflict)
	}
	multiPortConflict := performAuthedJSON(t, harness.server, http.MethodPut, "/api/settings", map[string]any{
		"multi_port_password": "legacy-multi-secret",
	})
	if multiPortConflict.Code != http.StatusConflict || multiPortConflict.Body["error"] != "credential_source_conflict" {
		t.Fatalf("legacy multi-port credential update = %#v", multiPortConflict)
	}

	delete(settings.Body, "local_server_enabled")
	delete(settings.Body, "local_server_auth_username")
	delete(settings.Body, "local_server_password_set")
	settings.Body["log_level"] = "debug"
	settings.Body["multi_port_base_port"] = float64(30000)
	settings.Body["multi_port_protocol"] = "http"
	settings.Body["pool_mode"] = "auto"
	for _, key := range []string{
		"pool_blacklist_duration",
		"sub_refresh_interval",
		"sub_refresh_timeout",
		"sub_refresh_health_check_timeout",
		"sub_refresh_drain_timeout",
		"source_sync_refresh_interval",
		"source_sync_request_timeout",
		"geoip_auto_update_interval",
		"management_health_check_interval",
	} {
		delete(settings.Body, key)
	}
	preserved := performAuthedJSON(t, harness.server, http.MethodPut, "/api/settings", settings.Body)
	if preserved.Code != http.StatusOK {
		t.Fatalf("sanitized settings save = %#v", preserved)
	}
	credentials := harness.profiles.Credentials()
	if credentials.Username != "easyproxy" || credentials.Password != "secret" {
		t.Fatalf("canonical credentials changed after sanitized save: %#v", credentials)
	}
	harness.config.RLock()
	if harness.config.Listener.Username != "easyproxy" || harness.config.Listener.Password != "secret" || harness.config.Management.Password != "secret" {
		t.Fatalf("derived credentials changed after sanitized save: listener=%#v management=%q", harness.config.Listener, harness.config.Management.Password)
	}
	harness.config.RUnlock()
}

func TestLocalServerSettingsRemainSanitizedDuringPendingDisable(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	response := performAuthedJSON(t, harness.server, http.MethodPut, "/api/local-server/config", map[string]any{
		"enabled": false,
	})
	if response.Code != http.StatusOK || response.Body["need_reload"] != true {
		t.Fatalf("pending disable = %#v", response)
	}
	settings := performAuthedJSON(t, harness.server, http.MethodGet, "/api/settings", nil)
	encoded, _ := json.Marshal(settings.Body)
	if settings.Code != http.StatusOK || bytes.Contains(encoded, []byte("listener_password")) || bytes.Contains(encoded, []byte("management_password")) || bytes.Contains(encoded, []byte("multi_port_password")) {
		t.Fatalf("pending disable leaked legacy passwords: %s", encoded)
	}
}

func TestLocalServerCredentialRotationPublishesWithoutReload(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "old-secret", 2)
	session, err := harness.server.createSession()
	if err != nil {
		t.Fatal(err)
	}
	headers := make(http.Header)
	headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("easyproxy:old-secret")))
	response := performJSONRequest(t, harness.server, http.MethodPut, "/api/local-server/config", map[string]any{
		"auth_password": "new-secret",
	}, headers)
	if response.Code != http.StatusOK || response.Body["need_reload"] != false {
		t.Fatalf("credential rotation response = %#v", response)
	}
	resource, ok := response.Body["resource"].(map[string]any)
	if !ok || resource["auth_username"] != "easyproxy" || resource["password_set"] != true {
		t.Fatalf("credential rotation resource = %#v", response.Body["resource"])
	}
	if _, exists := resource["auth_password"]; exists {
		t.Fatalf("credential rotation leaked password: %#v", resource)
	}
	credentials := harness.profiles.Credentials()
	if credentials.Password != "new-secret" || credentials.Generation != 3 {
		t.Fatalf("published credentials = %#v", credentials)
	}
	if harness.server.validateSession(session.Token) {
		t.Fatal("credential rotation left old session valid")
	}
}

func TestLocalServerStructuralChangeDoesNotPublishCredentialsBeforeReload(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "old-secret", 2)
	headers := make(http.Header)
	headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("easyproxy:old-secret")))
	response := performJSONRequest(t, harness.server, http.MethodPut, "/api/local-server/config", map[string]any{
		"listen":        "127.0.0.1:32323",
		"auth_password": "new-secret",
	}, headers)
	if response.Code != http.StatusOK || response.Body["need_reload"] != true {
		t.Fatalf("structural update response = %#v", response)
	}
	credentials := harness.profiles.Credentials()
	if credentials.Password != "old-secret" || credentials.Generation != 2 {
		t.Fatalf("structural update published credentials early: %#v", credentials)
	}
}

func TestLocalServerPendingStructuralChangeKeepsLaterCredentialsPending(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "old-secret", 2)
	headers := make(http.Header)
	headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("easyproxy:old-secret")))
	first := performJSONRequest(t, harness.server, http.MethodPut, "/api/local-server/config", map[string]any{
		"listen":        "127.0.0.1:32323",
		"auth_password": "pending-secret",
	}, headers)
	if first.Code != http.StatusOK || first.Body["need_reload"] != true {
		t.Fatalf("structural update response = %#v", first)
	}
	second := performJSONRequest(t, harness.server, http.MethodPut, "/api/local-server/config", map[string]any{
		"auth_password": "final-secret",
	}, headers)
	if second.Code != http.StatusOK || second.Body["need_reload"] != true {
		t.Fatalf("follow-up credential response = %#v", second)
	}
	credentials := harness.profiles.Credentials()
	if credentials.Password != "old-secret" || credentials.Generation != 2 {
		t.Fatalf("pending structural update published credentials early: %#v", credentials)
	}
}

func TestLocalServerRejectsEmptyPasswordUpdate(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	headers := make(http.Header)
	headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("easyproxy:secret")))
	response := performJSONRequest(t, harness.server, http.MethodPut, "/api/local-server/config", map[string]any{
		"auth_password": "",
	}, headers)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty password status = %d body=%#v", response.Code, response.Body)
	}
}

func TestLocalServerConfigPreservesPasswordWhenOmitted(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 4)
	headers := make(http.Header)
	headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("easyproxy:secret")))
	response := performJSONRequest(t, harness.server, http.MethodPut, "/api/local-server/config", map[string]any{
		"auth_username": "rotated-user",
	}, headers)
	if response.Code != http.StatusOK || response.Body["need_reload"] != false {
		t.Fatalf("username rotation response = %#v", response)
	}
	credentials := harness.profiles.Credentials()
	if credentials.Username != "rotated-user" || credentials.Password != "secret" || credentials.Generation != 5 {
		t.Fatalf("published credentials = %#v", credentials)
	}
	harness.config.RLock()
	defer harness.config.RUnlock()
	if harness.config.Listener.Username != "rotated-user" || harness.config.Listener.Password != "secret" || harness.config.Management.Password != "secret" {
		t.Fatalf("derived credentials = listener=%#v management=%q", harness.config.Listener, harness.config.Management.Password)
	}
}

func TestLocalServerConfigRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
	}{
		{name: "username characters", body: map[string]any{"auth_username": "bad user"}},
		{name: "username length", body: map[string]any{"auth_username": strings.Repeat("a", 65)}},
		{name: "password nul", body: map[string]any{"auth_password": "bad\x00secret"}},
		{name: "password length", body: map[string]any{"auth_password": strings.Repeat("a", 257)}},
		{name: "listen syntax", body: map[string]any{"listen": "not-a-listen"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
			headers := make(http.Header)
			headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("easyproxy:secret")))
			response := performJSONRequest(t, harness.server, http.MethodPut, "/api/local-server/config", tt.body, headers)
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d body=%#v", response.Code, response.Body)
			}
		})
	}
}

func TestLocalServerConfigRejectsListenConflict(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	harness.config.Lock()
	harness.config.Routing.Listen = "127.0.0.1:32324"
	harness.config.Unlock()
	headers := make(http.Header)
	headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("easyproxy:secret")))
	response := performJSONRequest(t, harness.server, http.MethodPut, "/api/local-server/config", map[string]any{
		"listen": "127.0.0.1:32323",
	}, headers)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d body=%#v", response.Code, response.Body)
	}
}

func TestLocalServerConfigRejectsReloadWindow(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	harness.server.BeginReloadWindow()
	defer harness.server.EndReloadWindow()
	headers := make(http.Header)
	headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("easyproxy:secret")))
	response := performJSONRequest(t, harness.server, http.MethodPut, "/api/local-server/config", map[string]any{
		"auth_password": "rotated-secret",
	}, headers)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%#v", response.Code, response.Body)
	}
	if got := harness.profiles.Credentials().Password; got != "secret" {
		t.Fatalf("credentials changed during reload window: %q", got)
	}
}

func TestLocalServerConfigSaveFailureDoesNotPublish(t *testing.T) {
	harness := newLocalServerMonitor(t, "easyproxy", "secret", 1)
	harness.config.SetFilePath(filepath.Join(t.TempDir(), "missing", "config.yaml"))
	headers := make(http.Header)
	headers.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("easyproxy:secret")))
	response := performJSONRequest(t, harness.server, http.MethodPut, "/api/local-server/config", map[string]any{
		"auth_password": "rotated-secret",
	}, headers)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%#v", response.Code, response.Body)
	}
	credentials := harness.profiles.Credentials()
	if credentials.Password != "secret" || credentials.Generation != 1 {
		t.Fatalf("credentials changed after save failure: %#v", credentials)
	}
}

func TestLocalServerConfigEnableIncrementsGenerationOnReload(t *testing.T) {
	harness := newLocalServerMonitorWithEnabled(t, "easyproxy", "secret", 1, false)
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer secret")
	response := performJSONRequest(t, harness.server, http.MethodPut, "/api/local-server/config", map[string]any{
		"enabled": true,
	}, headers)
	if response.Code != http.StatusOK || response.Body["need_reload"] != true {
		t.Fatalf("enable response = %#v", response)
	}
	harness.config.RLock()
	gotGeneration := harness.config.LocalServer.CredentialGeneration
	harness.config.RUnlock()
	if gotGeneration != 2 {
		t.Fatalf("persisted generation = %d, want 2", gotGeneration)
	}
	if credentials := harness.profiles.Credentials(); credentials.Generation != 1 || harness.profiles.LocalServerEnabled() {
		t.Fatalf("active profile state changed before reload: %#v enabled=%v", credentials, harness.profiles.LocalServerEnabled())
	}
}

func TestLocalServerConfigEnableMigratesLegacyCredentials(t *testing.T) {
	harness := newLocalServerMonitorWithEnabled(t, "placeholder", "legacy-secret", 1, false)
	harness.config.Lock()
	harness.config.LocalServer.Auth = config.LocalServerAuthConfig{}
	harness.config.Listener.Username = "legacy-user"
	harness.config.Listener.Password = "legacy-secret"
	harness.config.Management.Password = "legacy-secret"
	harness.config.Unlock()
	harness.server.SetConfig(harness.config)

	headers := make(http.Header)
	headers.Set("Authorization", "Bearer legacy-secret")
	response := performJSONRequest(t, harness.server, http.MethodPut, "/api/local-server/config", map[string]any{
		"enabled": true,
	}, headers)
	if response.Code != http.StatusOK || response.Body["need_reload"] != true {
		t.Fatalf("enable response = %#v", response)
	}
	harness.config.RLock()
	defer harness.config.RUnlock()
	if harness.config.LocalServer.Auth.Username != "legacy-user" || harness.config.LocalServer.Auth.Password != "legacy-secret" {
		t.Fatalf("canonical credentials = %#v", harness.config.LocalServer.Auth)
	}
	if harness.config.LocalServer.CredentialGeneration != 2 {
		t.Fatalf("credential generation = %d, want 2", harness.config.LocalServer.CredentialGeneration)
	}
}

func TestLocalServerConfigEnableRejectsLegacyCredentialConflict(t *testing.T) {
	harness := newLocalServerMonitorWithEnabled(t, "placeholder", "management-secret", 1, false)
	harness.config.Lock()
	harness.config.LocalServer.Auth = config.LocalServerAuthConfig{}
	harness.config.Listener.Username = "legacy-user"
	harness.config.Listener.Password = "listener-secret"
	harness.config.Management.Password = "management-secret"
	harness.config.Unlock()
	harness.server.SetConfig(harness.config)

	headers := make(http.Header)
	headers.Set("Authorization", "Bearer management-secret")
	response := performJSONRequest(t, harness.server, http.MethodPut, "/api/local-server/config", map[string]any{
		"enabled": true,
	}, headers)
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%#v", response.Code, response.Body)
	}
}
