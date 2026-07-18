# Smart Routing — Development Status & Handoff

> **Purpose of this doc**: a single-file snapshot of the smart-routing feature —
> what was planned, what is implemented, what is verified, what remains, and how
> to build/test/run it. Written so another AI (or engineer) can pick up the work
> without re-reading the whole conversation history.
>
> **Companion doc**: [`smart-routing.md`](./smart-routing.md) is the *design*
> reference (concepts, request-param grammar, config schema). This doc is the
> *status / handoff*. When they disagree, trust the code.
>
> **Last updated**: 2026-07-19
> **Branch**: `feat/smart-routing-dispatch`
> **Baseline commits**: `0903b1e` (backend) + `4c6f241` (UI + hot config API)
> **Current hardening**: implemented and validated in this branch
> **Tag**: `release-20260701-smart-routing-001`
> **PR**: https://github.com/aiaimimi0920/EasyProxy/pull/1

---

## 1. Goal

Turn EasyProxy from a plain proxy-pool front-end into a **smart proxy entry**
with three orthogonal, composable layers:

1. **Traffic splitting** — Clash/mihomo-style rules decide `DIRECT` vs `PROXY`
   per destination (built-in China-direct default set + custom rules + remote
   rule providers).
2. **Node selection strategy** — how the pool picks a member:
   - `stable` — pin all traffic in a filter-bucket to one node. New bindings
     prefer healthy long-lived nodes, fall back to healthy nodes when needed,
     and auto-promote on failure without drifting an existing healthy binding.
   - `session` — pin a session (by key, or client-IP fallback) to one node for
     a TTL (crawler stickiness).
   - `auto` — the pool's original health-based selection (unchanged).
3. **Attribute filtering** — narrow candidates by country / region / long-lived.

Two entry styles, both on **one port** (HTTP + SOCKS5 via first-byte sniffing):
- **Default entry** (system proxy, no params) → splitting + default strategy.
- **Parameterized** — path prefix (`stable+us/host:443`), `X-Proxy-*` headers,
  or a SOCKS5 username token. Port-bound directives are internal-only today.

**Backward-compat guarantee**: everything is gated behind `routing.enabled`
(default `false`). When disabled, the runtime behaves *exactly* as before — the
plain pool inbound serves all traffic. The pool's `selectMember` falls back
byte-for-byte to the original path when no directive is present.

---

## 2. Architecture (3 layers, all under `service/base/internal/`)

```
client ─▶ dispatch.Server (HTTP/SOCKS5 entry, one port)
            │  1. parse params → SelectionDirective (params.go / socks.go)
            │  2. routerule.Engine.Match(host) → DIRECT | PROXY
            ├─ DIRECT ─▶ net.Dialer (direct)
            └─ PROXY  ─▶ pool outbound.DialContext(ctx+directive)
                           │  selectMemberWithDirective:
                           │   directive==nil → original selectMember (unchanged)
                           │   stable/session → sticky.go bucket/session pin
                           │   + NodeFilter candidate filtering
                           └─ same health/blacklist/stats engine as before
```

- The dispatcher holds the **live pool outbound handle** via
  `boxMgr.PoolOutbound()` (re-fetched per request so it survives box reloads).
- Directive is injected into the dial `context` (`pool.WithDirective`); the pool
  reads it with `pool.DirectiveFrom`. sing-box's static route cannot express
  this — that's *why* the dispatcher must sit at the entry, not behind the
  sing-box inbound.
- boxmgr coordinates dispatcher, monitor listener, shared pool state, candidate
  health, and sing-box publication as one reload transaction. Failed candidates
  restore the last applied configuration; stale probe generations, round IDs,
  and probe revisions cannot write into the restored runtime.
- Management listener transitions publish the target runtime config (including
  management credentials) before accepting connections on a replacement
  address. Rollback restores both the old listener and its config snapshot.
- Shared pool state transactions detach retired monitor handles at registry
  swap time, so late old-box connections can finish their own counters without
  mutating a candidate monitor entry.

### Port takeover (route A)

When `routing.enabled` and the dispatch listen addr == the pool inbound addr,
the **builder omits the plain pool inbound** (`config.RoutingTakesOverPoolInbound()`),
so the dispatcher can bind that port. The pool **outbound** is still built and
dialed directly. sing-box tolerates zero inbounds. If `routing.listen` points at
a *different* port (route B), both entries coexist. Route B must also publish
that custom port in Docker/Compose; YAML cannot expose an unpublished port.

---

## 3. File inventory

