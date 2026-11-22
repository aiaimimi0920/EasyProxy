package pool

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"easy_proxies/internal/monitor"

	"github.com/sagernet/sing-box/log"
)

func TestDeadDestinationDoesNotChainBlacklistHealthyPool(t *testing.T) {
	ResetSharedStateStore()
	defer ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	specs := []struct {
		tag     string
		latency time.Duration
	}{
		{tag: "us-fixed-03", latency: 155 * time.Millisecond},
		{tag: "us-fixed-01", latency: 171 * time.Millisecond},
		{tag: "us-fixed-02", latency: 186 * time.Millisecond},
	}

	members := make([]*memberState, 0, len(specs))
	for _, spec := range specs {
		entry := mgr.Register(monitor.NodeInfo{Tag: spec.tag, Name: spec.tag})
		entry.RecordSuccessWithLatency(spec.latency)
		shared := acquireSharedState(spec.tag)
		shared.attachEntry(entry)
		members = append(members, &memberState{
			tag:      spec.tag,
			outbound: failingOutbound{},
			entry:    entry,
			shared:   shared,
		})
	}

	p := &poolOutbound{
		mode:    modeAuto,
		logger:  log.NewNOPFactory().Logger(),
		monitor: mgr,
		members: members,
		options: Options{
			Mode:              modeAuto,
			FailureThreshold:  3,
			BlacklistDuration: 24 * time.Hour,
		},
	}

	// Simulate DSM CloudSync repeatedly failing on dead port yamiyu.com:5244.
	var pickedTags []string
	for i := 0; i < 25; i++ {
		member, err := p.pickMember("tcp", nil, nil)
		if err != nil {
			t.Fatalf("pickMember() on iteration %d failed: %v", i, err)
		}
		pickedTags = append(pickedTags, member.tag)
		p.recordFailure(member, "tcp", io.EOF, "yamiyu.com:5244")
	}

	if len(pickedTags) < 2 || pickedTags[0] != "us-fixed-03" || pickedTags[1] != "us-fixed-01" {
		t.Fatalf("expected failure cooldown to rotate second attempt to next healthy member, got %v", pickedTags[:2])
	}

	now := time.Now()
	for _, m := range members {
		if m.shared.isTransportBlacklisted("tcp", now) {
			t.Fatalf("healthy member %s was falsely blacklisted by dead destination", m.tag)
		}
		snap := m.entry.Snapshot()
		if snap.Blacklisted || !snap.EffectiveAvailable {
			t.Fatalf("member %s lost availability from dead destination: %+v", m.tag, snap)
		}
		if m.shared.inFailureCooldown(now) {
			t.Fatalf("member %s remained in failure cooldown after destination correlation", m.tag)
		}
	}
}

