package gateway

import (
	"context"

	"easy_proxies/internal/boxmgr"
)

var _ boxmgr.ReloadLifecycleListener = (*Manager)(nil)

// PrepareReload removes capture before the candidate box changes. This keeps
// a short reload window fail-open instead of redirecting traffic into a dead
// listener.
func (m *Manager) PrepareReload(_ context.Context, _, _ boxmgr.ReloadState) error {
	return m.Stop()
}

func (m *Manager) CompleteReload(ctx context.Context, _, to boxmgr.ReloadState) error {
	if to.Config == nil {
		return nil
	}
	return m.Start(ctx, to.Config.Gateway)
}

func (m *Manager) FailedReload(ctx context.Context, from, _ boxmgr.ReloadState, _ error, restored bool) error {
	if !restored || from.Config == nil {
		return nil
	}
	return m.Start(ctx, from.Config.Gateway)
}
