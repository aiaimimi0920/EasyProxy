# Smart Routing Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `subagent-driven-development` to implement this plan task-by-task. Every production-code change follows red-green-refactor.

**Goal:** Turn the existing smart-routing feature branch into a consistently configurable, reload-safe, mode-compatible feature with durable automated regression coverage.

**Architecture:** Keep `dispatch.Server` as the HTTP/SOCKS entry and the existing pool outbound as the single proxy-selection engine. Make `routing.final_policy` authoritative, make stable mode prefer long-lived candidates with a healthy fallback, let box reloads coordinate dispatcher topology changes, and ensure every mode that enables routing builds the global pool outbound the dispatcher requires.

**Tech Stack:** Go 1.24, sing-box adapters, React/TypeScript, PowerShell/Python operator scripts, GitHub Actions.

---

### Task 1: Make final policy authoritative

**Files:**
- Modify: `service/base/internal/app/routing_controller.go`
- Test: `service/base/internal/app/routing_controller_test.go`

- [x] Add failing tests proving `final_policy: DIRECT` survives initial construction, default rules, and a provider-style rule replacement.
- [x] Run `go test ./internal/app -run 'TestBuildEngineFinalPolicy|TestApplyEffectiveRulesFinalPolicy' -count=1` and confirm the tests fail because `FINAL,PROXY` currently wins.
- [x] Add a single controller helper that replaces rules and then always calls `SetFinal` with the configured policy. Use it from initial construction, hot apply, and provider callbacks.
- [x] Run the targeted tests and `go test ./internal/routerule ./internal/app -count=1`.

Expected invariant:

```go
applyEngineConfig(engine, rules, finalPolicy)
// engine.Final() always equals NormalizePolicy(finalPolicy), even when rules
// contain a legacy FINAL entry.
```

### Task 2: Correct directive precedence and stable selection semantics

**Files:**
- Modify: `service/base/internal/dispatch/server.go`
- Modify: `service/base/internal/outbound/pool/pool.go`
- Test: `service/base/internal/dispatch/server_test.go`
- Test: `service/base/internal/outbound/pool/pool_test.go`

- [x] Add a failing CONNECT-level test proving `X-Proxy-*` headers override the CONNECT path token.
- [x] Reverse the CONNECT merge call to `pathOverlay.merge(parseHeaders(headers))`.
- [x] Add failing pool tests proving stable mode prefers long-lived candidates when `Filter.LongLived` is unset, falls back to healthy candidates when none are long-lived, and honors explicit `nolong`.
- [x] Implement a two-stage candidate collection for implicit stable mode: try a cloned directive with `LongLived=true`, then retry with the original directive only when the preferred set is empty.
- [x] Run `go test ./internal/dispatch ./internal/outbound/pool -count=1`.

Stable behavior:

```text
explicit long/nolong -> strict caller filter
stable with no long-lived filter -> prefer long-lived, fallback to healthy
session/auto -> unchanged
```

### Task 3: Coordinate dispatcher lifecycle with box reload

**Files:**
- Modify: `service/base/internal/boxmgr/manager.go`
- Modify: `service/base/internal/app/routing_controller.go`
- Modify: `service/base/internal/app/app.go`
- Test: `service/base/internal/boxmgr/manager_test.go`
- Test: `service/base/internal/app/routing_controller_test.go`

- [x] Add failing controller tests for disabled->enabled, enabled->disabled, listen change, unchanged-topology hot apply, and reload failure rollback.
- [x] Add optional reload lifecycle hooks to boxmgr listeners: prepare, completed, failed. Existing `ConfigUpdateListener` behavior remains compatible.
- [x] Before box teardown, let the routing controller stop its listener only when the desired enabled/listen/auth topology differs.
- [x] After successful box activation, call `OnConfigUpdate` and complete the routing reload so the dispatcher starts against the live pool.
- [x] On rollback, restore the controller's last applied routing snapshot.
- [x] Register `RoutingController` with `boxMgr.AddConfigListener` during app startup.
- [x] Run `go test ./internal/app ./internal/boxmgr ./internal/monitor -count=1`.

### Task 4: Hot-apply long-lived thresholds

