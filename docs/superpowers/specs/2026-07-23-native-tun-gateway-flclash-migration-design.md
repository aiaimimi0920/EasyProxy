# Native TUN Gateway and FlClash Migration Design

**Date:** 2026-07-23

**Status:** Approved architecture, pending written-spec review

## Summary

Upgrade the existing EasyProxy deployment at `192.168.15.201` from an IPv4
TCP-only TPROXY gateway to an EasyProxy-native TUN gateway. The upgrade reuses
the current Debian 12 VM and the sing-box runtime already embedded in
EasyProxy. It does not create another VM, introduce a second node pool, or bind
the gateway to Tailscale, StarVPN, or another overlay vendor.

The same change set provides a controlled FlClash migration path. The importer
will preserve the existing EasyProxy runtime, add only missing nodes, translate
compatible routing rules, and validate migrated nodes before enabling them.

## Decision

Use the existing `github.com/sagernet/sing-box` and `sing-tun` dependencies as
the packet-processing implementation inside the EasyProxy process. EasyProxy
continues to own:

- configuration and lifecycle;
- trusted ingress selection;
- DIRECT/PROXY routing decisions;
- proxy-node selection, health, blacklist, and statistics;
- DNS and fake-IP policy;
- management API and console status;
- rollback and fail-open behavior.

sing-box supplies the mature TUN, gVisor/system networking, DNS, and protocol
adapters. It is an implementation library, not a separately operated proxy
product. The target deployment remains one EasyProxy container and one
EasyProxy control plane.

## Current Baseline

The deployed gateway is:

```text
Host:              Debian 12 VM on Synology VMM
Address:           192.168.15.201
Container:         easy-proxy-gateway
Network:           host
Explicit proxy:    22323/tcp
Management:        29888/tcp
Transparent input: 15001/tcp
Capture:           IPv4 TCP TPROXY
UDP:               forwarded DIRECT
IPv6:              not transparently captured
Failure policy:    no usable proxy -> DIRECT
```

The existing implementation already includes:

- `sing-box v1.12.25`;
- `sing-tun v0.7.13`;
- the sing-box gVisor dependency;
- a custom EasyProxy pool outbound with TCP and UDP support;
- a smart routing engine and session/node-selection directives;
- a Linux gateway supervisor that owns policy-routing and nftables state;
- authenticated gateway status in the management API and console.

The running EasyProxy API was rechecked before this design was written:

```text
Total nodes:     70
Available nodes: 53
SS:              56
VLESS:           10
Hysteria2:        2
Trojan:           1
HTTP:             1
```

## Capability Definition

For this project, "full-protocol TUN gateway" means:

- IPv4 TCP through DIRECT or PROXY;
- IPv4 UDP through DIRECT or a UDP-capable proxy node;
- IPv6 TCP through DIRECT or PROXY;
- IPv6 UDP through DIRECT or a UDP-capable proxy node;
- TCP and UDP DNS interception for trusted clients;
- fake-IP domain association for clients that use the gateway DNS service;
- QUIC/HTTP3, STUN, WebRTC-like UDP, and ordinary game UDP sessions;
- private/LAN/overlay destinations bypassing the external proxy pool;
- per-flow fail-open DIRECT when no suitable proxy node is available.

TUN capture does not make every IP protocol proxyable. ICMP, ESP, GRE, and
other non-TCP/UDP protocols use an explicit DIRECT or reject policy. They must
not be reported as externally proxied unless a future outbound supports them.

## Goals

1. Keep the existing VM at `192.168.15.201`; do not create a second VM.
2. Make client applications independent of system-proxy support.
3. Proxy eligible TCP and UDP traffic from physical and virtual LAN clients.
4. Support IPv4 and IPv6 without an unobserved IPv6 bypass.
5. Preserve EasyProxy routing, node health, statistics, and DIRECT fallback.
6. Keep the gateway independent of overlay control-plane APIs.
7. Preserve the explicit mixed proxy and management console.
8. Import missing FlClash nodes without duplicating existing EasyProxy nodes.
9. Translate compatible FlClash rules without copying unrelated UI state.
10. Make startup, reload, stop, failure, and rollback deterministic.

