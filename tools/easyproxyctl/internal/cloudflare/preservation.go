package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
)

type PreservationImage struct {
	Objects map[string]string
	Tables  map[string][]string
}

// CapturePreservation includes all non-internal tables, not just the canonical
// MiSub rows used by logical export. Only hashes leave this in-memory image.
func (d D1) CapturePreservation(ctx context.Context, database string) (PreservationImage, error) {
	image := PreservationImage{Objects: map[string]string{}, Tables: map[string][]string{}}
	objects, err := d.query(ctx, database, "SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name")
	if err != nil {
		return image, err
	}
	if len(objects) > 1000 {
		return image, errors.New("EASYPROXY_MIGRATION_SNAPSHOT_LIMIT")
	}
	total := 0
	for _, object := range objects {
		name, nameOK := object["name"].(string)
		kind, kindOK := object["type"].(string)
		if !nameOK || !kindOK || name == "" || len(name) > 256 {
			return image, errors.New("EASYPROXY_MIGRATION_SNAPSHOT_INVALID")
		}
		if (strings.HasPrefix(name, "sqlite_") && name != "sqlite_sequence") || name == "_cf_KV" {
			continue
		}
		encoded, err := json.Marshal(object)
		if err != nil {
			return image, err
		}
		image.Objects[kind+":"+name] = string(encoded)
		if kind != "table" {
			continue
		}
		rows, err := d.query(ctx, database, `SELECT * FROM "`+strings.ReplaceAll(name, `"`, `""`)+`"`)
		if err != nil {
			return image, err
		}
		values := make([]string, 0, len(rows))
		for _, row := range rows {
			body, err := json.Marshal(row)
			if err != nil {
				return image, err
			}
			total += len(body)
			if total > 64<<20 {
				return image, errors.New("EASYPROXY_MIGRATION_SNAPSHOT_LIMIT")
			}
			values = append(values, string(body))
		}
		sort.Strings(values)
		image.Tables[name] = values
	}
	return image, nil
}

func (image PreservationImage) Digest() string {
	body, _ := json.Marshal(image)
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

// VerifyExpansion allows only reviewed new empty tables/indexes and the two
// exact schema-version rows. Existing objects and every existing row survive.
func VerifyExpansion(before, after PreservationImage) error {
	rejected := errors.New("EASYPROXY_MIGRATION_DATA_NOT_PRESERVED")
	for name, object := range before.Objects {
		if after.Objects[name] != object {
			return rejected
		}
	}
	allowedObjects := map[string]bool{}
	for _, name := range []string{"schema_migrations", "subscriptions", "profiles", "settings", "cron_executions"} {
		allowedObjects["table:"+name] = true
	}
	for _, name := range []string{"subscriptions", "profiles", "settings", "cron_executions"} {
		allowedObjects["index:idx_"+name+"_updated_at"] = true
	}
	for name := range after.Objects {
		if _, exists := before.Objects[name]; !exists && !allowedObjects[name] {
			return rejected
		}
	}
	for name, rows := range before.Tables {
		if name != "schema_migrations" && !reflect.DeepEqual(rows, after.Tables[name]) {
			return rejected
		}
	}
	for name, rows := range after.Tables {
		if _, exists := before.Tables[name]; !exists && name != "schema_migrations" && len(rows) != 0 {
			return rejected
		}
	}
	oldRows := map[string]bool{}
	for _, row := range before.Tables["schema_migrations"] {
		oldRows[row] = true
	}
	for _, row := range after.Tables["schema_migrations"] {
		delete(oldRows, row)
	}
	if len(oldRows) != 0 {
		return rejected
	}
	return ValidateReviewedSchema(after, true)
}

func ValidateReviewedSchema(image PreservationImage, complete bool) error {
	seen := map[int]bool{}
	for _, raw := range image.Tables["schema_migrations"] {
		var row struct {
			ID   int    `json:"migration_id"`
			Name string `json:"name"`
		}
		if json.Unmarshal([]byte(raw), &row) != nil || row.ID < 1 || row.ID > 2 || seen[row.ID] ||
			row.Name != strings.TrimSuffix(reviewedMigrations[row.ID-1].name, ".sql") {
			return errors.New("EASYPROXY_MIGRATION_SCHEMA_REJECTED")
		}
		seen[row.ID] = true
	}
	if complete && len(seen) != 2 {
		return errors.New("EASYPROXY_MIGRATION_SCHEMA_INCOMPLETE")
	}
	if complete {
		for _, name := range []string{"subscriptions", "profiles", "settings", "cron_executions"} {
			if _, exists := image.Tables[name]; !exists {
				return errors.New("EASYPROXY_MIGRATION_SCHEMA_INCOMPLETE")
			}
		}
	}
	return nil
}