### New packages / files
| File | Role |
|---|---|
| `internal/routerule/engine.go` | Rule engine: DOMAIN-SUFFIX/KEYWORD/DOMAIN/IP-CIDR/GEOIP/FINAL matching; `SetRules`/`SetFinal` hot-swap |
| `internal/routerule/defaults.go` | Built-in China-direct default rule set |
| `internal/routerule/provider.go` | Remote rule-provider fetch/refresh (fail-soft, interval) |
| `internal/routerule/engine_test.go` | Engine match tests |
| `internal/dispatch/server.go` | HTTP/SOCKS5 entry, first-byte sniffing, per-conn parsing, relay, dial routing |
| `internal/dispatch/params.go` | Param parsing (path tokens + `X-Proxy-*` headers) → overlay → directive |
| `internal/dispatch/socks.go` | SOCKS5 (RFC 1928/1929): handshake, CONNECT, username-token directive |
| `internal/dispatch/params_test.go`, `socks_test.go` | Parsing + handshake tests |
| `internal/outbound/pool/directive.go` | `SelectionDirective`, `Strategy`, `NodeFilter`, ctx carrier, bucket key |
| `internal/outbound/pool/sticky.go` | stable bucket + session stickiness (lazy-TTL, no goroutine) |
| `internal/outbound/pool/sticky_test.go` | Sticky + filter tests |
| `internal/app/routing_controller.go` | `RoutingController`: lifecycle + hot-apply (rules/strategy/final) vs reload |
| `internal/app/routing.go` | Shared adapters (geoip→CountryLookup, logger, engine builder) |
| `frontend/src/components/RoutingPanel.tsx` | UI page (see §6) |
| `docs/smart-routing.md` | Design reference |
| `docs/smart-routing-status.md` | **This file** |

### Modified files
| File | Change |
|---|---|
| `internal/outbound/pool/pool.go`, `shared_state.go` | Directive selection, stable preference/fallback, transactional shared failure/blacklist/traffic state, and pure probe callbacks |
| `internal/monitor/manager.go` | Long-lived self-assessment, live thresholds, generation/round/revision-scoped probe commits, and exclusive candidate probe summaries |
| `internal/monitor/server.go`, `internal/monitor/connectors.go` | Routing API, synchronized runtime dependency snapshots, and reusable, transactionally rebound management listener that preserves integrations and compat leases |
| `internal/config/config.go` | `RoutingConfig` schema, defaults, `DispatchListen()`, `RoutingTakesOverPoolInbound()`, `RoutingUseDefaultRules()`, `SaveSettings` persists `Routing` |
| `internal/config/config_test.go` | `TestRoutingTakesOverPoolInbound` |
| `internal/builder/builder.go` | Route-A inbound takeover plus a global pool outbound for routing-enabled pure multi-port mode |
| `internal/boxmgr/manager.go` | Transactional box/routing/monitor/shared-state reload, generation health gate, reload-safe pool accessors |
| `internal/app/app.go` | Wire `RoutingController` at startup and register reload/config lifecycle hooks |
| `internal/routerule/engine.go` | Atomic `SetRulesAndFinal` so configured final policy stays authoritative |
| `frontend/src/App.tsx` | `routing` tab + menu item + route |
| `frontend/src/api/client.ts` | `fetchRoutingStatus/Config`, `updateRoutingConfig` |
| `frontend/src/types/index.ts` | Routing types |
| `internal/monitor/assets/*` | Rebuilt embedded frontend bundle |
| `README.md`, `service/base/README.md`, `service/base/config.example.yaml` | Docs + config example (incl. SOCKS5 username-token convention) |

### Deleted
- `internal/geoip/router.go` — dead code (old path-prefix pseudo-splitting, never wired).

---

## 4. Status: implemented & verified

**Implementation is complete in this branch.** Verification uses durable
regression tests plus the real-binary and isolated-container evidence below.

### Durable test coverage
`go test -count=1 ./...` passes across app/boxmgr/builder/config/dispatch/geoip/monitor/pool/routerule/subscription.
The current suites cover:

- same-port HTTP + SOCKS5 sniffing, DIRECT/PROXY dial choice, keep-alive,
  live pool replacement, CONNECT header precedence, and dispatcher Stop cleanup;
- routing disabled/enabled transitions, listen and auth changes, unchanged-topology
  hot apply, bind failures, idle transitions, and rollback restoration;
