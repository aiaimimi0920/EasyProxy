# Multi-Overlay Transparent Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans (or superpowers:subagent-driven-development) to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Add a network-fabric-independent transparent TCP gateway that accepts trusted physical-LAN, Tailscale, and 星空组网 traffic, reuses EasyProxy DIRECT/PROXY routing and node selection, and fails open to DIRECT when no usable node exists.

**Architecture:** The gateway is a separate ingress beside the existing explicit HTTP/CONNECT/SOCKS5 dispatcher. A Linux transparent listener receives TPROXY'd TCP connections, derives the original destination from the transparent socket, and invokes the same routing engine and pool outbound used by dispatch.Server. A lifecycle supervisor owns policy-routing/nftables rules and always removes them on stop. Configuration describes interfaces/CIDRs and device aliases without naming any overlay provider in code.

**Tech Stack:** Go 1.24, net, Linux syscall/x/sys/unix, existing routerule.Engine and outbound/pool, YAML config, nftables/ip command supervisor, Go unit tests, Linux integration smoke script.

---

### Task 1: Add gateway configuration and validation

**Files:**
- Modify: service/base/internal/config/config.go
- Modify: service/base/internal/config/config_test.go
- Modify: service/base/config.example.yaml

- [ ] Step 1: Write failing configuration tests.

Add tests for gateway defaults, invalid policy/CIDR rejection, and deep cloning of device aliases.

Expected assertions:
- applyDefaults() sets no_available_proxy_policy to DIRECT and listen to 0.0.0.0:15001.
- normalize() rejects policy DROP and malformed trusted CIDRs.
- Config.Clone() does not share device address slices with the original.

- [ ] Step 2: Run the focused tests and verify they fail.

Run: go test ./internal/config -run 'TestGateway' -count=1

Expected: compile failures because gateway config types/defaults are absent.

- [ ] Step 3: Implement config types and normalization.

Add GatewayConfig, GatewayIngressConfig, GatewayCaptureConfig, GatewayRoutingConfig, GatewayDNSConfig, and GatewayDeviceConfig next to RoutingConfig. Add Gateway GatewayConfig to Config.

Use these fields and YAML tags:
- GatewayConfig: Enabled, Mode, Listen, Ingress, Capture, Routing, DNS, Devices.
- GatewayIngressConfig: Interfaces, InterfacePatterns, TrustedCIDRs.
- GatewayCaptureConfig: TCP, UDP, PreserveOriginalDestination.
- GatewayRoutingConfig: FinalPolicy, NoAvailableProxyPolicy.
- GatewayDNSConfig: Enabled, Listen.
- GatewayDeviceConfig: Addresses []string.

In applyDefaults, default Mode to transparent, Listen to 0.0.0.0:15001, TCP capture to tproxy, UDP capture to disabled, FinalPolicy to PROXY, NoAvailableProxyPolicy to DIRECT, and DNS listen to 0.0.0.0:53. Keep the gateway disabled by default.

In normalizeInternal, reject policies other than DIRECT/PROXY, capture modes other than tproxy/disabled, malformed trusted CIDRs, malformed device addresses, and enabled gateway listen values without a port. Do not require overlay interface names to exist on the current host.

Extend Config.Clone to deep-copy gateway slices and the device map. Add a gateway section to config.example.yaml showing physical LAN, ts-*, and star-* examples while stating these names are deployment-specific.

- [ ] Step 4: Run focused config tests.

Run: go test ./internal/config -run 'TestGateway' -count=1

Expected: PASS.

- [ ] Step 5: Commit.

git add service/base/internal/config/config.go service/base/internal/config/config_test.go service/base/config.example.yaml
git commit -m "feat(config): add transparent gateway settings"

### Task 2: Build a reusable transparent TCP connection router

**Files:**
- Create: service/base/internal/dispatch/transparent.go
- Create: service/base/internal/dispatch/transparent_test.go
- Modify: service/base/internal/dispatch/server.go

- [ ] Step 1: Add failing router tests using net.Pipe.

