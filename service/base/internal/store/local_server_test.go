package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionCredentialGenerationRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	want := &Session{
		Token:                "token",
		CreatedAt:            time.Now().UTC(),
		ExpiresAt:            time.Now().UTC().Add(time.Hour),
		CredentialGeneration: 7,
	}
	if err := st.CreateSession(ctx, want); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetSession(ctx, "token")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got == nil {
		t.Fatal("GetSession returned nil, want persisted session")
	}
	if got.CredentialGeneration != 7 {
		t.Fatalf("session credential_generation = %d, want 7", got.CredentialGeneration)
	}
}

func TestDeleteSessionsBeforeGeneration(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	sessions := []*Session{
		{
			Token:                "old",
			CreatedAt:            time.Now().UTC(),
			ExpiresAt:            time.Now().UTC().Add(time.Hour),
			CredentialGeneration: 1,
		},
		{
			Token:                "current",
			CreatedAt:            time.Now().UTC(),
			ExpiresAt:            time.Now().UTC().Add(time.Hour),
			CredentialGeneration: 3,
		},
	}
	for _, session := range sessions {
		if err := st.CreateSession(ctx, session); err != nil {
			t.Fatalf("CreateSession(%q): %v", session.Token, err)
		}
	}

	if err := st.DeleteSessionsBeforeGeneration(ctx, 3); err != nil {
		t.Fatalf("DeleteSessionsBeforeGeneration: %v", err)
	}

	oldSession, err := st.GetSession(ctx, "old")
	if err != nil {
		t.Fatalf("GetSession(old): %v", err)
	}
	if oldSession != nil {
		t.Fatalf("old session still exists: %#v", oldSession)
	}

	currentSession, err := st.GetSession(ctx, "current")
	if err != nil {
		t.Fatalf("GetSession(current): %v", err)
	}
	if currentSession == nil || currentSession.CredentialGeneration != 3 {
		t.Fatalf("current session = %#v, want preserved generation 3", currentSession)
	}
}

func TestDeleteDeviceIPMappingIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if _, err := st.PutDevice(ctx, Device{DeviceID: "desktop", DisplayName: "Desktop"}, 0); err != nil {
		t.Fatalf("PutDevice: %v", err)
	}
	mapping, err := st.PutDeviceIPMapping(ctx, DeviceIPMapping{
		MappingID: "map-1",
		CIDR:      "192.168.1.10/32",
		DeviceID:  "desktop",
		Priority:  1,
		Enabled:   true,
	}, 0)
	if err != nil {
		t.Fatalf("PutDeviceIPMapping: %v", err)
	}

	deleted, err := st.DeleteDeviceIPMapping(ctx, mapping.MappingID, mapping.Revision)
	if err != nil || !deleted {
		t.Fatalf("DeleteDeviceIPMapping first delete = %v, err=%v", deleted, err)
	}
	deleted, err = st.DeleteDeviceIPMapping(ctx, mapping.MappingID, mapping.Revision)
	if err != nil || deleted {
		t.Fatalf("DeleteDeviceIPMapping second delete = %v, err=%v", deleted, err)
	}
}

func TestDeviceAndProfileDeleteConflictReporting(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	profile, err := st.PutDeviceProfile(ctx, DeviceProfile{
		DeviceID:      "phone",
		ProfileJSON:   []byte(`{"schema_version":1,"enabled":true}`),
		SchemaVersion: 1,
	}, 0)
	if err != nil {
		t.Fatalf("PutDeviceProfile: %v", err)
	}

	_, err = st.PutDeviceProfile(ctx, DeviceProfile{
		DeviceID:      "phone",
		ProfileJSON:   []byte(`{"schema_version":1,"enabled":false}`),
		SchemaVersion: 1,
	}, profile.Revision+1)
	var conflict *RevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected RevisionConflictError, got %v", err)
	}
}