## Non-Goals

- Replacing the physical router with OpenWrt.
- Running a second independent mihomo or sing-box node pool.
- Copying the complete FlClash generated configuration into EasyProxy.
- Reproducing every FlClash proxy-group UI concept one-for-one.
- Automatically controlling Tailscale, StarVPN, or another overlay product.
- Proxying arbitrary non-TCP/UDP IP protocols through incompatible nodes.
- Retiring the DSM EasyProxy instance at `192.168.15.200` in this change.
- Adding gateway-level high availability or a second NAS.

## Target Topology

```text
physical-LAN and overlay clients
  -> client default route or selected overlay exit route
  -> Debian VM 192.168.15.201
  -> trusted-ingress nftables classification
  -> EasyProxy-owned policy-routing mark
  -> easyproxy0 TUN interface
  -> sing-box TUN inbound inside EasyProxy
  -> EasyProxy route outbound
       -> DIRECT outbound, or
       -> EasyProxy proxy-pool outbound
  -> Internet
```

DNS follows the same ownership boundary:

```text
client UDP/TCP :53
  -> EasyProxy DNS listener
  -> private/overlay DNS bypass or public resolver policy
  -> fake-IP mapping when enabled
  -> later TUN flow recovers the original domain
  -> EasyProxy DIRECT/PROXY decision
```

## Runtime Components

### TUN Inbound

The builder adds a sing-box TUN inbound tagged `easyproxy-tun` when
`gateway.mode: tun` is enabled. The inbound owns `easyproxy0`, processes IPv4
and IPv6 packets, and supports TCP and UDP sessions.

The container receives `/dev/net/tun` and retains only the required network
capabilities. The TUN interface exists inside the host network namespace because
the container uses host networking.

### EasyProxy Route Outbound

A new EasyProxy-owned sing-box outbound is the final route for TUN traffic. It
implements both `DialContext` and `ListenPacket` and delegates decisions to the
existing routing engine.

For each TCP connection or UDP session it:

1. obtains source, destination, protocol, ingress, and recovered domain;
2. resolves the EasyProxy device/profile identity;
3. evaluates local, custom, provider, GeoIP, and final rules;
4. sends DIRECT decisions to the sing-box direct outbound;
5. sends PROXY decisions to the existing EasyProxy pool outbound;
6. passes selection directives so stable/session/filter behavior is preserved;
7. applies `no_available_proxy_policy: DIRECT` when no suitable member exists;
8. records the decision, fallback reason, selected node, and byte counters.

This outbound prevents the TUN path from bypassing EasyProxy's smart-routing
engine or creating a parallel node scheduler.

Unlike the current explicit-proxy builder, an enabled TUN gateway must be able
to start with zero valid proxy nodes. In that state EasyProxy builds a
direct-only sing-box runtime, keeps TUN and DNS available, records PROXY
decisions as no-node fallbacks, and sends them DIRECT. A later source refresh
may add the pool and proxy outbounds through the normal candidate-runtime reload
without replacing the gateway with an unrelated process.

### Gateway Supervisor

The existing Linux supervisor gains a TUN mode. `transparent` and `tun` are
mutually exclusive; the same trusted flow must never enter both paths.

TUN mode owns:

- trusted-interface and trusted-CIDR packet classification;
- IPv4 and IPv6 packet marks;
- dedicated policy-routing tables whose default route is `easyproxy0`;
- explicit bypasses for local, private, overlay, control, management, and node
  endpoint destinations;
- an EasyProxy egress mark that bypasses TUN capture;
- idempotent apply, verification, reload, and cleanup;
- cleanup-first recovery after partial startup or reload failure.

The VM's base forwarding/NAT table remains separate. If EasyProxy cannot start,
the supervisor removes TUN capture state so the VM can return to ordinary
DIRECT forwarding instead of blackholing client traffic.

## Configuration Model

The existing `gateway` section remains the public entry point. The TPROXY
fields remain valid for `mode: transparent`; a new `tun` section is valid for
`mode: tun`.

Illustrative target configuration:

