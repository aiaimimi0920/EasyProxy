package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestMigration4PreservesExistingRows(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "data.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := rawDB.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT ''
		);
	`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for _, migration := range allMigrations()[:3] {
		if _, err := rawDB.Exec(migration.Up); err != nil {
			t.Fatalf("apply migration %d seed: %v", migration.Version, err)
		}
		if _, err := rawDB.Exec(
			`INSERT INTO schema_migrations (version, applied_at, description) VALUES (?, ?, ?)`,
			migration.Version,
			time.Now().UTC().Format(time.RFC3339),
			migration.Description,
		); err != nil {
			t.Fatalf("record migration %d seed: %v", migration.Version, err)
		}
	}

	wantNode := Node{
		URI:     "socks5://127.0.0.1:1080",
		Name:    "preserved-node",
		Source:  NodeSourceManual,
		Port:    1080,
		Enabled: true,
	}
	if _, err := rawDB.Exec(
		`INSERT INTO nodes (uri, name, source, port, username, password, region, country, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, '', '', '', '', 1, ?, ?)`,
		wantNode.URI,
		wantNode.Name,
		wantNode.Source,
		wantNode.Port,
		time.Now().UTC().Format(time.RFC3339),
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	wantSession := Session{
		Token:     "preserved-token",
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if _, err := rawDB.Exec(
		`INSERT INTO sessions (token, created_at, expires_at) VALUES (?, ?, ?)`,
		wantSession.Token,
		wantSession.CreatedAt.Format(time.RFC3339),
		wantSession.ExpiresAt.Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open upgraded store: %v", err)
	}
	defer st.Close()

	rawDB, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer rawDB.Close()

	version, err := CurrentVersion(rawDB)
	if err != nil {
		t.Fatalf("CurrentVersion: %v", err)
	}
	if version != 4 {
		t.Fatalf("schema version = %d, want 4", version)
	}

	nodes, err := st.ListNodes(ctx, NodeFilter{})
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != wantNode.Name {
		t.Fatalf("nodes = %#v, want preserved node", nodes)
	}

	gotSession, err := st.GetSession(ctx, wantSession.Token)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if gotSession == nil {
		t.Fatal("GetSession returned nil, want preserved session")
	}
	if gotSession.Token != wantSession.Token {
		t.Fatalf("session token = %q, want %q", gotSession.Token, wantSession.Token)
	}
	if gotSession.CredentialGeneration != 1 {
		t.Fatalf("session credential_generation = %d, want default 1 after migration", gotSession.CredentialGeneration)
	}
}

func TestLocalServerMigrationAndProfileCAS(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	device, err := st.PutDevice(ctx, Device{DeviceID: "laptop", DisplayName: "Laptop"}, 0)
	if err != nil || device.Revision != 1 {
		t.Fatalf("device=%#v err=%v", device, err)
	}
	created, err := st.PutDeviceProfile(ctx, DeviceProfile{
		DeviceID:      "laptop",
		ProfileJSON:   []byte(`{"schema_version":1,"enabled":true}`),
		SchemaVersion: 1,
	}, 0)
	if err != nil || created.Revision != 1 {
		t.Fatalf("profile=%#v err=%v", created, err)
	}
	var conflict *RevisionConflictError
	if _, err := st.PutDeviceProfile(ctx, created, 0); !errors.As(err, &conflict) {
		t.Fatalf("expected revision conflict, got %v", err)
	}
	deleted, err := st.DeleteDeviceProfile(ctx, "laptop", created.Revision)
	if err != nil || !deleted {
		t.Fatalf("deleted=%v err=%v", deleted, err)
	}
	deleted, err = st.DeleteDeviceProfile(ctx, "laptop", created.Revision)
	if err != nil || deleted {
		t.Fatalf("idempotent delete=%v err=%v", deleted, err)
	}
}

func TestPutDeviceProfileCreatesDeviceAtomically(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	created, err := st.PutDeviceProfile(ctx, DeviceProfile{
		DeviceID:      "tablet",
		ProfileJSON:   []byte(`{"schema_version":1,"enabled":false}`),
		SchemaVersion: 1,
	}, 0)
	if err != nil {
		t.Fatalf("PutDeviceProfile: %v", err)
	}
	if created.Revision != 1 {
		t.Fatalf("profile revision = %d, want 1", created.Revision)
	}

	device, err := st.GetDevice(ctx, "tablet")
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if device == nil {
		t.Fatal("GetDevice returned nil, want atomically created device")
	}
	if device.DisplayName != "tablet" {
		t.Fatalf("device display_name = %q, want device id", device.DisplayName)
	}
}

func TestDeviceAndMappingCAS(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	device, err := st.PutDevice(ctx, Device{DeviceID: "laptop", DisplayName: "Laptop"}, 0)
	if err != nil {
		t.Fatalf("PutDevice create: %v", err)
	}
	updated, err := st.PutDevice(ctx, Device{DeviceID: "laptop", DisplayName: "Laptop Pro"}, device.Revision)
	if err != nil {
		t.Fatalf("PutDevice update: %v", err)
	}
	if updated.Revision != 2 {
		t.Fatalf("updated device revision = %d, want 2", updated.Revision)
	}
	var deviceConflict *RevisionConflictError
	if _, err := st.PutDevice(ctx, Device{DeviceID: "laptop", DisplayName: "Laptop Legacy"}, device.Revision); !errors.As(err, &deviceConflict) {
		t.Fatalf("expected device revision conflict, got %v", err)
	}

	mapping, err := st.PutDeviceIPMapping(ctx, DeviceIPMapping{
		MappingID: "map-1",
		CIDR:      "10.0.0.0/24",
		DeviceID:  "laptop",
		Priority:  10,
		Enabled:   true,
	}, 0)
	if err != nil {
		t.Fatalf("PutDeviceIPMapping create: %v", err)
	}
	if mapping.Revision != 1 {
		t.Fatalf("mapping revision = %d, want 1", mapping.Revision)
	}
	updatedMapping, err := st.PutDeviceIPMapping(ctx, DeviceIPMapping{
		MappingID: "map-1",
		CIDR:      "10.0.0.0/24",
		DeviceID:  "laptop",
		Priority:  20,
		Enabled:   false,
	}, mapping.Revision)
	if err != nil {
		t.Fatalf("PutDeviceIPMapping update: %v", err)
	}
	if updatedMapping.Revision != 2 {
		t.Fatalf("updated mapping revision = %d, want 2", updatedMapping.Revision)
	}
	var mappingConflict *RevisionConflictError
	if _, err := st.PutDeviceIPMapping(ctx, DeviceIPMapping{
		MappingID: "map-1",
		CIDR:      "10.0.0.0/24",
		DeviceID:  "laptop",
		Priority:  30,
		Enabled:   true,
	}, mapping.Revision); !errors.As(err, &mappingConflict) {
		t.Fatalf("expected mapping revision conflict, got %v", err)
	}
}

func TestMappingRequiresExistingDevice(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	_, err = st.PutDeviceIPMapping(ctx, DeviceIPMapping{
		MappingID: "map-1",
		CIDR:      "10.0.1.0/24",
		DeviceID:  "missing-device",
		Priority:  1,
		Enabled:   true,
	}, 0)
	if !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("expected ErrDeviceNotFound, got %v", err)
	}
}

func TestLocalServerMigrationRollsBackOnFailure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "data.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE sessions (
			token TEXT PRIMARY KEY,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			credential_generation INTEGER NOT NULL DEFAULT 1
		);
		INSERT INTO schema_migrations (version, applied_at, description) VALUES (3, '2026-07-19T00:00:00Z', 'seed');
	`); err != nil {
		t.Fatalf("seed failure db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	st, err := Open(dbPath)
	if err == nil {
		st.Close()
		t.Fatal("Open unexpectedly succeeded for a database that should fail migration 4")
	}

	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen raw db: %v", err)
	}
	defer db.Close()

	version, err := CurrentVersion(db)
	if err != nil {
		t.Fatalf("CurrentVersion after failed migration: %v", err)
	}
	if version != 3 {
		t.Fatalf("schema version after failed migration = %d, want 3", version)
	}
}
