package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func (s *sqliteStore) ListDeviceIPMappings(ctx context.Context) ([]DeviceIPMapping, error) {
	rows, err := s.conn().QueryContext(ctx,
		`SELECT mapping_id, cidr, device_id, priority, enabled, revision, created_at, updated_at
		 FROM device_ip_mappings
		 ORDER BY mapping_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list device ip mappings: %w", err)
	}
	defer rows.Close()
	return scanDeviceIPMappings(rows)
}

func (s *sqliteStore) GetDeviceIPMapping(ctx context.Context, mappingID string) (*DeviceIPMapping, error) {
	row := s.conn().QueryRowContext(ctx,
		`SELECT mapping_id, cidr, device_id, priority, enabled, revision, created_at, updated_at
		 FROM device_ip_mappings
		 WHERE mapping_id = ?`,
		normalizeMappingID(mappingID),
	)
	return scanDeviceIPMapping(row)
}

func (s *sqliteStore) PutDeviceIPMapping(ctx context.Context, mapping DeviceIPMapping, expectedRevision int64) (DeviceIPMapping, error) {
	mappingID := normalizeMappingID(mapping.MappingID)
	if mappingID == "" {
		return DeviceIPMapping{}, errors.New("mapping id is required")
	}
	cidr := strings.TrimSpace(mapping.CIDR)
	if cidr == "" {
		return DeviceIPMapping{}, errors.New("cidr is required")
	}
	deviceID := normalizeDeviceID(mapping.DeviceID)
	if deviceID == "" {
		return DeviceIPMapping{}, errors.New("device id is required")
	}

	device, err := s.GetDevice(ctx, deviceID)
	if err != nil {
		return DeviceIPMapping{}, err
	}
	if device == nil {
		return DeviceIPMapping{}, ErrDeviceNotFound
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if expectedRevision == 0 {
		if _, err := s.conn().ExecContext(ctx,
			`INSERT INTO device_ip_mappings (mapping_id, cidr, device_id, priority, enabled, revision, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, 1, ?, ?)`,
			mappingID, cidr, deviceID, mapping.Priority, boolToInt(mapping.Enabled), now, now,
		); err != nil {
			current, lookupErr := s.GetDeviceIPMapping(ctx, mappingID)
			if lookupErr == nil && current != nil {
				return DeviceIPMapping{}, &RevisionConflictError{CurrentRevision: current.Revision}
			}
			return DeviceIPMapping{}, fmt.Errorf("insert device ip mapping %q: %w", mappingID, err)
		}
	} else {
		result, err := s.conn().ExecContext(ctx,
			`UPDATE device_ip_mappings
			    SET cidr = ?, device_id = ?, priority = ?, enabled = ?, revision = revision + 1, updated_at = ?
			  WHERE mapping_id = ? AND revision = ?`,
			cidr, deviceID, mapping.Priority, boolToInt(mapping.Enabled), now, mappingID, expectedRevision,
		)
		if err != nil {
			return DeviceIPMapping{}, fmt.Errorf("update device ip mapping %q: %w", mappingID, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return DeviceIPMapping{}, fmt.Errorf("rows affected for device ip mapping %q: %w", mappingID, err)
		}
		if affected == 0 {
			current, lookupErr := s.GetDeviceIPMapping(ctx, mappingID)
			if lookupErr != nil {
				return DeviceIPMapping{}, lookupErr
			}
			currentRevision := int64(0)
			if current != nil {
				currentRevision = current.Revision
			}
			return DeviceIPMapping{}, &RevisionConflictError{CurrentRevision: currentRevision}
		}
	}

	saved, err := s.GetDeviceIPMapping(ctx, mappingID)
	if err != nil {
		return DeviceIPMapping{}, err
	}
	if saved == nil {
		return DeviceIPMapping{}, errors.New("device ip mapping disappeared after CAS write")
	}
	return *saved, nil
}

func (s *sqliteStore) DeleteDeviceIPMapping(ctx context.Context, mappingID string, expectedRevision int64) (bool, error) {
	mappingID = normalizeMappingID(mappingID)
	current, err := s.GetDeviceIPMapping(ctx, mappingID)
	if err != nil {
		return false, err
	}
	if current == nil {
		return false, nil
	}
	if current.Revision != expectedRevision {
		return false, &RevisionConflictError{CurrentRevision: current.Revision}
	}

	result, err := s.conn().ExecContext(ctx,
		`DELETE FROM device_ip_mappings WHERE mapping_id = ? AND revision = ?`,
		mappingID, expectedRevision,
	)
	if err != nil {
		return false, fmt.Errorf("delete device ip mapping %q: %w", mappingID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected for deleting device ip mapping %q: %w", mappingID, err)
	}
	if affected == 0 {
		current, lookupErr := s.GetDeviceIPMapping(ctx, mappingID)
		if lookupErr != nil {
			return false, lookupErr
		}
		if current == nil {
			return false, nil
		}
		return false, &RevisionConflictError{CurrentRevision: current.Revision}
	}
	return true, nil
}

// ===================== Subscription status =====================