```yaml
gateway:
  enabled: true
  mode: tun
  ingress:
    interfaces:
      - ens3
    interface_patterns:
      - tailscale*
      - star*
    trusted_cidrs:
      - 192.168.15.0/24
      - 100.64.0.0/10
      - fd00:15::/64
  routing:
    final_policy: PROXY
    no_available_proxy_policy: DIRECT
  dns:
    enabled: true
    listen: 0.0.0.0:53
  tun:
    interface_name: easyproxy0
    addresses:
      - 172.31.255.1/30
      - fd31:255::1/126
    stack: mixed
    mtu: 1500
    ipv4: true
    ipv6: true
    udp: true
    strict_route: true
    dns_hijack: true
    fake_ip: true
    fake_ipv4_range: 198.18.0.0/16
    fake_ipv6_range: fc00::/18
  devices: {}
```

The supported stack values are `system`, `gvisor`, and `mixed`. The deployment
default is `mixed`; a test-only override may compare gVisor and system behavior
without changing the public routing model.

`addresses` are the private L3 addresses assigned to the TUN interface. They
must not overlap trusted client CIDRs, LAN/overlay ranges, fake-IP ranges, or
the VM's existing addresses. The supervisor derives the policy route from the
same address family and does not use the TUN address as a client gateway
address; clients still route to `192.168.15.201` or their overlay exit path.

EasyProxy owns policy routes explicitly rather than delegating forwarded-client
routes to sing-box `auto_route`. This keeps trusted-ingress filtering and
rollback under the existing gateway supervisor. Configuration validation must
reject:

- an enabled TUN gateway without IPv4 or IPv6;
- invalid or overlapping fake-IP ranges;
- fake-IP ranges that overlap configured trusted or private networks;
- a TUN interface name that conflicts with an existing non-EasyProxy interface;
- simultaneous TPROXY and TUN capture;
- DNS hijack with no active gateway DNS listener;
- invalid stack, MTU, policy, interface pattern, or CIDR values.

## DNS and Domain Association

The gateway DNS service must support UDP and TCP port 53. Trusted ingress DNS
is redirected to this listener before ordinary TUN classification.

Public queries may return fake-IP addresses so a later IP flow retains the
original domain for rule matching. The fake-IP cache is persistent under the
EasyProxy data directory and is included in lifecycle reloads. Private,
overlay, mDNS, and explicitly bypassed suffixes use real answers and DIRECT
resolution.

The DNS path must avoid dependency loops:

- proxy-node endpoint names resolve without using the proxy pool;
- resolver endpoint names resolve through the local/bootstrap resolver;
- public client DNS may use the configured encrypted remote resolver;
- a proxy-pool outage does not prevent bootstrap or private DNS resolution.

DoH and DoT cannot be universally redirected by a port-53 rule. Their traffic
still enters TUN and follows ordinary domain/IP policy. Applications with
certificate-pinned private DNS may bypass fake-IP domain association, so IP,
GeoIP, and final rules remain required.

## UDP Behavior

The current EasyProxy pool outbound already exposes a UDP packet path. TUN mode
uses that path only for nodes whose sing-box outbound supports UDP.

Decision behavior is:

```text
DIRECT rule                 -> direct UDP socket
PROXY + UDP-capable node    -> proxy UDP through selected node
PROXY + no capable node     -> DIRECT when fail-open is configured
gateway/process unavailable -> remove capture and restore DIRECT forwarding
```

The pool must filter candidates by requested network before selection. A TCP
healthy node that cannot create a UDP packet connection must not be reported as
a successful UDP route. UDP failures feed the existing health/statistics model
without incorrectly blacklisting a node for unrelated TCP traffic.

## IPv6 Behavior

IPv6 is enabled only after all of the following exist together:

- `net.ipv6.conf.all.forwarding=1` in the VM;
- IPv6 trusted-ingress and bypass rules;
- an IPv6 policy route to `easyproxy0`;
- IPv6 DIRECT egress from the VM or a proxy node that can reach the destination;
- IPv6 DNS behavior and fake-IP mapping;
- loop-prevention marks equivalent to IPv4.

If the deployment lacks usable IPv6 egress, the configuration must explicitly
disable IPv6 capture or reject it. It must not silently allow clients to bypass
the gateway through their previous IPv6 route.

