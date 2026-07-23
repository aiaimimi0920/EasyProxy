# Native TUN Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade the existing EasyProxy Debian gateway from IPv4 TCP TPROXY to an EasyProxy-owned TUN data plane supporting TCP, UDP, IPv4, IPv6, DNS interception, and fail-open DIRECT.

**Architecture:** Add a sing-box TUN inbound to the existing EasyProxy-managed sing-box instance, route it through a custom EasyProxy gateway outbound, and let the existing pool outbound remain the only proxy-node scheduler. Extend the current gateway supervisor to own trusted-ingress marks and IPv4/IPv6 routes to `easyproxy0`; TUN and TPROXY modes remain mutually exclusive and cleanup always precedes process teardown.

**Tech Stack:** Go 1.24, sing-box v1.12.25, sing-tun v0.7.13, gVisor/system mixed stack, nftables, iproute2, YAML, React/Vite, Python asset tests, Linux network namespaces, Docker host networking.

---

## File Map

- `service/base/internal/config/config.go`: public TUN and local-rule-file configuration, defaults, cloning.
- `service/base/internal/config/routing_validation.go`: path and rule-file validation.
- `service/base/internal/routerule/engine.go`: destination request matching including port and network.
- `service/base/internal/routerule/local.go`: deterministic local rule-file loading.
- `service/base/internal/outbound/gatewayroute/route.go`: EasyProxy DIRECT/PROXY TCP/UDP outbound.
- `service/base/internal/outbound/gatewayroute/route_test.go`: route decisions, UDP, fallback, redacted stats.
- `service/base/internal/builder/builder.go`: direct-only runtime, TUN inbound, gateway-route outbound, DNS route actions.
- `service/base/internal/boxmgr/manager.go`: register the custom outbound and permit zero-node TUN runtime.
- `service/base/internal/gateway/supervisor.go`: TUN policy routes and nftables ownership.
- `service/base/internal/gateway/manager.go`: mode-aware lifecycle and runtime status.
- `service/base/internal/monitor/server.go`: authenticated capability/status response.
- `service/base/frontend/src/App.tsx`: compact TUN status in the existing gateway view.
- `deploy/gateway/debian/*`: `/dev/net/tun`, IPv6 forwarding, prerequisites, rollback docs.
- `scripts/validate-tun-gateway-linux.sh`: disposable namespace acceptance test and cleanup.

### Task 1: Add TUN configuration and local rule files

**Files:**
- Modify: `service/base/internal/config/config.go`
- Modify: `service/base/internal/config/config_test.go`
- Modify: `service/base/internal/config/routing_validation.go`
- Create: `service/base/internal/routerule/local.go`
- Create: `service/base/internal/routerule/local_test.go`
- Modify: `config.example.yaml`
- Modify: `service/base/config.example.yaml`
- Modify: `deploy/service/base/config.template.yaml`

- [ ] **Step 1: Write failing config tests**

Add `TestGatewayTunDefaults`, `TestGatewayTunRejectsConflicts`, `TestGatewayTunClone`, and `TestLoadLocalRuleFiles` with assertions equivalent to:

```go
func TestGatewayTunDefaults(t *testing.T) {
    cfg := minimalConfig()
    cfg.Gateway.Enabled = true
    cfg.Gateway.Mode = "tun"
    require.NoError(t, cfg.normalizeInternal())
    require.Equal(t, "easyproxy0", cfg.Gateway.Tun.InterfaceName)
    require.Equal(t, "mixed", cfg.Gateway.Tun.Stack)
    require.Equal(t, uint32(1500), cfg.Gateway.Tun.MTU)
    require.True(t, cfg.Gateway.Tun.IPv4)
    require.True(t, cfg.Gateway.Tun.UDP)
    require.Equal(t, "198.18.0.0/16", cfg.Gateway.Tun.FakeIPv4Range)
}
```

The conflict table must reject TUN plus `capture.tcp: tproxy`, DNS hijack with gateway DNS disabled, invalid stack, MTU outside 1280-9000, malformed fake-IP prefixes, and fake-IP overlap with trusted CIDRs.

- [ ] **Step 2: Run focused tests and confirm failure**