Define a test connection whose LocalAddr() returns the original destination and RemoteAddr() returns the client address. Test DIRECT reaching a local TCP echo server, PROXY invoking an injected pool outbound, and unavailable pool with no_available_proxy_policy=DIRECT reaching the echo server directly.

- [ ] Step 2: Run the dispatch tests and verify failure.

Run: go test ./internal/dispatch -run 'TestTransparent' -count=1

Expected: compile failures because the transparent router does not exist.

- [ ] Step 3: Implement the router by reusing Server routing.

Add TransparentRouterConfig with DialTimeout and NoAvailableProxyPolicy. Add TransparentRouter with provider, engine, logger, and direct dialer.

ServeConn must:
1. Read conn.LocalAddr() as the original destination and reject non-TCP addresses or port 0.
2. Preserve conn.RemoteAddr() as the session/device source.
3. Evaluate the existing engine and use the existing directive selection.
4. Dial DIRECT or the pool using pool.WithDirective.
5. When a PROXY dial fails because the pool is unavailable and the configured fallback is DIRECT, retry with the direct dialer and log reason no_available_proxy.
6. Relay bytes with the existing relay helper.

Add Server.TransparentRouter() so app wiring can construct the router without copying routing logic.

- [ ] Step 4: Run focused dispatch tests.

Run: go test ./internal/dispatch -run 'TestTransparent' -count=1

Expected: PASS.

- [ ] Step 5: Commit.

git add service/base/internal/dispatch/transparent.go service/base/internal/dispatch/transparent_test.go service/base/internal/dispatch/server.go
git commit -m "feat(dispatch): route transparent TCP connections"

### Task 3: Add Linux transparent listener and rule lifecycle

**Files:**
- Create: service/base/internal/gateway/listener_linux.go
- Create: service/base/internal/gateway/listener_other.go
- Create: service/base/internal/gateway/supervisor.go
- Create: service/base/internal/gateway/supervisor_test.go
- Create: service/base/internal/gateway/listener_test.go

- [ ] Step 1: Define an injected command runner and listener tests.

Test that the supervisor applies ip rule, ip route, and nftables commands in order, is idempotent, and removes all rules on Stop. Test that non-Linux builds return ErrUnsupported without running commands. Use a fake runner; tests must not mutate the host network namespace.

- [ ] Step 2: Implement Linux listener socket setup.

Use net.ListenConfig.Control and golang.org/x/sys/unix to set IP_TRANSPARENT, SO_REUSEADDR, and SO_MARK before bind. Accept TCP connections from Gateway.Listen. Under TPROXY, the accepted socket LocalAddr is the original destination; wrap it in an OriginalDestinationConn that normalizes IPv4/IPv6 forms for the dispatch router. Reject connections whose source is outside trusted CIDRs. Interface matching is enforced by the nftables ingress rules, not by assuming a provider-specific interface exists.

- [ ] Step 3: Implement the rule supervisor.

Create Supervisor.Apply(ctx, GatewayConfig) and Supervisor.Stop(ctx). Generate rules only for configured trusted CIDRs/interfaces and add bypasses for loopback, private/overlay ranges, DNS/management ports, and marked EasyProxy egress. Track applied commands and run inverse commands on Stop. Treat already-exists and does-not-exist errors as idempotent success. Never put these commands in the container entrypoint.

- [ ] Step 4: Run gateway unit tests.

Run: go test ./internal/gateway -count=1

Expected: PASS on Windows through the fake runner and ErrUnsupported build path. A real TPROXY smoke test is Linux-only.

- [ ] Step 5: Commit.

git add service/base/internal/gateway
git commit -m "feat(gateway): add Linux transparent capture lifecycle"

### Task 4: Wire gateway lifecycle into the application

**Files:**
- Create: service/base/internal/gateway/manager.go
- Create: service/base/internal/gateway/manager_test.go
- Modify: service/base/internal/app/app.go
- Modify: service/base/internal/app/routing_controller.go

- [ ] Step 1: Add lifecycle tests.

Verify disabled gateway is a no-op; enabled gateway starts only after box manager and routing controller have a live pool; reload stops listener before rules are changed; shutdown removes capture rules even when listener startup fails; zero healthy nodes do not stop the gateway because DIRECT fallback remains available.