func TestSingleDestinationPinnedNodeUsesProbeBeforeBlacklisting(t *testing.T) {
	t.Run("healthy-node-survives-dead-destination", func(t *testing.T) {
		ResetSharedStateStore()
		defer ResetSharedStateStore()

		mgr, err := monitor.NewManager(monitor.Config{})
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}
		defer mgr.Stop()

		entry := mgr.Register(monitor.NodeInfo{Tag: "claude-us-fixed"})
		entry.RecordSuccessWithLatency(150 * time.Millisecond)
		probeDone := make(chan struct{}, 4)
		entry.SetProbe(func(context.Context) (time.Duration, error) {
			select {
			case probeDone <- struct{}{}:
			default:
			}
			return 150 * time.Millisecond, nil
		})
		shared := acquireSharedState("claude-us-fixed")
		shared.attachEntry(entry)
		member := &memberState{
			tag:      "claude-us-fixed",
			outbound: failingOutbound{},
			entry:    entry,
			shared:   shared,
		}

		p := &poolOutbound{
			mode:    modeAuto,
			logger:  log.NewNOPFactory().Logger(),
			monitor: mgr,
			members: []*memberState{member},
			options: Options{
				FailureThreshold:  3,
				BlacklistDuration: 24 * time.Hour,
			},
		}

		for i := 0; i < 6; i++ {
			p.recordFailure(member, "tcp", io.EOF, "dead-origin.example:5244")
			if i == 2 {
				select {
				case <-probeDone:
				case <-time.After(2 * time.Second):
					t.Fatal("timed out waiting for single-destination verification probe")
				}
				// Allow probe result callback to finish forgiving streak.
				deadline := time.Now().Add(time.Second)
				for shared.probeVerifying.Load() && time.Now().Before(deadline) {
					time.Sleep(5 * time.Millisecond)
				}
			}
		}

		if shared.isTransportBlacklisted("tcp", time.Now()) {
			t.Fatal("healthy pinned node was blacklisted by single unreachable destination")
		}
	})

	t.Run("dead-node-is-blacklisted-after-probe-fails", func(t *testing.T) {
		ResetSharedStateStore()
		defer ResetSharedStateStore()

		mgr, err := monitor.NewManager(monitor.Config{})
		if err != nil {
			t.Fatalf("NewManager() error = %v", err)
		}
		defer mgr.Stop()

		entry := mgr.Register(monitor.NodeInfo{Tag: "dead-us-node"})
		entry.RecordSuccessWithLatency(150 * time.Millisecond)
		entry.SetProbe(func(context.Context) (time.Duration, error) {
			return 0, errors.New("upstream proxy unreachable")
		})
		shared := acquireSharedState("dead-us-node")
		shared.attachEntry(entry)
		member := &memberState{
			tag:      "dead-us-node",
			outbound: failingOutbound{},
			entry:    entry,
			shared:   shared,
		}

		p := &poolOutbound{
			mode:    modeAuto,
			logger:  log.NewNOPFactory().Logger(),
			monitor: mgr,
			members: []*memberState{member},
			options: Options{
				FailureThreshold:  3,
				BlacklistDuration: time.Hour,
			},
		}

		p.recordFailure(member, "tcp", io.EOF, "api.anthropic.com:443")
		p.recordFailure(member, "tcp", io.EOF, "api.anthropic.com:443")
		p.recordFailure(member, "tcp", io.EOF, "api.anthropic.com:443")

		deadline := time.Now().Add(2 * time.Second)
		for !shared.isTransportBlacklisted("tcp", time.Now()) && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if !shared.isTransportBlacklisted("tcp", time.Now()) {
			t.Fatal("genuinely dead node was not blacklisted after verification probe failed")
		}
	})
}

func TestMultiDestinationFailuresStillBlacklistBrokenNodeImmediately(t *testing.T) {
	ResetSharedStateStore()
	defer ResetSharedStateStore()

	mgr, err := monitor.NewManager(monitor.Config{})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer mgr.Stop()

	entry := mgr.Register(monitor.NodeInfo{Tag: "broken-node"})
	entry.SetProbe(func(context.Context) (time.Duration, error) {
		return 10 * time.Millisecond, nil
	})
	shared := acquireSharedState("broken-node")
	shared.attachEntry(entry)
	member := &memberState{
		tag:      "broken-node",
		outbound: failingOutbound{},
		entry:    entry,
		shared:   shared,
	}

	p := &poolOutbound{
		mode:    modeAuto,
		logger:  log.NewNOPFactory().Logger(),
		monitor: mgr,
		members: []*memberState{member},
		options: Options{
			FailureThreshold:  3,
			BlacklistDuration: time.Hour,
		},
	}

	p.recordFailure(member, "tcp", io.EOF, "one.example:443")
	p.recordFailure(member, "tcp", io.EOF, "two.example:443")
	p.recordFailure(member, "tcp", io.EOF, "three.example:443")

	if !shared.isTransportBlacklisted("tcp", time.Now()) {
		t.Fatal("broken node failing across multiple destinations should be blacklisted immediately")
	}
}
