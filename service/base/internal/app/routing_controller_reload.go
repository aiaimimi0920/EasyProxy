package app

import (
	"context"
	"fmt"
	"log"
	"strings"

	"easy_proxies/internal/boxmgr"
	"easy_proxies/internal/config"
)

func (rc *RoutingController) BeginReloadIntent(_ context.Context) error {
	rc.operationMu.Lock()
	rc.mu.Lock()
	if rc.stopped {
		rc.mu.Unlock()
		rc.operationMu.Unlock()
		return errRoutingControllerStopped
	}
	rc.reloadIntents++
	rc.mu.Unlock()
	rc.operationMu.Unlock()
	return nil
}

// EndReloadIntent releases one reload-intent guard.
func (rc *RoutingController) EndReloadIntent(_ context.Context) {
	rc.operationMu.Lock()
	rc.mu.Lock()
	if rc.reloadIntents > 0 {
		rc.reloadIntents--
	}
	rc.mu.Unlock()
	rc.operationMu.Unlock()
}

// PrepareReload stops the dispatcher before the old box releases ports when
// the effective routing topology changes. Source-only and rule-only reloads
// leave the listener running.
func (rc *RoutingController) PrepareReload(_ context.Context, from, to boxmgr.ReloadState) error {
	from = cloneRoutingState(from)
	to = cloneRoutingState(to)
	if rc.profiles != nil && to.Config != nil && to.Config.LocalServer.Enabled {
		if err := rc.profiles.PrepareConfig(to.Config); err != nil {
			return err
		}
	}
	rc.operationMu.Lock()
	defer rc.operationMu.Unlock()

	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.stopped {
		rc.pendingFrom = boxmgr.ReloadState{}
		rc.hasPending = false
		rc.pendingRuntimeMutated = false
		return nil
	}
	if rc.hasLastApplied {
		from = cloneRoutingState(rc.lastApplied)
	}
	rc.pendingFrom = cloneRoutingState(from)
	rc.hasPending = true
	rc.pendingRuntimeMutated = false
	if routingTopologyFor(from) != routingTopologyFor(to) {
		rc.pendingRuntimeMutated = true
		rc.stopRuntimeLocked()
	}
	return nil
}

// CompleteReload applies the candidate state after boxmgr has published the
// new box, so proxy requests always resolve the live candidate PoolOutbound.
func (rc *RoutingController) CompleteReload(_ context.Context, from, to boxmgr.ReloadState) error {
	from = cloneRoutingState(from)
	to = cloneRoutingState(to)
	rc.operationMu.Lock()
	defer rc.operationMu.Unlock()

	rc.mu.Lock()
	if rc.stopped {
		rc.pendingFrom = boxmgr.ReloadState{}
		rc.hasPending = false
		rc.pendingRuntimeMutated = false
		rc.mu.Unlock()
		return nil
	}
	if rc.hasPending {
		from = cloneRoutingState(rc.pendingFrom)
	} else if rc.hasLastApplied {
		from = cloneRoutingState(rc.lastApplied)
	}

	topologyChanged := routingTopologyFor(from) != routingTopologyFor(to)
	if !routingEnabled(to) {
		if rc.running || rc.server != nil || rc.engine != nil || rc.provider != nil {
			rc.pendingRuntimeMutated = true
			rc.stopRuntimeLocked()
		}
		wantsGeo := to.Config != nil && to.Config.GeoIP.Enabled && strings.TrimSpace(to.Config.GeoIP.DatabasePath) != ""
		if routingGeoChanged(from.Config, to.Config) || wantsGeo != (rc.geo != nil) {
			rc.pendingRuntimeMutated = true
			rc.closeGeoLocked()
			rc.openGeoLocked(to.Config)
		}
	} else if topologyChanged || !rc.running || rc.server == nil || (!to.Config.LocalServer.Enabled && rc.engine == nil) {
		rc.pendingRuntimeMutated = true
		rc.stopRuntimeLocked()
		if err := rc.startLocked(to.Config); err != nil {
			rc.mu.Unlock()
			return err
		}
	} else if to.Config.LocalServer.Enabled && routingGeoChanged(from.Config, to.Config) {
		rc.pendingRuntimeMutated = true
		rc.closeGeoLocked()
		rc.openGeoLocked(to.Config)
	} else if !to.Config.LocalServer.Enabled && routingRuntimeChanged(from.Config, to.Config) {
		rc.pendingRuntimeMutated = true
		if err := rc.applyRuntimeConfigLocked(from.Config, to.Config); err != nil {
			rc.mu.Unlock()
			return err
		}
	}

	rc.setAppliedStateLocked(to)
	rc.mu.Unlock()
	rc.applyThresholds(to.Config)
	return nil
}