## Loop Prevention

EasyProxy-generated outbound sockets receive a dedicated egress mark that is
different from the trusted-ingress capture mark. Policy rules send egress-marked
traffic to the normal main table and exclude it from TUN classification.

The supervisor also bypasses:

- loopback and the VM's own addresses;
- physical and overlay private destinations;
- management, explicit proxy, SSH, and DNS control traffic;
- configured resolver endpoints;
- all current proxy-node endpoint addresses;
- DHCP, NDP, multicast, and link-local traffic as appropriate.

Node endpoint bypasses are updated atomically on source refresh. A failed
update keeps the previous verified bypass set and does not partially replace
it.

## Lifecycle and Reload

Startup order:

1. validate configuration and host prerequisites;
2. build a candidate sing-box instance containing the TUN and route outbounds;
3. create and verify `easyproxy0` without capturing client traffic;
4. start DNS and TUN processing;
5. apply policy routes and nftables capture rules;
6. verify API readiness and mark the gateway active.

Reload order:

1. stop accepting new captured flows;
2. remove or suspend ingress capture rules;
3. drain existing sessions within the configured timeout;
4. build and start the candidate runtime;
5. atomically replace routes, bypasses, DNS state, and capture rules;
6. destroy the old runtime only after the candidate passes readiness checks.

Stop and failure paths remove capture before closing TUN. Cleanup is idempotent
and runs after process restart through a boot-time stale-state check.

## FlClash Inventory

The active local FlClash configuration was parsed from:

```text
C:\Users\vmjcv\AppData\Roaming\com.follow\clash\config.yaml
```

Current inventory:

```text
Nodes:          68
  SS:           56
  AnyTLS:       12
Proxy groups:   17
Rules:          1336
  DOMAIN-SUFFIX 1276
  DOMAIN-KEYWORD 56
  GEOIP         2
  DST-PORT      1
  MATCH         1
TUN:            gvisor, auto-route, DNS hijack any:53
DNS:            fake-ip, 198.18.0.1/16, IPv6 disabled
```

The live EasyProxy comparison is:

```text
Exact name overlap: 56
FlClash-only nodes:  12
EasyProxy-only nodes: 14
```

All 12 FlClash-only nodes are AnyTLS nodes named `TLS-L1-1` through
`TLS-L1-4`, `TLS-S1-1` through `TLS-S1-4`, and `TLS-T1-1` through
`TLS-T1-4`. EasyProxy already supports AnyTLS URI parsing and sing-box AnyTLS
outbounds.

## FlClash Migration Tool

Add a repository-level migration command that accepts an explicit FlClash
configuration path and defaults to dry-run. It must never discover and import
profiles silently.

The command produces a redacted report containing:

- source file and content hash;
- proxy counts by type;
- exact-name matches;
- canonical-identity matches;
- nodes to add, skip, or reject;
- supported, translated, and unsupported rules;
- proxy groups that can be mapped to EasyProxy filters or profiles;
- proposed TUN/DNS settings;
- warnings and validation failures.

No report, log, API response, or test artifact may contain node passwords,
UUIDs, full proxy URIs, subscription URLs, management credentials, or tokens.

### Node Identity and Import

Deduplication must not rely only on display names. The importer creates a
canonical identity from normalized protocol, endpoint, port, authentication,
TLS, transport, and plugin fields and compares a cryptographic fingerprint.
Secrets participate in the fingerprint but are never emitted.

Import behavior:

1. export and back up the current EasyProxy configured nodes;
2. parse FlClash YAML with the existing structured parser;
3. validate that each node can build a sing-box outbound;
4. classify duplicate, missing, and unsupported nodes;
5. add missing nodes initially disabled;
6. rebuild a candidate runtime without replacing the active runtime;
7. probe each imported node using the configured management targets;
8. perform an explicit Google HTTPS test through each candidate;
9. enable only candidates that pass acceptance checks;
10. preserve failed candidates as disabled with a redacted failure reason;
11. provide a rollback command that removes only nodes created by this import.

The initial migration is expected to add the 12 missing AnyTLS nodes and skip
the 56 SS nodes already represented in EasyProxy.

