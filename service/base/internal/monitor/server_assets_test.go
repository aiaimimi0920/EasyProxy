package monitor

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
)

func TestEmbeddedFrontendAssets(t *testing.T) {
	harness := newLocalServerMonitorWithEnabled(t, "", "", 0, false)

	get := func(path string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		harness.server.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		return recorder
	}

	indexResponse := get("/")
	if indexResponse.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", indexResponse.Code, http.StatusOK)
	}
	indexBody := append([]byte(nil), indexResponse.Body.Bytes()...)

	assetRefPattern := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)
	assetRefs := assetRefPattern.FindAllStringSubmatch(indexResponse.Body.String(), -1)
	if len(assetRefs) == 0 {
		t.Fatal("embedded index does not reference any /assets files")
	}
	seen := make(map[string]struct{}, len(assetRefs))
	for _, match := range assetRefs {
		path := match[1]
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		response := get(path)
		if response.Code != http.StatusOK {
			t.Errorf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
			continue
		}
		if response.Header().Get("Content-Type") == "text/html; charset=utf-8" {
			t.Errorf("GET %s returned HTML fallback instead of an asset", path)
		}
		if bytes.Equal(response.Body.Bytes(), indexBody) {
			t.Errorf("GET %s returned the embedded index instead of the asset", path)
		}
	}

	spaResponse := get("/devices/unknown-route")
	if spaResponse.Code != http.StatusOK {
		t.Fatalf("SPA fallback status = %d, want %d", spaResponse.Code, http.StatusOK)
	}
	spaBody, err := io.ReadAll(spaResponse.Body)
	if err != nil {
		t.Fatalf("read SPA fallback body: %v", err)
	}
	if !bytes.Equal(spaBody, indexBody) {
		t.Fatal("SPA fallback body does not match the embedded index")
	}

	apiResponse := get("/api/not-real")
	if apiResponse.Code != http.StatusNotFound {
		t.Fatalf("unknown API status = %d, want %d", apiResponse.Code, http.StatusNotFound)
	}
}