**Files:**
- Modify: `service/base/internal/monitor/manager.go`
- Modify: `service/base/internal/boxmgr/manager.go`
- Modify: `service/base/internal/app/routing_controller.go`
- Modify: `service/base/internal/monitor/server.go`
- Test: `service/base/internal/monitor/manager_test.go`
- Test: `service/base/internal/monitor/server_test.go`

- [x] Add failing tests proving both uptime and success-rate thresholds update existing node entries without a box reload.
- [x] Add `monitor.Manager.SetLongLivedThresholds`, updating manager defaults and every registered entry under locks.
- [x] Expose the operation through boxmgr and call it from routing hot apply/reconfigure.
- [x] Remove both long-lived thresholds from `reloadNeeded`; retain reload for enabled/listen/session TTL.
- [x] Add routing-config API tests covering the complete `need_reload` matrix, including `MinSuccessRate`.
- [x] Run `go test ./internal/monitor ./internal/app ./internal/boxmgr -count=1`.

### Task 5: Support smart routing in pure multi-port mode

**Files:**
- Modify: `service/base/internal/builder/builder.go`
- Test: `service/base/internal/builder/builder_test.go`

- [x] Add failing topology tests proving routing-enabled `multi-port` options contain the global `proxy-pool` outbound but no plain pool inbound.
- [x] Build the global pool outbound when either a pool inbound is enabled or smart routing is enabled. Only set the sing-box final route for the existing pool/hybrid path.
- [x] Keep per-node multi-port inbounds/outbounds unchanged.
- [x] Run `go test ./internal/builder ./internal/boxmgr ./internal/outbound/pool -count=1`.

### Task 6: Complete root operator configuration

**Files:**
- Modify: `config.example.yaml`
- Modify: `deploy/service/base/config.template.yaml`
- Modify: `README.md`
- Modify: `service/base/config.example.yaml`
- Modify: `tests/test_script_smoke.py`

- [x] Add a failing render test proving `serviceBase.runtime.routing` survives root config rendering.
- [x] Add a disabled, fully annotated routing block to the root example and deployment template.
- [x] Document route A as the default supported Docker path, custom-listen port publication, WebUI persistence versus root re-rendering, and import-code config precedence.
- [x] Align stable wording with “prefer long-lived, fallback to healthy”. Remove the claim that port-bound directives are publicly wired unless a public config is added.
- [x] Run `python -m unittest discover -s tests -p 'test_*.py' -v`.

### Task 7: Expand durable CI and integration coverage

**Files:**
- Modify: `.github/workflows/validate.yml`
- Modify: `.github/workflows/publish-ghcr-images.yml`
- Modify: `.github/workflows/deploy-cloudflare.yml`
- Modify: `.github/workflows/deploy-aggregator.yml`
- Modify: `.github/workflows/publish-service-base-config.yml`
- Test: `service/base/internal/dispatch/server_test.go`

- [x] Replace partial service/base package lists with `go test ./...` in every release/deploy preflight.
- [x] Add service/base frontend install, lint, and build to the primary PR validation workflow.
- [x] Add committed dispatcher integration tests covering same-port HTTP/SOCKS sniffing, DIRECT/PROXY dial choice, HTTP keep-alive, and live pool replacement.
- [x] Run the complete local validation chain.

Validation commands:

```powershell
Set-Location service/base
go test -count=1 ./...

Set-Location frontend
npm run lint
npm run build

Set-Location ../../..
python -m unittest discover -s tests -p "test_*.py" -v
git diff --check
```

### Task 8: Update status and perform final review

**Files:**
- Modify: `docs/smart-routing.md`
- Modify: `docs/smart-routing-status.md`

- [x] Replace historical limitations that are fixed by this plan with the new runtime contract.
- [x] Keep remaining limitations explicit: literal-IP-only GEOIP, SOCKS CONNECT-only, and route-B Docker port publication requirements.
- [x] Record committed test coverage rather than relying on the deleted temporary E2E harness.
- [x] Run full verification, inspect `git diff --check`, and perform a final code review focused on regressions and missing tests.

### Final review follow-up (completed)

- [x] Bind automatic probes to generation, round ID, and probe revision; make candidate probes supersede and drain periodic coordinators.
- [x] Apply management target config/auth before listener activation and restore it with listener rollback.
- [x] Synchronize reusable monitor-server dependency injection and use handler-local snapshots outside dependency locks.
- [x] Detach retired shared-state registries from reused monitor entries at transaction/reset boundaries.