Run:

```powershell
cd service/base
go test ./internal/config ./internal/routerule -run 'TestGatewayTun|TestLoadLocalRuleFiles' -count=1
```

Expected: compile failures because `GatewayTunConfig`, `Routing.RuleFiles`, and `LoadLocalRuleFiles` do not exist.

- [ ] **Step 3: Implement the config contract**

Add these fields:

```go
type GatewayConfig struct {
    Enabled bool                           `yaml:"enabled"`
    Mode    string                         `yaml:"mode"`
    Listen  string                         `yaml:"listen"`
    Ingress GatewayIngressConfig           `yaml:"ingress"`
    Capture GatewayCaptureConfig           `yaml:"capture"`
    Routing GatewayRoutingConfig           `yaml:"routing"`
    DNS     GatewayDNSConfig               `yaml:"dns"`
    Tun     GatewayTunConfig               `yaml:"tun"`
    Devices map[string]GatewayDeviceConfig `yaml:"devices"`
}

type GatewayTunConfig struct {
    InterfaceName string `yaml:"interface_name"`
    Addresses []string `yaml:"addresses"`
    Stack string `yaml:"stack"`
    MTU uint32 `yaml:"mtu"`
    IPv4 bool `yaml:"ipv4"`
    IPv6 bool `yaml:"ipv6"`
    UDP bool `yaml:"udp"`
    StrictRoute bool `yaml:"strict_route"`
    DNSHijack bool `yaml:"dns_hijack"`
    FakeIP bool `yaml:"fake_ip"`
    FakeIPv4Range string `yaml:"fake_ipv4_range"`
    FakeIPv6Range string `yaml:"fake_ipv6_range"`
}
```

Add `RuleFiles []string `yaml:"rule_files"`` to `RoutingConfig`. Defaults are `easyproxy0`, addresses `172.31.255.1/30` and `fd31:255::1/126`, `mixed`, MTU 1500, IPv4/UDP/strict-route true, IPv6 false until deployment enables it, and the approved fake ranges. Treat `gateway.mode: transparent` as the legacy TPROXY mode and `gateway.mode: tun` as mutually exclusive with non-disabled capture fields. When TUN mode is selected, default TCP/UDP capture fields to `disabled` instead of inheriting TPROXY defaults.

`routerule.LoadLocalRuleFiles(paths)` reads UTF-8 text or YAML `payload:` files in declared order, ignores blank/comment lines, validates every materialized rule with `ValidateRules`, and returns a path-qualified error. Resolve relative paths against the primary config directory, not the process working directory.

- [ ] **Step 4: Run and format**

```powershell
cd service/base
gofmt -w internal/config/config.go internal/config/config_test.go internal/config/routing_validation.go internal/routerule/local.go internal/routerule/local_test.go
go test ./internal/config ./internal/routerule -run 'TestGatewayTun|TestLoadLocalRuleFiles' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add service/base/internal/config service/base/internal/routerule config.example.yaml service/base/config.example.yaml deploy/service/base/config.template.yaml
git commit -m "feat(config): add native TUN gateway settings"
```

### Task 2: Extend routing rules for TUN request metadata

**Files:**
- Modify: `service/base/internal/routerule/engine.go`
- Modify: `service/base/internal/routerule/engine_test.go`
- Modify: `service/base/internal/app/routing_controller.go`
- Modify: `service/base/internal/app/routing_controller_test.go`

- [ ] **Step 1: Write failing request-matching tests**

Add a request type and tests for domain, IP, destination port, and network:

```go
type Request struct {
    Host string
    Port uint16
    Network string
}

func TestEngineMatchRequestDestinationPort(t *testing.T) {
    e := New([]string{"DST-PORT,53,DIRECT", "FINAL,PROXY"}, PolicyProxy, nil)
    got := e.MatchRequest(Request{Host: "8.8.8.8", Port: 53, Network: "udp"})
    require.Equal(t, PolicyDirect, got)
}
```

Keep `Match(host)` as a compatibility wrapper around `MatchRequest`.

- [ ] **Step 2: Run the test and confirm failure**