### Rule Translation

Supported mappings are:

```text
DOMAIN-SUFFIX  -> EasyProxy domain-suffix rule
DOMAIN-KEYWORD -> EasyProxy domain-keyword rule
GEOIP          -> EasyProxy GeoIP rule where the country code is supported
DST-PORT       -> EasyProxy destination-port rule
MATCH          -> routing.final_policy
```

The imported rule set is stored as a generated local rule file under the
EasyProxy data directory, not expanded into thousands of lines in the primary
operator configuration. `routing.rule_files` references one or more local
files, which are loaded before remote rule providers and before the final
policy. Generated files include source hash and import metadata but no proxy
credentials.

Rule ordering is preserved. Unknown Clash rule types are reported and skipped;
they are never silently converted to `MATCH` or another broader rule.

### Proxy Group Translation

FlClash proxy groups are not copied as a second scheduler. Translation is
limited to concepts that map to current EasyProxy behavior:

- `url-test` and `fallback` contribute health/availability intent;
- geographic groups contribute node-filter metadata;
- stable named groups may become optional EasyProxy routing profiles;
- nested group references are flattened only after cycle detection;
- UI-only selection state is not imported.

The existing EasyProxy pool remains authoritative. The first production import
does not require proxy-group parity before the missing nodes and rules can be
used.

## API and Console

Extend `GET /api/gateway/status` with:

- active mode (`transparent` or `tun`);
- TUN interface, stack, MTU, and readiness;
- IPv4, IPv6, TCP, UDP, and DNS capability flags;
- active TCP connections and UDP sessions;
- DIRECT, PROXY, no-node fallback, and unsupported-protocol counters;
- DNS queries, fake-IP entries, and domain-recovery counters;
- current ingress and egress marks without exposing rule internals;
- last apply, cleanup, reload, and validation errors.

Add an authenticated FlClash import preview endpoint only if the CLI and
existing node APIs cannot provide a complete dry-run workflow. Applying an
import remains an explicit operation and returns an import ID used for status
and rollback.

The console adds TUN status to the existing gateway view. It must not become a
second proxy-node management surface. Existing node management remains the
source of truth.

## Deployment Changes

The existing Debian compose deployment adds:

```yaml
devices:
  - /dev/net/tun:/dev/net/tun
```

It retains:

```yaml
network_mode: host
cap_add:
  - NET_ADMIN
  - NET_RAW
environment:
  EASY_PROXY_RUN_AS_ROOT: "1"
```

The bootstrap script validates `/dev/net/tun`, IPv4/IPv6 forwarding, nftables,
policy routing, and the configured interface name. It does not install capture
rules itself; the EasyProxy lifecycle supervisor remains their owner.

The existing TCP TPROXY deployment remains available as a rollback mode.
Switching back requires changing `gateway.mode`, restarting only the VM
EasyProxy container, and verifying that no TUN routes or interface remain.

## Security

- Only configured trusted interfaces and source CIDRs enter TUN capture.
- Management and explicit proxy ports remain restricted to trusted networks.
- Fake-IP and DNS listeners are not exposed to WAN.
- Imported FlClash credentials are never written to logs or reports.
- Runtime configuration, `.env`, databases, node URIs, and import backups are
  not committed to Git.
- Generated rule files are non-secret but remain runtime data unless explicitly
  sanitized for repository fixtures.
- The importer modifies neither the active FlClash file nor its profile store.
- Rollback removes only resources associated with the recorded import ID.

## Observability

Metrics and logs distinguish:

- TCP from UDP;
- IPv4 from IPv6;
- DIRECT from PROXY;
- rule-selected DIRECT from no-node DIRECT fallback;
- proxy node incapable of UDP from a general node failure;
- DNS fake-IP hit, real-IP answer, and domain-recovery miss;
- physical-LAN ingress from overlay ingress;
- TUN lifecycle errors from proxy-node errors.

Sensitive addresses may be truncated or hashed in aggregate views, but
operators must still be able to identify the configured device/profile and
selected policy.

## Verification Strategy

### Unit and Component Tests

