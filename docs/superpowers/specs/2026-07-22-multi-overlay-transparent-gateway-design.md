# Multi-Overlay Transparent Gateway Design

## Status

Approved design baseline for implementation planning. This document defines the
network and runtime boundaries; it does not change the current EasyProxy
container or any LAN router.

## Goal

Deploy EasyProxy on the NAS as a shared transparent gateway for machines that
reach the NAS through any trusted network fabric, including:

- the physical LAN;
- Tailscale;
- 星空组网;
- future virtual-LAN providers.

Clients select the NAS as their network exit point. They do not run FlClash,
sing-box, mihomo, or a local proxy-node pool. A virtual-LAN client may still be
required to reach the NAS, but it is only the transport layer.

The NAS gateway must remain usable when one overlay is unavailable. EasyProxy
must not import or depend on a Tailscale or 星空组网 API, DNS service, or
interface-specific control plane.

## Non-Goals

- Changing the existing physical router to OpenWrt.
- Making PAC the server-side routing engine.
- Requiring every client to configure an HTTP proxy URL.
- Treating Tailscale as the only supported network fabric.
- Introducing a second independent node pool outside EasyProxy.
- Blocking all traffic when the proxy pool is temporarily empty.

## Core Invariants

1. Overlay networks only deliver packets to the NAS. EasyProxy consumes a
   generic ingress contract based on interfaces, source CIDRs, and routes.
2. The transparent gateway captures packets before any overlay-specific NAT
   hides the client identity.
3. EasyProxy owns DIRECT/PROXY rules, node selection, health, blacklist, and
   runtime statistics.
4. Local/private traffic and gateway/control-plane traffic bypass the proxy.
5. If no usable proxy node exists, the selected fail-open policy sends the flow
   DIRECT instead of making the client lose connectivity.
6. EasyProxy egress is marked and excluded from capture so the gateway cannot
   proxy itself recursively.

## Target Topology

```text
client devices
  |-- physical LAN
  |-- Tailscale
  `-- 星空组网 / future overlays
          |
          v
NAS network stack
  |-- overlay routing / exit-node adapters
  |-- policy routing and nftables marks
  `-- EasyProxy transparent Gateway
          |-- DNS interception and domain association
          |-- DIRECT/PROXY split
          |-- device identity resolution
          `-- EasyProxy pool and node health
                  |
                  v
              Internet
```

Tailscale may expose the NAS as an Exit Node. 星空组网 may expose the NAS as
its default route or policy-route target. These are deployment adapters, not
EasyProxy runtime dependencies.

## Network Ingress Contract

The gateway accepts trusted traffic from configured interfaces and CIDRs. The
configuration must support both exact names and patterns because virtual-LAN
interface names are not stable across products or hosts.

Illustrative configuration:

```yaml
gateway:
  enabled: true
  mode: transparent
  ingress:
    interfaces:
      - eth0
      - tailscale0
      - xingkong0
    interface_patterns:
      - ts-*
      - star-*
    trusted_cidrs:
      - 192.168.15.0/24
      - 100.64.0.0/10
      - 10.66.0.0/16
  capture:
    tcp: tproxy
    udp: tproxy
    preserve_original_destination: true
  routing:
    final_policy: PROXY
    no_available_proxy_policy: DIRECT
  dns:
    enabled: true
    listen: 0.0.0.0:53
```

The names above are examples. The implementation must not assume that
`tailscale0` or `xingkong0` exists.

## Traffic Processing

### TCP

1. A client sends a TCP flow through its selected overlay or physical route.
2. Linux policy routing sends the flow to the EasyProxy transparent listener.
3. The gateway preserves the original destination and source identity.
4. EasyProxy evaluates local, domain, IP, country, and final rules.
5. `DIRECT` dials the destination directly from the NAS.
6. `PROXY` selects a healthy EasyProxy node and dials through the existing pool.
7. If the pool has no usable node, `no_available_proxy_policy: DIRECT` applies.
8. The flow is recorded with the ingress interface, device ID, decision, and
   selected node when applicable.

The existing HTTP/CONNECT/SOCKS5 dispatcher remains available for explicit
proxy clients. Transparent TCP is a separate ingress path that feeds the same
routing and pool logic.

### UDP

Packets may reach the NAS through an Exit Node, but the current EasyProxy
SOCKS5 implementation is CONNECT-only and does not provide UDP ASSOCIATE.
The first gateway implementation therefore makes UDP policy explicit:

- supported transparent UDP flows use the new UDP path;
- unsupported UDP flows follow the configured fallback, initially DIRECT;
- QUIC/HTTP3 may be disabled by policy to allow browser TCP fallback;
- future UDP proxying must use a real UDP-capable EasyProxy gateway or node
  transport, not a fake TCP wrapper.

This is a capability boundary, not an overlay limitation.

### DNS

DNS is a routing input, not the mechanism that sends packets to the NAS.
The gateway may intercept UDP/TCP port 53 from trusted ingress interfaces and
serve a common resolver. Tailscale MagicDNS and 星空组网 DNS are optional
client conveniences, not required by EasyProxy.

The gateway must account for DoH/DoT bypass, IPv6 DNS, and DNS traffic from the
NAS itself. Private and overlay names must remain DIRECT.

## Device Identity

A device can have several addresses:

```text
physical LAN: 192.168.15.100
Tailscale:    100.64.0.20
星空组网:      10.66.0.20
```

Device identity is therefore a resource with multiple address bindings, not a
single Tailscale IP. The registry must support:

```yaml
devices:
  laptop:
    addresses:
      - 192.168.15.100
      - 100.64.0.20
      - 10.66.0.20