```powershell
cd service/base
go test ./internal/routerule -run 'TestEngineMatchRequest' -count=1
```

Expected: compile failure because `Request` and `MatchRequest` are absent.

- [ ] **Step 3: Implement strict `DST-PORT` support**

Parse one port or an inclusive `start-end` range. Reject port zero, values above 65535, reversed ranges, and extra fields. Store destination-port matchers in the ordered rule list and evaluate them against `Request.Port`. Preserve existing rule ordering and `FINAL` behavior.

Merge local rule files before remote provider rules in `routing_controller.go`:

```text
explicit routing.rules
local routing.rule_files
remote routing.rule_providers
default rules
final policy
```

- [ ] **Step 4: Run routing tests**

```powershell
cd service/base
gofmt -w internal/routerule/engine.go internal/routerule/engine_test.go internal/app/routing_controller.go internal/app/routing_controller_test.go
go test ./internal/routerule ./internal/app -run 'TestEngineMatchRequest|TestRouting.*RuleFile' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add service/base/internal/routerule service/base/internal/app/routing_controller.go service/base/internal/app/routing_controller_test.go
git commit -m "feat(routing): match TUN destination metadata"
```

### Task 3: Add the EasyProxy gateway route outbound

**Files:**
- Create: `service/base/internal/outbound/gatewayroute/route.go`
- Create: `service/base/internal/outbound/gatewayroute/route_test.go`
- Modify: `service/base/internal/outbound/pool/pool.go`
- Modify: `service/base/internal/outbound/pool/pool_test.go`

- [ ] **Step 1: Write failing outbound tests**

Cover TCP DIRECT, TCP PROXY, UDP PROXY, UDP-incapable pool member exclusion, and no-node DIRECT fallback. Construct `adapter.InboundContext` in the call context so the outbound sees sniffed domains and source addresses:

```go
metadata := &adapter.InboundContext{
    Network: N.NetworkUDP,
    Source: M.ParseSocksaddr("192.168.15.50:40000"),
    Destination: M.ParseSocksaddr("8.8.8.8:53"),
    Domain: "dns.google",
}
ctx := adapter.WithContext(context.Background(), metadata)
packetConn, err := outbound.ListenPacket(ctx, metadata.Destination)
```

Assert separate counters for DIRECT, PROXY, no-node fallback, TCP, UDP, IPv4, and IPv6.

- [ ] **Step 2: Run tests and confirm failure**

```powershell
cd service/base
go test ./internal/outbound/gatewayroute ./internal/outbound/pool -count=1
```

Expected: package-not-found/compile failure for `gatewayroute`.

- [ ] **Step 3: Implement the custom outbound**

Define:

```go
const Type = "easyproxy-gateway-route"
const Tag = "easyproxy-gateway-route"

type Options struct {
    Rules []string
    FinalPolicy string
    NoAvailableProxyPolicy string
    DefaultStrategy pool.Strategy
    PoolTag string
    DirectTag string
}
```

Register it with sing-box's outbound registry. The constructor receives the same-box `adapter.Router` plus `service.FromContext[adapter.OutboundManager](ctx)` and resolves `PoolTag`/`DirectTag` lazily from that outbound manager so construction order does not create a cycle. `DialContext` and `ListenPacket` read `adapter.ContextFrom(ctx)`, choose `metadata.Domain` before destination IP, call `MatchRequest`, and route to direct or pool. PROXY failures use DIRECT only when the configured fallback is DIRECT.

Pass a `pool.SelectionDirective` with the configured default strategy. Extend pool candidate selection so it filters members by the requested network before attempting them; unsupported UDP must not be recorded as a generic TCP failure.

- [ ] **Step 4: Run outbound tests**

```powershell
cd service/base
gofmt -w internal/outbound/gatewayroute internal/outbound/pool
go test ./internal/outbound/gatewayroute ./internal/outbound/pool -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add service/base/internal/outbound/gatewayroute service/base/internal/outbound/pool
git commit -m "feat(outbound): route native TUN traffic"
```

### Task 4: Build TUN and direct-only sing-box runtimes

