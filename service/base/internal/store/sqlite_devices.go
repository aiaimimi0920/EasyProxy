package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

func (s *sqliteStore) ListDevices(ctx context.Context) ([]Device, error) {
	rows, err := s.conn().QueryContext(ctx,
		`SELECT device_id, display_name, revision, created_at, updated_at
		 FROM devices
		 ORDER BY device_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	defer rows.Close()
	return scanDevices(rows)
}

func (s *sqliteStore) GetDevice(ctx context.Context, deviceID string) (*Device, error) {
	row := s.conn().QueryRowContext(ctx,
		`SELECT device_id, display_name, revision, created_at, updated_at
		 FROM devices
		 WHERE device_id = ?`,
		normalizeDeviceID(deviceID),
	)
	return scanDevice(row)
}

func (s *sqliteStore) PutDevice(ctx context.Context, device Device, expectedRevision int64) (Device, error) {
	deviceID := normalizeDeviceID(device.DeviceID)
	if deviceID == "" {
		return Device{}, errors.New("device id is required")
	}
	displayName := normalizeDisplayName(deviceID, device.DisplayName)
	now := time.Now().UTC().Format(time.RFC3339)

	if expectedRevision == 0 {
		if _, err := s.conn().ExecContext(ctx,
			`INSERT INTO devices (device_id, display_name, revision, created_at, updated_at)
			 VALUES (?, ?, 1, ?, ?)`,
			deviceID, displayName, now, now,
		); err != nil {
			current, lookupErr := s.GetDevice(ctx, deviceID)
			if lookupErr == nil && current != nil {
				return Device{}, &RevisionConflictError{CurrentRevision: current.Revision}
			}
			return Device{}, fmt.Errorf("insert device %q: %w", deviceID, err)
		}
	} else {
		result, err := s.conn().ExecContext(ctx,
			`UPDATE devices
			    SET display_name = ?, revision = revision + 1, updated_at = ?
			  WHERE device_id = ? AND revision = ?`,
			displayName, now, deviceID, expectedRevision,
		)
		if err != nil {
			return Device{}, fmt.Errorf("update device %q: %w", deviceID, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return Device{}, fmt.Errorf("rows affected for device %q: %w", deviceID, err)
		}
		if affected == 0 {
			current, lookupErr := s.GetDevice(ctx, deviceID)
			if lookupErr != nil {
				return Device{}, lookupErr
			}
			currentRevision := int64(0)
			if current != nil {
				currentRevision = current.Revision
			}
			return Device{}, &RevisionConflictError{CurrentRevision: currentRevision}
		}
	}

	saved, err := s.GetDevice(ctx, deviceID)
	if err != nil {
		return Device{}, err
	}
	if saved == nil {
		return Device{}, errors.New("device disappeared after CAS write")
	}
	return *saved, nil
}
