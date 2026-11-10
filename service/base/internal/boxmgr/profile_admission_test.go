package boxmgr

import (
	"context"
	"easy_proxies/internal/config"
	"easy_proxies/internal/outbound/profileselect"
	"easy_proxies/internal/profile"
	"errors"
	"net/netip"
	"testing"
	"time"
)

type admissionResolver struct{}

func (admissionResolver) Resolve(profile.RequestIdentity) profile.Resolution {
	return profile.Resolution{}
}
func (admissionResolver) Observe(profile.Resolution, netip.Addr, time.Time) {}

type admissionConfigListener func(*config.Config)

func (f admissionConfigListener) OnConfigUpdate(cfg *config.Config) { f(cfg) }

func TestProfileAdmissionWaitsForConfigPublicationAndRestoresRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			disabled := false
			cfg := &config.Config{Mode: "pool", Management: config.ManagementConfig{Enabled: &disabled}, Nodes: []config.NodeConfig{{Name: "node", URI: "http://127.0.0.1:1"}}}
			cfg.LocalServer.Enabled = true
			cfg.Gateway.Enabled = true
			cfg.Gateway.Mode = "tun"
			cfg.Routing.FinalPolicy = "DIRECT"
			next := cfg.Clone()
			next.Routing.FinalPolicy = "PROXY"
			oldGate := new(profileselect.Admission)
			oldGate.Commit()
			manager := &Manager{baseCtx: context.Background(), cfg: cfg, lastAppliedCfg: snapshotConfig(cfg), logger: defaultLogger{}, profiles: admissionResolver{}, currentBox: &profileGatedBox{managedBox: &fakeManagedBox{name: "old"}, gate: oldGate}}
			var gates []*profileselect.Admission
			manager.boxFactory = func(ctx context.Context, _ *config.Config) (managedBox, error) {
				gate := profileselect.AdmissionFrom(ctx)
				if gate == nil || gate.Check() == nil {
					t.Fatal("candidate admitted before commit")
				}
				gates = append(gates, gate)
				return &fakeManagedBox{name: "candidate"}, nil
			}
			expected := next.Routing.FinalPolicy
			if fail {
				expected = cfg.Routing.FinalPolicy
				manager.AddReloadLifecycleListener(&recordingReloadListener{events: &[]string{}, completeErr: errors.New("reject candidate")})
			}
			published := false
			manager.AddConfigListener(admissionConfigListener(func(got *config.Config) {
				if got.Routing.FinalPolicy != expected {
					t.Fatalf("published wrong policy: %s", got.Routing.FinalPolicy)
				}
				box := manager.currentBox.(*profileGatedBox)
				if box.gate.Check() == nil {
					t.Fatal("box admitted before profile publication callback completed")
				}
				published = true
			}))
			err := manager.Reload(next)
			if (err != nil) != fail {
				t.Fatalf("reload error=%v fail=%v", err, fail)
			}
			if !published {
				t.Fatal("missing config publication")
			}
			if oldGate.Check() == nil {
				t.Fatal("closed old generation still admitted")
			}
			if manager.currentBox.(*profileGatedBox).gate.Check() != nil {
				t.Fatal("committed or restored generation unavailable")
			}
			if fail && (len(gates) != 2 || gates[0].Check() == nil) {
				t.Fatal("rejected candidate gate reopened")
			}
		})
	}
}