**Files:**
- Modify: `service/base/internal/builder/builder.go`
- Modify: `service/base/internal/builder/builder_test.go`
- Modify: `service/base/internal/builder/dns_test.go`
- Modify: `service/base/internal/boxmgr/manager.go`
- Modify: `service/base/internal/boxmgr/manager_test.go`

- [ ] **Step 1: Write failing builder tests**

Add tests asserting that TUN mode creates:

```text
inbound type: tun
inbound tag: easyproxy-tun
interface: easyproxy0
auto_route: false
strict_route: true
sniff: true
route final: easyproxy-gateway-route
outbounds: direct + easyproxy-gateway-route + proxy-pool when nodes exist
```

Add `TestBuildTunGatewayWithoutNodesProducesDirectOnlyRuntime`; it must return valid options containing no pool outbound.

- [ ] **Step 2: Run and confirm failure**

```powershell
cd service/base
go test ./internal/builder ./internal/boxmgr -run 'TestBuildTun|TestTunGatewayWithoutNodes' -count=1
```

Expected: FAIL because the builder still returns `ErrNoValidNodes` and emits no TUN inbound.

- [ ] **Step 3: Implement TUN option construction**

Create a built-in direct outbound tagged `direct`. Add `option.TunInboundOptions` with `AutoRoute: false`, configured IPv4/IPv6 addresses, stack, MTU, UDP timeout, strict route, and `InboundOptions{SniffEnabled: true, SniffOverrideDestination: true}`. Add a route rule sending `easyproxy-tun` to `gatewayroute.Tag` and DNS traffic to the sing-box DNS action when gateway DNS is enabled.

Only suppress `ErrNoValidNodes` when an enabled TUN gateway has DIRECT fallback. In that case omit the pool outbound, keep direct/gateway-route/TUN/DNS, and let later source refresh rebuild the candidate runtime with nodes.

Register `gatewayroute.Register(outboundRegistry)` beside `pool.Register`. Keep TUN disabled on non-Linux runtime validation but allow option construction tests on Windows.

- [ ] **Step 4: Run builder and manager tests**

```powershell
cd service/base
gofmt -w internal/builder internal/boxmgr/manager.go internal/boxmgr/manager_test.go
go test ./internal/builder ./internal/boxmgr -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add service/base/internal/builder service/base/internal/boxmgr
git commit -m "feat(builder): create native TUN sing-box runtime"
```

### Task 5: Add TUN supervisor mode and IPv6 policy routing

**Files:**
- Modify: `service/base/internal/gateway/supervisor.go`
- Modify: `service/base/internal/gateway/supervisor_test.go`
- Create: `service/base/internal/gateway/tun_linux.go`
- Create: `service/base/internal/gateway/tun_other.go`
- Create: `service/base/internal/gateway/tun_test.go`

- [ ] **Step 1: Write command-generation and cleanup tests**

Use the fake runner to assert this order:

```text
create/verify easyproxy0
add IPv4 and optional IPv6 route tables
add main-table bypass rule for EasyProxy egress mark
create nftables trusted-ingress mark rules
verify route/interface state
```

Every failure index must produce cleanup commands in reverse ownership order. TUN mode must not emit `tproxy` expressions or create port `15001` capture rules.

- [ ] **Step 2: Run tests and confirm failure**

```powershell
cd service/base
go test ./internal/gateway -run 'TestTunSupervisor|TestTunCleanup' -count=1
```

Expected: FAIL because only transparent TPROXY commands exist.

- [ ] **Step 3: Implement mode-aware supervisor commands**

Keep the current `easyproxy_gateway` table name and use distinct input and egress marks. Match only configured ingress interfaces/patterns and trusted source CIDRs. Add bypass sets before the capture mark for loopback, private/link-local/multicast, management ports, DNS control traffic, VM addresses, resolver endpoints, and node endpoints.

For IPv4 and IPv6, install a dedicated route table whose default route is `dev easyproxy0`. Egress-marked EasyProxy sockets select the normal main table. On stop, remove classification first, then rules/routes, and let sing-box close the TUN interface last.

- [ ] **Step 4: Run gateway tests**

```powershell
cd service/base
gofmt -w internal/gateway
go test ./internal/gateway -count=1
```