- configuration defaults, validation, clone, and YAML round trips;
- TUN builder output for enabled and disabled modes;
- route outbound TCP and UDP decisions;
- UDP-capability filtering and DIRECT fallback;
- IPv4/IPv6 route and nftables command generation;
- cleanup after every partial supervisor failure point;
- DNS fake-IP allocation, persistence, expiry, and domain recovery;
- FlClash node conversion, canonical deduplication, and redaction;
- rule ordering, translation, unsupported-rule reporting, and local rule files;
- import rollback ownership and idempotency.

### Container Tests

- image contains TUN requirements and starts with `/dev/net/tun`;
- container fails clearly when required capabilities are absent;
- explicit proxy behavior remains unchanged when TUN is disabled;
- TUN and TPROXY cannot be active simultaneously;
- reload and stop leave no stale interface, route, rule, or nftables state.

### Isolated Linux Network Tests

Use network namespaces as independent clients with all application and system
proxy settings removed. Validate:

- IPv4 TCP Google HTTPS through PROXY;
- IPv4 UDP DNS through the gateway;
- QUIC/HTTP3 or another controlled UDP flow through a UDP-capable node;
- IPv6 TCP and UDP when deployment IPv6 is enabled;
- private LAN and overlay destinations remain DIRECT;
- DNS fake-IP response maps a later connection back to the original domain;
- disabling all nodes keeps connectivity through DIRECT and increments the
  no-node fallback counter;
- stopping EasyProxy restores base DIRECT forwarding and does not blackhole the
  client;
- restarting the VM restores the configured gateway without stale state.

### FlClash Migration Tests

- dry-run reports 68 FlClash nodes, 56 overlaps, and 12 additions for the
  current source file without exposing secrets;
- apply creates only the 12 missing AnyTLS nodes;
- existing 70 EasyProxy nodes and their statistics are preserved;
- failed AnyTLS candidates remain disabled;
- compatible rules retain source order;
- rollback removes only nodes and rule files from the recorded import;
- FlClash files remain byte-for-byte unchanged.

## Rollout Sequence

1. Add configuration and rule-file support with the gateway still in TPROXY
   mode.
2. Add the EasyProxy route outbound with TCP/UDP component tests.
3. Add sing-box TUN inbound construction and Linux supervisor TUN mode.
4. Add DNS/fake-IP and IPv6 support.
5. Update API, console, image, compose, bootstrap, and documentation.
6. Build and validate a new image in isolated Linux namespaces.
7. Run the FlClash importer in dry-run mode and review the redacted report.
8. Import and validate the 12 missing AnyTLS nodes without changing gateway
   mode.
9. Snapshot the Debian VM and back up EasyProxy runtime data.
10. Switch one isolated test client to TUN mode and complete acceptance tests.
11. Restart the container and VM, then repeat core TCP, UDP, DNS, IPv6, and
    DIRECT-fallback tests.
12. Move selected real clients only after isolated acceptance passes.

## Acceptance Criteria

The work is complete only when all of the following are demonstrated on the
actual Debian VM:

1. No second VM or independent node pool is introduced.
2. The EasyProxy container owns a healthy `easyproxy0` TUN interface.
3. A client with no local proxy software reaches Google over IPv4 TCP through
   an EasyProxy node.
4. A controlled UDP or QUIC flow uses an EasyProxy UDP-capable node.
5. DNS interception and fake-IP domain recovery affect routing as configured.
6. IPv6 TCP and UDP are either successfully captured or explicitly disabled
   with no bypass path.
7. Physical-LAN and at least one overlay path use the same provider-neutral
   gateway core.
8. Private and control-plane traffic remains DIRECT and loop-free.
9. With no usable proxy node, client connectivity remains available through
   visible DIRECT fallback.
10. Stopping or crashing EasyProxy removes capture state and does not blackhole
    the VM or clients.
11. Explicit proxy port `22323` and management port `29888` still work.
12. The importer adds only the 12 missing AnyTLS nodes, preserves existing
    EasyProxy state, and leaves FlClash unchanged.
13. Migrated nodes pass EasyProxy probes and a Google HTTPS validation before
    they are enabled.
14. Unit, Go, Python, container, namespace, reboot, and repository validation
    suites pass before the image is promoted.