- box candidate close/health failures, shared-state registry rollback, monitor
  generation probes, periodic-round supersession, late probe invalidation,
  retired-registry monitor detachment, and reusable management listener
  rebind/rollback including target-auth activation and the `/api/reload`
  self-trigger path;
- stable long-lived preference/fallback, hot thresholds, final-policy authority,
  pure multi-port topology, `Config.Clone`, root config rendering, and workflow
  validation.

### Historical real-binary evidence (7/7, EXIT=0)
A now-removed local harness also ran the **real binary** with a fake upstream
proxy (hit-counter = ground truth) and fake origin on multiple loopback IPs.
This is supplementary evidence; the durable package/integration tests above are
the maintained regression contract.

```
PASS (HTTP):   DIRECT rule bypasses proxy            (127.0.0.2 → direct)
PASS (HTTP):   foreign IP proxied by default entry   (127.0.0.9 → pool)
PASS (HTTP):   nosplit token forces proxy on DIRECT host
PASS (SOCKS5): DIRECT rule bypasses proxy            (same port, sniffed)
PASS (SOCKS5): foreign IP proxied via socks5 entry
PASS (API):    /api/routing/status reports enabled
PASS (API):    PUT rule hot-applied (127.0.0.9→DIRECT, NO restart)
```

### Isolated container evidence
On 2026-07-19, image
`easyproxy/easy-proxy-monorepo-service:smart-routing-final-20260719`
(`sha256:13f9319412f420295298f266f45148f96b4ec598d9645b5444c21adc1fe93488`)
was exercised in disposable network `epe2e-final-20260719045112-3217` with separate
nginx origin, Python upstream-proxy, curl client, and EasyProxy containers.
The hit counter was on the upstream proxy, not inferred from EasyProxy logs:

| Scenario | Result | Upstream hit | Outcome |
|---|---:|---:|---|
| HTTP rule `DIRECT` | `200`, 896-byte body | `http-direct`: `0 -> 0` | bypassed upstream |
| `X-Proxy-Split: off` | `200`, 896-byte body | `http-direct`: `0 -> 1` | header forced PROXY |
| no rule, `final_policy: PROXY` | `200`, 896-byte body | `final-origin`: `0 -> 1` | final policy authoritative |
| SOCKS5 on the same `22323` port, `DIRECT` rule | `200`, 896-byte body | `socks-direct`: `0 -> 0` | SOCKS sniff + DIRECT |
| SOCKS username `nosplit` | `200`, 896-byte body | `socks-direct`: `0 -> 1` | username forced PROXY |

The upstream self-check also returned an `X-Upstream-Proxy: yes` marker for an
absolute-form HTTP request, while EasyProxy's node outbound correctly used
CONNECT for the real forwarding cases. The final routing API reported
`enabled=true`, `listen=0.0.0.0:22323`, `final_policy=PROXY`, and `rule_count=2`;
the node ended with `available_nodes=1`, `traffic_proven_usable=true`,
`probe_available_nodes=1`, `effective_available=true`,
`availability_source=probe+recent_traffic`, and `failure_count=0`.

The first startup health snapshot was `0/1`, but the next five-second periodic
round established `1/1` probe availability and later rounds remained healthy;
the final state therefore did not depend on traffic fallback alone. All
temporary containers, network, and files were removed, and the stopped legacy
`easy-proxy` container was not touched.

### Real bugs found *only* by e2e (all fixed)
1. **Port conflict** — dispatcher and pool inbound both bound the entry port;
   routing "on" was effectively a no-op. Fixed via builder inbound-skip.
2. **Shared `http.Server` reuse broke** — the initial design fed connections one
   at a time to a shared `http.Server` via a one-shot listener; after the first
   hijacked CONNECT, later connections died. Rewrote to parse HTTP directly off
   the connection (`serveHTTP` loop) with per-conn panic recovery.
3. **CONNECT token parsing** — must read the token prefix from `req.RequestURI`,
   not `req.Host` (Go splits `nosplit/host:443` into Host=`nosplit`).
4. **Stale-binary trap** — a build with a deprecated `with_ech` tag failed but a
   piped `| head` swallowed the exit code, leaving an old binary that 404'd on
   the new route. Lesson baked into §5: **always check build EXIT=0**.

---

## 5. Build / test / run

### Build (from `service/base/`)
```bash
# Frontend (only if UI changed) — outputs into internal/monitor/assets/
cd frontend && npm run build && cd ..

# Binary — MUST verify EXIT=0 (do not trust piped output)
go build -tags "with_quic with_grpc with_utls with_dhcp with_gvisor" \
  -o easy-proxy ./cmd/easy_proxies
echo "exit=$?"
```