Expected: PASS on Windows through fake command runners and unsupported-platform stubs.

- [ ] **Step 5: Commit**

```powershell
git add service/base/internal/gateway
git commit -m "feat(gateway): manage TUN policy routing"
```

### Task 6: Wire lifecycle, reload, status, and DNS/fake-IP

**Files:**
- Modify: `service/base/internal/gateway/manager.go`
- Modify: `service/base/internal/gateway/manager_reload.go`
- Modify: `service/base/internal/gateway/manager_test.go`
- Modify: `service/base/internal/app/app.go`
- Modify: `service/base/internal/app/routing_controller.go`
- Modify: `service/base/internal/monitor/server.go`
- Modify: `service/base/internal/monitor/server_test.go`

- [ ] **Step 1: Write lifecycle and status tests**

Test disabled, transparent, and TUN modes; zero-node direct-only startup; candidate reload failure; cleanup-before-close; stale-state cleanup; and authenticated status containing capability flags and counters.

- [ ] **Step 2: Run tests and confirm failure**

```powershell
cd service/base
go test ./internal/gateway ./internal/app ./internal/monitor -run 'Test.*Tun|TestGatewayStatus' -count=1
```

Expected: FAIL because the manager reports only a TCP listener and transparent counters.

- [ ] **Step 3: Implement lifecycle ordering**

In TUN mode, start the candidate box first so `easyproxy0`, DNS, and gateway-route exist, then apply capture. On reload, suspend capture, build/start candidate, atomically apply new bypasses, swap box, and re-enable capture. On failure, keep or restore the old verified runtime. On stop, remove capture before closing sing-box.

Force a sing-box candidate rebuild for routing-rule/profile changes while TUN is active so the gateway-route engine cannot lag behind the explicit dispatcher. Transparent mode keeps its current hot-update behavior.

Extend gateway status with mode, interface, stack, MTU, IPv4/IPv6/TCP/UDP/DNS flags, active TCP/UDP sessions, DIRECT/PROXY/no-node counts, fake-IP counters, and the last lifecycle error.

- [ ] **Step 4: Run lifecycle tests**

```powershell
cd service/base
gofmt -w internal/gateway internal/app internal/monitor
go test ./internal/gateway ./internal/app ./internal/monitor -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```powershell
git add service/base/internal/gateway service/base/internal/app service/base/internal/monitor
git commit -m "feat(app): operate native TUN gateway lifecycle"
```

### Task 7: Update console and Debian deployment assets

**Files:**
- Modify: `service/base/frontend/src/App.tsx`
- Modify: `service/base/frontend/src/App.test.tsx`
- Modify: `deploy/gateway/debian/docker-compose.yaml`
- Modify: `deploy/gateway/debian/bootstrap-gateway.sh`
- Modify: `deploy/gateway/debian/easyproxy-forwarding.nft`
- Modify: `deploy/gateway/debian/README.md`
- Modify: `tests/test_debian_gateway_assets.py`
- Modify: `docs/transparent-gateway.md`

- [ ] **Step 1: Add failing frontend and asset tests**

Assert the gateway view labels TUN mode and capability state without adding a second node-management view. Asset tests require `/dev/net/tun`, `NET_ADMIN`, `NET_RAW`, host networking, IPv4/IPv6 forwarding sysctls, and no bootstrap-owned EasyProxy capture table.

- [ ] **Step 2: Run tests and confirm failure**

```powershell
cd service/base
npm test -- --run App.test.tsx
cd ../..
python -m pytest tests/test_debian_gateway_assets.py -q -s
```

Expected: FAIL for missing TUN UI fields and device mount.

- [ ] **Step 3: Implement UI and deployment changes**

Add a compact capability table to the existing routing/gateway screen. Update compose with:

```yaml
devices:
  - /dev/net/tun:/dev/net/tun