```

Resolution precedence:

1. an explicit authenticated device identity when an explicit proxy path is
   used;
2. an exact ingress-interface/source-address mapping;
3. a trusted CIDR mapping;
4. a shared default device policy.

If an overlay NATs all peers to one source address, the adapter must preserve
peer identity or the gateway must treat the source as one shared device. The
core must not assume that IP mapping always distinguishes clients.

## Overlay Adapters

Each overlay adapter has only four responsibilities:

1. join or configure the virtual network;
2. make the NAS reachable as an exit/default-route target;
3. expose health and route state to an optional client-side selector;
4. install or remove provider-specific routes without changing EasyProxy rules.

The Tailscale adapter may advertise an Exit Node. The 星空组网 adapter may
install an equivalent default route using that product's mechanism. A future
adapter follows the same contract.

Automatic failover is separate from proxy node selection:

```text
network selector: choose Tailscale or 星空组网 path to the NAS
EasyProxy pool:   choose the external proxy node after reaching the NAS
```

The first version supports manual overlay selection. An optional lightweight
route selector can later adjust route metrics after checking a gateway health
endpoint. It is not a proxy client and does not own node selection.

## Routing and Loop Prevention

The NAS must enable forwarding and use policy routing/nftables marks. Rules
must explicitly bypass:

- loopback and private LAN destinations;
- all trusted overlay control and peer ranges;
- NAS management and DNS control traffic;
- Tailscale and 星空组网 control/relay endpoints;
- EasyProxy process egress and upstream node addresses.

The transparent listener must never capture its own outbound pool connections.
A watchdog must remove or disable capture rules before stopping the gateway so a
container restart cannot leave a dead redirect that blackholes the NAS.

## Failure Policy

The selected policy is fail-open:

```text
usable node exists -> PROXY according to EasyProxy rules
no usable node       -> DIRECT
gateway unavailable  -> restore normal forwarding/route state
overlay A unavailable -> use overlay B when a route selector is enabled
```

This prioritizes connectivity. The UI and API must expose when a flow used the
DIRECT fallback because the proxy pool was empty. Per-device fail-closed can
be added later, but it is not the default for this deployment.

The NAS remains a single gateway failure domain. Redundant overlays protect the
client-to-NAS path, not a failed NAS or failed EasyProxy process. Multiple NAS
gateways are a separate high-availability feature.

## Security Boundary

- Only configured trusted interfaces/CIDRs may enter the transparent gateway.
- Ports `22323` and `29888` must be reachable only from trusted LAN/Tailnet
  sources, never from WAN.
- The management API and Web Console remain authenticated.
- Overlay membership and ACLs provide the first client admission layer.
- Device mappings are routing identity, not a substitute for authentication.
- DNS interception must not expose the management surface.
- Gateway and EasyProxy logs must redact credentials and proxy URLs.

## Deployment Shape on NAS

The NAS deployment should contain:

1. EasyProxy container with host or equivalent networking for transparent
   capture, persistent data, and the management port.
2. Tailscale host/package or a host-network container with `/dev/net/tun` and
   the required network capabilities.
3. 星空组网 host/package or equivalent host-network integration.
4. A controlled nftables/policy-routing supervisor owned by the Gateway
   lifecycle, not ad-hoc commands in a container entrypoint.

The Docker bridge network must not be treated as the LAN gateway. Transparent
capture requires host networking or a deliberately configured network namespace
with forwarding, TPROXY, and route-mark capabilities.

## Phased Implementation

### Phase 1: Native TCP Gateway

- add generic interface/CIDR ingress configuration;
- add Linux TPROXY/policy-routing lifecycle;
- preserve original TCP destination;
- feed DIRECT/PROXY decisions into the current routing and pool engine;
- implement fail-open DIRECT fallback;
- add device address aliases across multiple overlays;
- add DNS interception and loop exclusions;
- validate on physical LAN, Tailscale, and 星空组网 paths.

### Phase 2: UDP and IPv6

- implement UDP transparent handling with an explicit supported transport;
- validate QUIC, DNS, WebRTC-like UDP, and ordinary TCP fallback;
- enable IPv6 forwarding and equivalent capture rules;
- expose per-protocol fallback decisions in the UI and API.

### Phase 3: Route Selection and High Availability

- add optional client route-selector scripts/service;
- health-check NAS through each overlay;
- switch route metrics without modifying EasyProxy policy;
- add a second NAS gateway only if gateway-level HA is required.

## Acceptance Criteria

The design is implemented only when all of the following are true:

1. A physical-LAN client can use the NAS as its transparent gateway without a
   local proxy-node application.
2. A Tailscale client can select the NAS as Exit Node and reach the same
   EasyProxy routing behavior.
3. A 星空组网 client can use the NAS through the same EasyProxy core without
   any Tailscale-specific code path.
4. Switching off one overlay does not require an EasyProxy configuration
   change.
5. Local/private traffic bypasses the proxy and does not loop.
6. A missing proxy node produces a visible DIRECT fallback rather than a
   connectivity outage.
7. Proxy decisions expose ingress interface, device identity, DIRECT/PROXY
   result, fallback reason, and selected node where applicable.
8. Management access remains restricted to trusted network sources.