### Test
```bash
go test ./...
```

### Run
```bash
./easy-proxy --config config.yaml
```

### Enable routing (config.yaml)
```yaml
routing:
  enabled: true            # default false
  default_strategy: stable # stable | session | auto
  use_default_rules: true  # append built-in China-direct set
  final_policy: PROXY      # DIRECT | PROXY
  rules:
    - "DOMAIN-SUFFIX,internal.corp,DIRECT"
  # listen: ""             # empty → take over listener host:port (route A)
```
See `service/base/config.example.yaml` for the full annotated block (incl. the
SOCKS5 username-token convention and request-param grammar).

---

## 6. UI

- **Where**: management panel, default `http://<host>:29888` (`management.listen`,
  default `0.0.0.0:29888`). Login only if `management.password` is set.
- **Tab**: sidebar "智能分流" (between 节点管理 and 调试面板). Direct hash:
  `http://<host>:29888/#routing`.
- **Component**: `frontend/src/components/RoutingPanel.tsx`.
- **Controls**: enable toggle, default strategy, final policy, rule editor
  (line-based), default-set toggle, remote rule providers, long-lived thresholds,
  session TTL, live status (rule count + sticky bindings).
- **The UI only exists in a binary built from this branch.** An old running
  binary will not have it — rebuild + redeploy.

---

## 7. Hot-update model (important for future work)

The management API applies edits with the cheapest sufficient mechanism
(`RoutingController.ApplyHot` in `routing_controller.go`):

| Edit | Effect |
|---|---|
| rules / rule_providers / final_policy / default_strategy | **Hot-applied** to the running engine + dispatcher. No restart, no dropped connections. `need_reload=false`. |
| long-lived `min_uptime` / `min_success_rate` | **Hot-applied** to the live monitor manager and existing entries. `need_reload=false`; zero restores the default threshold. |
| enabled / listen / session TTL | **Needs reload**. boxmgr coordinates the candidate box, dispatcher topology, and monitor listener transactionally; `need_reload=true` and the UI surfaces it. |

Both routing and management listener runtime state are kept on the same server
object across reloads. A new management address is bound synchronously, its
target config/auth snapshot is applied before activation, and the old listener
is drained asynchronously; a bind failure leaves the old address serving and
aborts the candidate transaction. Handler dependencies are read as local
snapshots before calling reload-capable managers, so a listener rebind cannot
tear an interface value or hold a dependency lock across a reload.

---

## 8. Known limitations / next steps

- **GEOIP rules match literal-IP destinations only.** The dispatcher does not
  perform per-request DNS resolution on the hot path; domain destinations use
  domain rules and `final_policy`.
- **SOCKS5 is CONNECT-only.** UDP ASSOCIATE and BIND are not implemented.
- **Route B needs explicit Docker/Compose publication.** A custom
  `routing.listen` port must be mapped from the container; route A uses the
  existing listener publication.
- **Port-bound fixed directives are not public configuration.** The internal
  overlay exists for dispatcher composition, but users currently configure
  path/username tokens and `X-Proxy-*` headers.
- **Sticky affinity is pool-instance state.** A successful box reload starts a
  fresh pool and therefore fresh stable/session bindings; shared failure,
  blacklist, traffic, and monitor state are transactionally isolated and are
  restored exactly on rollback.
- **Probe cancellation is cooperative at the network-call boundary.** The
  scheduler invalidates generation/round/epoch tokens and drains its managed
  coordinators, so late results cannot update health. A custom probe callback
  that ignores context can still leave its own goroutine alive until its I/O
  returns; built-in pool probes use bounded socket deadlines.
- **`pruneTags` in sticky.go is intentionally unused.** Candidate selection
  skips tags absent from the live pool, and reload creates a fresh sticky store.

---

## 9. Environment notes (for the next AI on this machine)

- Windows + PowerShell; use repo-relative or absolute Windows paths for test
  artifacts that cross Go, Node, and Python tooling.
- Normal network tests should use `-count=1` or a small repeat count. An earlier
  `-count=1000` loop exhausted the Windows dynamic port range with `TIME_WAIT`.
- `go test -race` is not available in the current toolchain because CGO is
  disabled; ordinary tests and `go vet ./...` are the current local evidence.
- The hardening changes described here are committed on this branch. The two
  `tmp-pc2` tarballs are local OCI image archives and are intentionally excluded
  from source control; the legacy stopped container remains outside this change.