```

Bootstrap validates TUN availability and writes IPv6 forwarding sysctls, but continues to leave capture rules to EasyProxy. Document TUN-to-TPROXY rollback and stale-state inspection commands.

- [ ] **Step 4: Build frontend and run tests**

```powershell
cd service/base
npm test -- --run App.test.tsx
npm run build
cd ../..
python -m pytest tests/test_debian_gateway_assets.py -q -s
```

Expected: PASS and refreshed embedded frontend assets.

- [ ] **Step 5: Commit**

```powershell
git add service/base/frontend service/base/internal/monitor/assets deploy/gateway/debian tests/test_debian_gateway_assets.py docs/transparent-gateway.md
git commit -m "feat(deploy): expose native TUN gateway"
```

### Task 8: Add isolated Linux acceptance validation

**Files:**
- Create: `scripts/validate-tun-gateway-linux.sh`
- Modify: `scripts/validate-transparent-gateway.ps1`
- Modify: `README.md`

- [ ] **Step 1: Write shell structure and cleanup assertions**

The script must use a unique namespace/veth/table prefix, register `trap cleanup EXIT INT TERM`, verify every created object is absent after cleanup, and refuse to run against an unrecognized interface or production CIDR unless `--allow-production` is explicit.

- [ ] **Step 2: Implement acceptance cases**

Run clients with proxy environment variables removed and validate:

```text
IPv4 Google HTTPS through PROXY
UDP DNS through gateway
controlled UDP/QUIC through a UDP-capable node
private destination DIRECT bypass
zero-node Google HTTPS through DIRECT fallback
container restart recovery
optional IPv6 TCP/UDP when the host has working IPv6 egress
```

The script captures authenticated gateway status before and after each case and verifies the expected counter changed.

- [ ] **Step 3: Run static and focused validation**

```powershell
bash -n scripts/validate-tun-gateway-linux.sh
python -m pytest tests/test_debian_gateway_assets.py -q -s
cd service/base
go test ./internal/config ./internal/routerule ./internal/outbound/... ./internal/builder ./internal/boxmgr ./internal/gateway ./internal/app ./internal/monitor -count=1
```

Expected: PASS. The Linux namespace script is executed only in a disposable Linux environment in Task 9.

- [ ] **Step 4: Commit**

```powershell
git add scripts/validate-tun-gateway-linux.sh scripts/validate-transparent-gateway.ps1 README.md
git commit -m "test: validate native TUN gateway"
```

### Task 9: Full repository verification and Debian VM deployment

**Files:**
- Runtime-only: `/opt/easyproxy-gateway/config/config.yaml`
- Runtime-only: `/opt/easyproxy-gateway/compose/docker-compose.yaml`
- Runtime-only: `/opt/easyproxy-gateway/data`

- [ ] **Step 1: Run local full verification**

```powershell
python -m pytest -q -s
cd service/base
go test -count=1 ./...
go vet ./...
gofmt -l .
npm test -- --run
npm run build
cd ../..
git diff --check
```

Expected: all tests pass, `gofmt -l` prints nothing, and diff check is clean.

- [ ] **Step 2: Build and import a new gateway image without replacing the running container**

Use a unique image tag and validate it in a disposable container/network namespace first. Save a VM snapshot and copy the current runtime config/data before changing compose. Do not modify the DSM container at `192.168.15.200`.

- [ ] **Step 3: Run the isolated Linux acceptance script**

```bash
sudo ./scripts/validate-tun-gateway-linux.sh \
  --image easyproxy/easy-proxy:<new-tag> \
  --config /opt/easyproxy-gateway/config/config.yaml
```

Expected: all mandatory IPv4 TCP, UDP DNS, UDP proxy, private bypass, zero-node fallback, restart, and cleanup cases pass. IPv6 is marked PASS only when real VM IPv6 egress is available; otherwise production config keeps IPv6 disabled.

- [ ] **Step 4: Switch the VM container and verify reboot persistence**

Enable `gateway.mode: tun`, preserve `no_available_proxy_policy: DIRECT`, deploy the tested image, verify ports 22323/29888, run Google/UDP/DNS tests, reboot the VM, and repeat. Confirm no TPROXY capture table remains active in TUN mode.

- [ ] **Step 5: Commit runtime documentation and push**

Record only sanitized image/tag and acceptance results. Do not commit runtime config, passwords, node URIs, or database files.

```powershell
git add docs deploy scripts tests service/base
git commit -m "docs: record native TUN gateway validation"
git push origin main
```
