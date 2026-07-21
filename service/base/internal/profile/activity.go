package profile

import (
	"net/netip"
	"sync"
	"time"
)

type DeviceActivity struct {
	DeviceID   string
	Source     IdentitySource
	LastSeenIP netip.Addr
	LastSeenAt time.Time
}

type DeviceActivityTracker struct {
	mu       sync.RWMutex
	byDevice map[string]DeviceActivity
}

func NewDeviceActivityTracker() *DeviceActivityTracker {
	return &DeviceActivityTracker{
		byDevice: make(map[string]DeviceActivity),
	}
}

func (t *DeviceActivityTracker) Observe(resolution Resolution, peer netip.Addr, at time.Time) {
	if t == nil {
		return
	}
	deviceID := normalizeDeviceID(resolution.DeviceID)
	if deviceID == "" {
		return
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	t.mu.Lock()
	t.byDevice[deviceID] = DeviceActivity{
		DeviceID:   deviceID,
		Source:     resolution.Source,
		LastSeenIP: peer,
		LastSeenAt: at,
	}
	t.mu.Unlock()
}

func (t *DeviceActivityTracker) Snapshot() map[string]DeviceActivity {
	if t == nil {
		return map[string]DeviceActivity{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	snapshot := make(map[string]DeviceActivity, len(t.byDevice))
	for deviceID, activity := range t.byDevice {
		snapshot[deviceID] = activity
	}
	return snapshot
}
