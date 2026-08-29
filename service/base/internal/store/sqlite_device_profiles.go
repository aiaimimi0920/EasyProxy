package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

func (s *sqliteStore) ListDeviceProfiles(ctx context.Context) ([]DeviceProfile, error) {
	rows, err := s.conn().QueryContext(ctx,
		`SELECT device_id, profile_json, schema_version, revision, created_at, updated_at
		 FROM device_profiles
		 ORDER BY device_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list device profiles: %w", err)
	}
	defer rows.Close()
	return scanDeviceProfiles(rows)
}

func (s *sqliteStore) GetDeviceProfile(ctx context.Context, deviceID string) (*DeviceProfile, error) {
	row := s.conn().QueryRowContext(ctx,
		`SELECT device_id, profile_json, schema_version, revision, created_at, updated_at
		 FROM device_profiles
		 WHERE device_id = ?`,
		normalizeDeviceID(deviceID),
	)
	return scanDeviceProfile(row)
}

func (s *sqliteStore) PutDeviceProfile(ctx context.Context, profile DeviceProfile, expectedRevision int64) (DeviceProfile, error) {
	deviceID := normalizeDeviceID(profile.DeviceID)
	if deviceID == "" {
		return DeviceProfile{}, errors.New("device id is required")
	}

	var saved DeviceProfile
	err := s.WithTx(ctx, func(tx Store) error {
		txStore, ok := tx.(*sqliteStore)
		if !ok {
			return errors.New("unexpected transaction store implementation")
		}

		device, err := txStore.GetDevice(ctx, deviceID)
		if err != nil {
			return err
		}
		if device == nil {
			if _, err := txStore.PutDevice(ctx, Device{DeviceID: deviceID, DisplayName: deviceID}, 0); err != nil {
				return err
			}
		}

		now := time.Now().UTC().Format(time.RFC3339)
		if expectedRevision == 0 {
			if _, err := txStore.conn().ExecContext(ctx,
				`INSERT INTO device_profiles (device_id, profile_json, schema_version, revision, created_at, updated_at)
				 VALUES (?, ?, ?, 1, ?, ?)`,
				deviceID, string(profile.ProfileJSON), profile.SchemaVersion, now, now,
			); err != nil {
				current, lookupErr := txStore.GetDeviceProfile(ctx, deviceID)
				if lookupErr == nil && current != nil {
					return &RevisionConflictError{CurrentRevision: current.Revision}
				}
				return fmt.Errorf("insert device profile %q: %w", deviceID, err)
			}
		} else {
			result, err := txStore.conn().ExecContext(ctx,
				`UPDATE device_profiles
				    SET profile_json = ?, schema_version = ?, revision = revision + 1, updated_at = ?
				  WHERE device_id = ? AND revision = ?`,
				string(profile.ProfileJSON), profile.SchemaVersion, now, deviceID, expectedRevision,
			)
			if err != nil {
				return fmt.Errorf("update device profile %q: %w", deviceID, err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("rows affected for device profile %q: %w", deviceID, err)
			}
			if affected == 0 {
				current, lookupErr := txStore.GetDeviceProfile(ctx, deviceID)
				if lookupErr != nil {
					return lookupErr
				}
				currentRevision := int64(0)
				if current != nil {
					currentRevision = current.Revision
				}
				return &RevisionConflictError{CurrentRevision: currentRevision}
			}
		}

		current, err := txStore.GetDeviceProfile(ctx, deviceID)
		if err != nil {
			return err
		}
		if current == nil {
			return errors.New("device profile disappeared after CAS write")
		}
		saved = *current
		return nil
	})
	if err != nil {
		return DeviceProfile{}, err
	}
	return saved, nil
}

func (s *sqliteStore) DeleteDeviceProfile(ctx context.Context, deviceID string, expectedRevision int64) (bool, error) {
	deviceID = normalizeDeviceID(deviceID)
	current, err := s.GetDeviceProfile(ctx, deviceID)
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
		`DELETE FROM device_profiles WHERE device_id = ? AND revision = ?`,
		deviceID, expectedRevision,
	)
	if err != nil {
		return false, fmt.Errorf("delete device profile %q: %w", deviceID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected for deleting device profile %q: %w", deviceID, err)
	}
	if affected == 0 {
		current, lookupErr := s.GetDeviceProfile(ctx, deviceID)
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
