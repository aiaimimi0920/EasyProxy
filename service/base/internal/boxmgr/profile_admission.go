package boxmgr

import (
	"easy_proxies/internal/outbound/profileselect"
)

// The wrapper follows the box, including failed candidates and rollback builds.
// It owns no registry snapshot; committed boxes continue seeing live hot edits.
type profileGatedBox struct {
	managedBox
	gate *profileselect.Admission
}

func (b *profileGatedBox) Close() error {
	b.gate.Close()
	return b.managedBox.Close()
}
func commitProfileAdmission(instance managedBox) {
	if box, ok := instance.(*profileGatedBox); ok {
		box.gate.Commit()
	}
}
func (m *Manager) commitCurrentProfileAdmission() {
	m.mu.RLock()
	instance := m.currentBox
	m.mu.RUnlock()
	commitProfileAdmission(instance)
}