- [ ] Step 2: Implement gateway.Manager.

Manager.Start(ctx, cfg, router) applies the supervisor, starts the Linux listener, and serves each accepted connection through TransparentRouter.ServeConn. Manager.Stop cancels accepts, waits for active connections, then removes rules. On non-Linux, enabled gateway returns a clear error while disabled remains a no-op. Expose a status snapshot with enabled, listener, applied, active connections, and last error.

- [ ] Step 3: Wire ordering and hot reload.

Construct the gateway after boxMgr.Start and routingCtl.StartState, using the current routing engine and pool provider. Register it as a routing reload lifecycle listener so BeforeReload stops capture/listener and AfterReload reconstructs it from the new config. Ensure every error path calls Stop and leaves no nftables/ip state. Keep explicit proxy listener behavior unchanged when gateway is disabled.

- [ ] Step 4: Run application tests.

Run: go test ./internal/app ./internal/gateway -count=1

Expected: PASS.

- [ ] Step 5: Commit.

git add service/base/internal/gateway service/base/internal/app/app.go service/base/internal/app/routing_controller.go
git commit -m "feat(app): wire transparent gateway lifecycle"

### Task 5: Add observability, docs, and Linux smoke validation

**Files:**
- Modify: service/base/internal/monitor/server.go
- Modify: service/base/frontend/src/App.tsx
- Create: scripts/validate-transparent-gateway.ps1
- Create: scripts/validate-transparent-gateway-linux.sh
- Modify: service/base/README.md
- Create: docs/transparent-gateway.md

- [ ] Step 1: Add status/API tests.

Expose GET /api/gateway/status with enabled/listen/applied, configured ingress interfaces/patterns/CIDRs, active connections, last error, and counters for DIRECT, PROXY, and DIRECT fallback due to unavailable nodes. Keep the endpoint authenticated and do not allow unauthenticated edits.

- [ ] Step 2: Add a compact console status view.

Add a gateway section to the existing routing/status screen showing disabled, active, or error, the fail-open policy, and recent decision counters. Do not add a second node pool or overlay-specific controls.

- [ ] Step 3: Document deployment boundaries.

Document host networking or an equivalent namespace, NET_ADMIN, IP_TRANSPARENT, forwarding, policy routing, trusted CIDRs, overlay adapters as host/DSM services, and that Docker bridge mode alone is not a gateway. State UDP/IPv6 are Phase 2 and DoH/DoT can bypass DNS interception. Include rollback commands and a cleanup verification.

- [ ] Step 4: Add smoke scripts.

The PowerShell script validates config parsing and API status against a running explicit-proxy instance without changing host networking. The Linux script checks ip/nft/CAP_NET_ADMIN/forwarding, launches an isolated gateway instance, tests physical-LAN and overlay sources, verifies private CIDRs bypass, verifies google.com through a healthy node, disables all nodes, and verifies the same flow succeeds DIRECT with a fallback counter.

- [ ] Step 5: Run full verification.

Run:
go test ./...
git diff --check

On a disposable Linux/NAS host only:
./scripts/validate-transparent-gateway-linux.sh --config /etc/easy-proxy/config.yaml

Expected: Go tests and diff checks pass on all platforms; Linux smoke reports each acceptance criterion and restores host forwarding/routing state after cleanup.

- [ ] Step 6: Commit and push.

git add service/base/internal/monitor/server.go service/base/frontend/src/App.tsx scripts docs/transparent-gateway.md service/base/README.md
git commit -m "feat: document and verify transparent gateway"
git push origin main

## Scope Gaps Carried Forward

- UDP transparent proxying, UDP ASSOCIATE, QUIC/WebRTC handling, and IPv6 capture remain Phase 2 and must not be represented as supported by Phase 1 status.
- Tailscale and 星空组网 adapters remain deployment-layer integrations; EasyProxy code only consumes interfaces, CIDRs, source addresses, and routes.
- Route selection between redundant overlays and multi-NAS failover remain Phase 3.
