package store

import (
	"database/sql"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func scanNode(row *sql.Row) (*Node, error) {
	var n Node
	var enabled int
	var createdAtStr, updatedAtStr string

	err := row.Scan(
		&n.ID, &n.URI, &n.Name, &n.Source, &n.Port,
		&n.Username, &n.Password, &n.Region, &n.Country,
		&enabled, &createdAtStr, &updatedAtStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	n.Enabled = enabled != 0
	n.CreatedAt = parseTime(createdAtStr)
	n.UpdatedAt = parseTime(updatedAtStr)
	return &n, nil
}

func scanNodes(rows *sql.Rows) ([]Node, error) {
	var nodes []Node
	for rows.Next() {
		var n Node
		var enabled int
		var createdAtStr, updatedAtStr string

		err := rows.Scan(
			&n.ID, &n.URI, &n.Name, &n.Source, &n.Port,
			&n.Username, &n.Password, &n.Region, &n.Country,
			&enabled, &createdAtStr, &updatedAtStr,
		)
		if err != nil {
			return nil, err
		}

		n.Enabled = enabled != 0
		n.CreatedAt = parseTime(createdAtStr)
		n.UpdatedAt = parseTime(updatedAtStr)
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

func scanDevice(row *sql.Row) (*Device, error) {
	var device Device
	var createdAtStr, updatedAtStr string
	err := row.Scan(
		&device.DeviceID,
		&device.DisplayName,
		&device.Revision,
		&createdAtStr,
		&updatedAtStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	device.CreatedAt = parseTime(createdAtStr)
	device.UpdatedAt = parseTime(updatedAtStr)
	return &device, nil
}

func scanDevices(rows *sql.Rows) ([]Device, error) {
	var devices []Device
	for rows.Next() {
		var device Device
		var createdAtStr, updatedAtStr string
		if err := rows.Scan(
			&device.DeviceID,
			&device.DisplayName,
			&device.Revision,
			&createdAtStr,
			&updatedAtStr,
		); err != nil {
			return nil, err
		}
		device.CreatedAt = parseTime(createdAtStr)
		device.UpdatedAt = parseTime(updatedAtStr)
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

func scanDeviceProfile(row *sql.Row) (*DeviceProfile, error) {
	var profile DeviceProfile
	var profileJSON string
	var createdAtStr, updatedAtStr string
	err := row.Scan(
		&profile.DeviceID,
		&profileJSON,
		&profile.SchemaVersion,
		&profile.Revision,
		&createdAtStr,
		&updatedAtStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	profile.ProfileJSON = []byte(profileJSON)
	profile.CreatedAt = parseTime(createdAtStr)
	profile.UpdatedAt = parseTime(updatedAtStr)
	return &profile, nil
}

func scanDeviceProfiles(rows *sql.Rows) ([]DeviceProfile, error) {
	var profiles []DeviceProfile
	for rows.Next() {
		var profile DeviceProfile
		var profileJSON string
		var createdAtStr, updatedAtStr string
		if err := rows.Scan(
			&profile.DeviceID,
			&profileJSON,
			&profile.SchemaVersion,
			&profile.Revision,
			&createdAtStr,
			&updatedAtStr,
		); err != nil {
			return nil, err
		}
		profile.ProfileJSON = []byte(profileJSON)
		profile.CreatedAt = parseTime(createdAtStr)
		profile.UpdatedAt = parseTime(updatedAtStr)
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func scanDeviceIPMapping(row *sql.Row) (*DeviceIPMapping, error) {
	var mapping DeviceIPMapping
	var enabled int
	var createdAtStr, updatedAtStr string
	err := row.Scan(
		&mapping.MappingID,
		&mapping.CIDR,
		&mapping.DeviceID,
		&mapping.Priority,
		&enabled,
		&mapping.Revision,
		&createdAtStr,
		&updatedAtStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	mapping.Enabled = enabled != 0
	mapping.CreatedAt = parseTime(createdAtStr)
	mapping.UpdatedAt = parseTime(updatedAtStr)
	return &mapping, nil
}

func scanDeviceIPMappings(rows *sql.Rows) ([]DeviceIPMapping, error) {
	var mappings []DeviceIPMapping
	for rows.Next() {
		var mapping DeviceIPMapping
		var enabled int
		var createdAtStr, updatedAtStr string
		if err := rows.Scan(
			&mapping.MappingID,
			&mapping.CIDR,
			&mapping.DeviceID,
			&mapping.Priority,
			&enabled,
			&mapping.Revision,
			&createdAtStr,
			&updatedAtStr,
		); err != nil {
			return nil, err
		}
		mapping.Enabled = enabled != 0
		mapping.CreatedAt = parseTime(createdAtStr)
		mapping.UpdatedAt = parseTime(updatedAtStr)
		mappings = append(mappings, mapping)
	}
	return mappings, rows.Err()
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// Try other formats
		t, err = time.Parse("2006-01-02 15:04:05", s)
		if err != nil {
			return time.Time{}
		}
	}
	return t
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func normalizeDeviceID(deviceID string) string {
	return strings.ToLower(strings.TrimSpace(deviceID))
}

func normalizeDisplayName(deviceID, displayName string) string {
	normalized := strings.TrimSpace(displayName)
	if normalized == "" {
		return deviceID
	}
	return normalized
}

func normalizeMappingID(mappingID string) string {
	return strings.TrimSpace(mappingID)
}
