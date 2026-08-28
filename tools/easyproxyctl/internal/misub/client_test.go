package misub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientLoginAndExport(t *testing.T) {
	data := map[string]interface{}{"profiles": []interface{}{}, "settings": map[string]interface{}{"x": true}, "sources": []interface{}{"a"}}
	canonical, _ := json.Marshal(data)
	digest := sha256.Sum256(canonical)
	exportData := map[string]interface{}{
		"format": "misub-logical-backup", "formatVersion": 1, "applicationVersion": "2.4.0", "schemaVersion": 2,
		"counts": map[string]int{"sources": 1}, "data": data,
		"integrity":        map[string]string{"canonicalDataSha256": hex.EncodeToString(digest[:])},
		"resourceIdentity": map[string]string{"deploymentName": "demo", "pagesProject": "demo-pages", "d1DatabaseId": "db-1", "d1Binding": "MISUB_DB"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/login":
			http.SetCookie(writer, &http.Cookie{Name: "auth_session", Value: "token", Path: "/"})
			_ = json.NewEncoder(writer).Encode(map[string]bool{"success": true})
		case "/api/system/export":
			if cookie, err := request.Cookie("auth_session"); err != nil || cookie.Value != "token" {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]interface{}{"success": true, "exportData": exportData})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Login(context.Background(), "secret"); err != nil {
		t.Fatal(err)
	}
	result, err := client.Export(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.D1DatabaseID != "db-1" || result.LogicalDataSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("unexpected export metadata: %+v", result)
	}
}

func TestClientRejectsChecksumMismatch(t *testing.T) {
	_, err := ValidateExport(json.RawMessage(`{"format":"misub-logical-backup","formatVersion":1,"counts":{},"data":{"sources":[],"profiles":[]},"integrity":{"canonicalDataSha256":"wrong"}}`))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
}

func TestClientRejectsCredentialsInBaseURL(t *testing.T) {
	if _, err := NewClient("https://user:password@example.com", time.Second); err == nil {
		t.Fatal("expected URL credentials to be rejected")
	}
}
