package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func migrationDirectory(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "..", "upstreams", "misub", "migrations")
}

func TestLoadReviewedMigrationsRejectsUnreviewedOrMissingSQL(t *testing.T) {
	plan, err := LoadReviewedMigrations(migrationDirectory(t))
	if err != nil || len(plan.Statements) != 11 {
		t.Fatalf("reviewed plan rejected: %v statements=%d", err, len(plan.Statements))
	}
	for _, change := range []string{"changed", "extra", "missing", "directory"} {
		t.Run(change, func(t *testing.T) {
			directory := t.TempDir()
			for _, item := range reviewedMigrations {
				body, err := os.ReadFile(filepath.Join(migrationDirectory(t), item.name))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, item.name), body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(directory, reviewedMigrations[0].name)
			switch change {
			case "changed":
				_ = os.WriteFile(path, []byte("DROP TABLE subscriptions;"), 0o600)
			case "extra":
				_ = os.WriteFile(filepath.Join(directory, "0003_extra.sql"), []byte("SELECT 1;"), 0o600)
			case "missing":
				_ = os.Remove(path)
			case "directory":
				_ = os.Remove(path)
				_ = os.Mkdir(path, 0o700)
			}
			if _, err := LoadReviewedMigrations(directory); err == nil {
				t.Fatal("unreviewed migrations accepted")
			}
		})
	}
}

func TestD1SingleWriterDoesNotRetryOrRedirect(t *testing.T) {
	for _, outcome := range []string{"success", "redirect", "unavailable", "api-error", "statement-error", "empty", "malformed", "lost"} {
		t.Run(outcome, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				calls++
				if request.Method != "POST" || request.Header.Get("Authorization") != "Bearer synthetic" || request.Header.Get("Idempotency-Key") != "" {
					t.Error("unexpected mutation request")
				}
				var payload map[string]string
				if json.NewDecoder(request.Body).Decode(&payload) != nil || payload["sql"] != "CREATE TABLE x(y)" {
					t.Error("mutation body mismatch")
				}
				switch outcome {
				case "redirect":
					response.Header().Set("Location", "/must-not-follow")
					response.WriteHeader(307)
				case "unavailable":
					response.WriteHeader(503)
				case "api-error":
					_, _ = response.Write([]byte(`{"success":false,"result":[{"success":true}],"errors":[{"message":"sensitive"}]}`))
				case "statement-error":
					_, _ = response.Write([]byte(`{"success":true,"result":[{"success":false}]}`))
				case "empty":
					_, _ = response.Write([]byte(`{"success":true,"result":[]}`))
				case "malformed":
					_, _ = response.Write([]byte(`{"sensitive":"provider diagnostic"`))
				case "lost":
					connection, _, _ := response.(http.Hijacker).Hijack()
					_ = connection.Close()
				default:
					_, _ = response.Write([]byte(`{"success":true,"result":[{"success":true}]}`))
				}
			}))
			defer server.Close()
			writer, err := NewD1SingleWriter(strings.Repeat("a", 32), "synthetic")
			if err != nil {
				t.Fatal(err)
			}
			writer.endpoint = server.URL // Only package tests can replace the fixed endpoint.
			err = writer.Apply(context.Background(), "11111111-2222-3333-4444-555555555555", MigrationPlan{Statements: []string{"CREATE TABLE x(y)"}})
			if (outcome == "success") != (err == nil) || calls != 1 {
				t.Fatalf("outcome=%s err=%v calls=%d", outcome, err, calls)
			}
			if err != nil && !errors.Is(err, ErrMigrationUnknown) {
				t.Fatal("provider diagnostic escaped safe error boundary")
			}
		})
	}
}

func TestD1SingleWriterStopsAtFirstUnknownStatement(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 2 {
			response.WriteHeader(503)
			return
		}
		_, _ = response.Write([]byte(`{"success":true,"result":[{"success":true}]}`))
	}))
	defer server.Close()
	writer, _ := NewD1SingleWriter(strings.Repeat("a", 32), "synthetic")
	writer.endpoint = server.URL
	err := writer.Apply(context.Background(), "11111111-2222-3333-4444-555555555555", MigrationPlan{Statements: []string{"first", "second", "never"}})
	if !errors.Is(err, ErrMigrationUnknown) || calls != 2 {
		t.Fatalf("unexpected statement replay/continuation: %v calls=%d", err, calls)
	}
}

func TestD1SingleWriterRejectsVerifyPlaceholderAndIdentity(t *testing.T) {
	for _, token := range []string{"", "verification-not-applicable", "line\nbreak"} {
		if _, err := NewD1SingleWriter(strings.Repeat("a", 32), token); err == nil {
			t.Fatal("invalid credentials accepted")
		}
	}
	if _, err := NewD1SingleWriter("../elsewhere", "synthetic"); err == nil {
		t.Fatal("invalid account accepted")
	}
	writer, _ := NewD1SingleWriter(strings.Repeat("a", 32), "synthetic")
	if err := writer.Apply(context.Background(), "../other", MigrationPlan{Statements: []string{"sql"}}); err == nil {
		t.Fatal("invalid database accepted")
	}
}