// FailedReload restores the last-good dispatcher after boxmgr has restored the
// old box. When box restoration failed, routing remains disabled so it cannot
// expose an entry backed by a missing or rejected pool.
func (rc *RoutingController) FailedReload(_ context.Context, from, _ boxmgr.ReloadState, cause error, restored bool) error {
	from = cloneRoutingState(from)
	rc.operationMu.Lock()
	defer rc.operationMu.Unlock()

	rc.mu.Lock()
	if rc.stopped {
		rc.pendingFrom = boxmgr.ReloadState{}
		rc.hasPending = false
		rc.pendingRuntimeMutated = false
		rc.mu.Unlock()
		return nil
	}
	if rc.hasPending {
		from = cloneRoutingState(rc.pendingFrom)
	}
	if restored && rc.hasPending && !rc.pendingRuntimeMutated {
		rc.setAppliedStateLocked(from)
		rc.hasPending = false
		rc.pendingRuntimeMutated = false
		rc.mu.Unlock()
		rc.applyThresholds(from.Config)
		return nil
	}
	rc.stopRuntimeLocked()
	if !restored {
		if from.Config != nil && from.Config.LocalServer.Enabled && routingEnabled(from) {
			if err := rc.startLocked(from.Config); err != nil {
				rc.setAppliedStateLocked(from)
				rc.hasPending = false
				rc.pendingRuntimeMutated = false
				rc.mu.Unlock()
				return fmt.Errorf("restore Local Server dispatch after failed box rollback: %w", err)
			}
		} else if !routingEnabled(from) {
			rc.openGeoLocked(from.Config)
		}
		rc.setAppliedStateLocked(from)
		rc.hasPending = false
		rc.pendingRuntimeMutated = false
		rc.mu.Unlock()
		if from.Config == nil || !from.Config.LocalServer.Enabled {
			log.Printf("⚠️ routing remains disabled after failed box reload: %v", cause)
		}
		return nil
	}

	if routingEnabled(from) {
		if err := rc.startLocked(from.Config); err != nil {
			rc.stopRuntimeLocked()
			rc.setAppliedStateLocked(from)
			rc.hasPending = false
			rc.pendingRuntimeMutated = false
			rc.mu.Unlock()
			rc.applyThresholds(from.Config)
			log.Printf("⚠️ failed to restore smart routing after box rollback: %v", err)
			return fmt.Errorf("restore smart routing after box rollback: %w", err)
		}
	} else {
		rc.openGeoLocked(from.Config)
	}
	rc.setAppliedStateLocked(from)
	rc.hasPending = false
	rc.pendingRuntimeMutated = false
	rc.mu.Unlock()
	rc.applyThresholds(from.Config)
	return nil
}

// OnConfigUpdate reattaches the routing API when boxmgr replaces the monitor
// server because its enablement or listen address changed during reload.
func (rc *RoutingController) OnConfigUpdate(_ *config.Config) {
	rc.operationMu.Lock()
	rc.mu.Lock()
	stopped := rc.stopped
	rc.pendingFrom = boxmgr.ReloadState{}
	rc.hasPending = false
	rc.pendingRuntimeMutated = false
	rc.mu.Unlock()
	rc.operationMu.Unlock()

	if stopped || rc.managerRef == nil {
		return
	}
	if server := rc.managerRef.MonitorServer(); server != nil {
		server.SetRoutingController(rc)
	}
}
