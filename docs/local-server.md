# EasyProxy Local Server 与设备策略

## 适用范围

本文按主题集中维护相关合同，按目录查阅；不包含后续阶段计划。

## 目录

- [设备配置与接口](#local-server)
- [三级节点选择策略](#device-selection-tiers)

<a id="local-server"></a>

## Local Server Device Profiles

Local Server turns one EasyProxy instance into a trusted-LAN proxy gateway for
multiple devices. Every client uses the same canonical username/password and
the same mixed proxy listener, while EasyProxy selects either the shared
forwarding Profile or a fully independent per-device Profile for each request.

No standalone client, device agent, browser extension, or companion daemon is
required. Configure the operating system, browser, command-line tool, or
application to use EasyProxy as a normal HTTP or SOCKS5 proxy.

### Topology

```text
trusted LAN clients
  |  HTTP proxy / HTTPS CONNECT / SOCKS5
  |  canonical username + password
  v
EasyProxy mixed listener :22323
  -> authenticate
  -> resolve explicit device_id, then IP/CIDR mapping, then shared fallback
  -> select independent Profile when present; otherwise select shared Profile
     |-> selected Profile disabled -> DIRECT
     |-> rule result DIRECT        -> local network dial
     `-> rule result PROXY         -> shared global proxy pool

Web Console and management API :29888
  -> same canonical credential
  -> shared Profile, devices, independent Profiles, and IP mappings

Global runtime state
  -> MiSub, subscriptions, connectors, nodes, health, blacklist, and statistics
```

Profiles change routing and node-selection policy; they do not create separate
node pools. MiSub, subscription refresh, connectors, node health, failure
feedback, and blacklist state remain global.

The standard ports are:

- `22323/tcp`: one mixed HTTP/CONNECT/SOCKS5 proxy entry.
- `29888/tcp`: embedded Web Console and management API.

### Device selection tiers

Profiles support two optional country/latency/stability priorities and a fixed
final general-pool fallback. See [Device selection tiers](local-server.md#device-selection-tiers)
for metrics, bounded retries, hard-filter compatibility, and TUN/transparent
source-address resolution. The final proxy-pool tier is separate from DIRECT
routing and the gateway's existing fail-open setting.

### Shared And Independent Profiles

The shared Profile is stored in the normal YAML `routing` block. An independent
Profile is stored in SQLite and is a complete snapshot owned by one device ID.
It is not an overlay and does not inherit later shared changes.

| Independent Profile | Selected Profile enabled | Shared enabled | Effective behavior |
| --- | --- | --- | --- |
| Absent | No | No | `DIRECT` |
| Absent | Yes | Yes | Apply shared rules and selection policy |
| Present | No | Either | `DIRECT` |
| Present | Yes | No | Apply the independent Profile |
| Present | Yes | Yes | Apply the independent Profile |

Important lifecycle rules:

- `copy-shared` creates a one-time independent snapshot at revision 1.
- Editing the shared Profile never mutates an independent Profile.
- Disabling an independent Profile forces that device to `DIRECT`; it does not
  fall through to the shared Profile.
- Deleting an independent Profile keeps the device resource and IP mappings,
  then returns the device to the shared Profile.
- If the selected Profile is disabled, request-level `nosplit`, path/header
  overrides, and `final_policy: PROXY` cannot force the request through the
  pool.
- If the pool is empty or idle, the listener remains available: `DIRECT`
  requests continue to work and `PROXY` requests fail explicitly.

### Canonical Credential

Local Server has one canonical credential under `local_server.auth`. The
runtime derives the proxy listener and management password from it, so the Web
Console, management API, HTTP proxy, HTTPS CONNECT, and SOCKS5 all authenticate
against the same pair.

Username rules:

- 1 to 64 ASCII characters.
- Letters, digits, `.`, `_`, and `-` only.
- The default is `easyproxy`.

Password rules:

- Required and non-empty while Local Server is enabled.
- At most 256 bytes.
- Must not contain NUL.
- Root render/deploy scripts reject known placeholder values such as
  `change_me_to_a_strong_shared_password`.

The password is write-only through the API. `GET /api/local-server/config`
returns `password_set`, never the password. Do not expect the Web Console to
refill the password field after a save or reload.

When Local Server is first enabled, or the canonical username/password changes,
the credential generation increments. Existing Web sessions from an older
generation become invalid. Rotating a credential while Local Server is already
enabled atomically publishes the new management and proxy credential without
rebinding `22323`; established relay connections may finish, while new requests
must use the new credential. Enabling/disabling Local Server or changing its
listen address still requires a topology reload.

### Device Resolution

Identity resolution has fixed precedence:

1. Explicit `device_id` in the authenticated proxy username.
2. Best matching enabled IP/CIDR mapping for the TCP peer address.
3. Shared fallback.

Use this proxy username form for an explicit device:

```text
easyproxy+dev=laptop
```

Device IDs are normalized to lower case and must be 1 to 64 characters using
only `a-z`, `0-9`, `.`, `_`, and `-`. If the explicit ID has no independent
Profile, the request still uses the shared Profile but remains observable as an
explicit identity. A valid explicit ID always overrides an IP mapping.

IP mappings are policy selectors, not authentication or authorization. All
clients still use the canonical shared credential, and an authenticated client
can claim another valid device ID. This is an intentional single-owner/trusted-
LAN boundary, not multi-tenant isolation.

#### Docker And NAT Warning

The resolver reads the actual TCP peer address. It does not trust
`X-Forwarded-For`, and it does not automatically learn mappings.

Docker bridge networking, published ports, NAT, reverse proxies, VPN gateways,
and some IPv6 privacy configurations can make several clients appear as the
same gateway address. In that case IP/CIDR mappings collapse onto one observed
source and cannot distinguish devices. `GET /api/local-server/status` reports
`peer_address_mode: tcp_peer` and includes the current source-IP warning.

Use explicit `+dev=<device_id>` identities whenever device selection must be
stable. Treat IP/CIDR mappings as best-effort fallback unless the deployed
network mode has been verified to preserve the original LAN source address.

### Configuration

#### Deployment Topology And Runtime Config

`topology.yaml` selects the local component, install mode, access mode, and
release channel. It never owns listeners, credentials, routing rules, devices,
or Profiles. Those fields live in the selected runtime configuration file.
The root `deploy-host.ps1` defaults to `easyproxy-runtime.yaml` relative to its
launcher root; the repository helper `scripts/deploy-easyproxy.ps1` defaults to
`deploy/service/base/config.yaml`. `-RuntimeConfigPath` selects a different
file. The selected file is initialized when missing and preserved across
ordinary deploys and updates.

For the repository-helper default path, initialize and deploy from the repository root:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\init-runtime-config.ps1
powershell -ExecutionPolicy Bypass -File .\scripts\deploy-subproject.ps1 -Project easyproxy
```

Web Console changes persist to the same runtime file. Replace every placeholder
credential before exposing the listener to a trusted LAN.
Official Docker deployments mount that host file at the writable container path
`/var/lib/easyproxy/config/config.yaml`; the image's `/etc/easyproxy/config.yaml`
is bootstrap-only.

#### Runtime Config Example

For the root deployment wrapper, a directly launched binary, or a service-level
Docker mount, use the direct service schema:

```yaml
mode: pool

listener:
  address: 0.0.0.0
  port: 22323
  protocol: mixed
  username: ""
  password: ""

routing:
  enabled: true
  listen: ""
  default_strategy: stable
  use_default_rules: true
  final_policy: PROXY
  rules: []
  rule_providers: []
  long_lived:
    min_uptime: 2h
    min_success_rate: 0.9
  session:
    ttl: 10m

local_server:
  enabled: true
  listen: ""
  auth:
    username: easyproxy
    password: "change_me_to_a_strong_shared_password"

management:
  enabled: true
  listen: 0.0.0.0:29888
  password: ""
```

Replace the placeholder password before launching the binary or container. In
Local Server mode that canonical password is synchronized to management auth.
Outside Local Server mode, an empty management password is allowed only on a
loopback listener; wildcard, LAN, and other non-loopback addresses fail closed.

Local Server enabled mode rejects:

- `mode: multi-port` or `mode: hybrid`;
- any `extra_listeners`;
- a listener protocol other than `mixed`;
- conflicting non-empty `local_server.listen` and `routing.listen` values.

To use legacy multi-port, hybrid, extra-listener, or route-B behavior, set
`local_server.enabled: false`. Legacy Smart Routing remains available in that
mode through the `routing` configuration described in
[`routing.md#smart-routing`](./routing.md#smart-routing).

### Client Examples

The examples below use an explicit `laptop` device ID. Remove `+dev=laptop` to
allow IP mapping or shared fallback resolution.

#### Plain HTTP Proxy

An HTTP URL sent through an HTTP proxy uses absolute-form requests:

```bash
curl --proxy http://192.168.10.20:22323 \
  --proxy-user 'easyproxy+dev=laptop:replace-with-the-real-password' \
  http://example.com/
```

#### HTTPS CONNECT

An HTTPS target through the HTTP proxy causes `curl` to establish a CONNECT
tunnel:

```bash
curl --proxy http://192.168.10.20:22323 \
  --proxy-user 'easyproxy+dev=laptop:replace-with-the-real-password' \
  https://example.com/
```

#### SOCKS5

Use `socks5h` when DNS should be resolved through the proxy path:

```bash
curl --proxy socks5h://192.168.10.20:22323 \
  --proxy-user 'easyproxy+dev=laptop:replace-with-the-real-password' \
  https://example.com/
```

HTTP, CONNECT, and SOCKS5 parse the same canonical credential and explicit
device syntax. HTTP keep-alive requests resolve the current Registry per
request. A SOCKS5 CONNECT relay keeps the identity selected during its
authentication handshake; a new connection reads the current Registry.

### Web Console

Open `http://<easyproxy-host>:29888/`, sign in with the canonical username and
password, then open `#devices` (the **Device Profiles** / `设备策略` tab).

The page provides:

- shared Profile summary and editing;
- device creation and display-name editing;
- independent Profile creation from an empty Profile or a shared snapshot;
- independent Profile edit, enable/disable, and delete-to-shared behavior;
- IP/CIDR mapping create, update, and delete;
- effective mode, enabled state, revisions, last-seen data, and the source-IP
  warning.

Unsaved Profile edits are guarded when changing devices, tabs, or browser hash
routes. A `409` conflict does not overwrite the local form; reload the server
version or discard the local edit explicitly.

### Management API And CAS

#### Authentication

The Web Console uses `POST /api/auth` to create a session cookie/token:

```bash
curl -c easyproxy.cookies \
  -H 'Content-Type: application/json' \
  -d '{"username":"easyproxy","password":"replace-with-the-real-password"}' \
  http://192.168.10.20:29888/api/auth

curl -b easyproxy.cookies \
  http://192.168.10.20:29888/api/local-server/status
```

Service-to-service management calls may use canonical HTTP Basic auth:

```bash
curl --user 'easyproxy:replace-with-the-real-password' \
  http://192.168.10.20:29888/api/local-server/status
```

`Proxy-Authorization` is intentionally ignored by management APIs; proxy
authentication cannot impersonate management authentication.

#### Resource Paths

```text
GET /api/local-server/status
GET /api/local-server/config
PUT /api/local-server/config

GET /api/local-server/profiles/shared
PUT /api/local-server/profiles/shared

GET  /api/local-server/devices
GET  /api/local-server/devices/{device_id}
PUT  /api/local-server/devices/{device_id}
PUT  /api/local-server/devices/{device_id}/profile
PATCH /api/local-server/devices/{device_id}/profile/enabled
POST /api/local-server/devices/{device_id}/profile/copy-shared
DELETE /api/local-server/devices/{device_id}/profile

GET    /api/local-server/ip-mappings
POST   /api/local-server/ip-mappings
PUT    /api/local-server/ip-mappings/{mapping_id}
DELETE /api/local-server/ip-mappings/{mapping_id}
```

The legacy `/api/routing/config` and `/api/routing/status` paths are shared-
Profile compatibility aliases. New device-aware integrations should use the
`/api/local-server/*` resources.

#### Revision And CAS Example

Profile, device, and mapping writes use optimistic concurrency. Create with
`If-None-Match: *` / `expected_revision: 0`; update or delete with
`If-Match: "<revision>"` and the same `expected_revision` in the body.

Create a device and copy the current shared Profile:

```bash
curl --user 'easyproxy:replace-with-the-real-password' \
  -X PUT -H 'Content-Type: application/json' -H 'If-None-Match: *' \
  -d '{"expected_revision":0,"display_name":"Laptop"}' \
  http://192.168.10.20:29888/api/local-server/devices/laptop

curl --user 'easyproxy:replace-with-the-real-password' \
  -X POST \
  http://192.168.10.20:29888/api/local-server/devices/laptop/profile/copy-shared
```

Update the independent Profile using the revision returned by `GET` or the
previous mutation response:

```bash
curl --user 'easyproxy:replace-with-the-real-password' \
  -X PUT -H 'Content-Type: application/json' -H 'If-Match: "1"' \
  -d '{
    "expected_revision": 1,
    "profile": {
      "schema_version": 1,
      "enabled": true,
      "default_strategy": "stable",
      "use_default_rules": true,
      "final_policy": "PROXY",
      "rules": [],
      "rule_providers": [],
      "node_filter": {"countries": [], "regions": [], "long_lived": null},
      "long_lived": {"min_uptime": "2h", "min_success_rate": 0.9},
      "session": {"ttl": "10m"}
    }
  }' \
  http://192.168.10.20:29888/api/local-server/devices/laptop/profile
```

A stale write returns `409` and the current server revision without modifying
the active Profile:

```json
{
  "error": "revision_conflict",
  "current_revision": 2
}
```

Successful mutations include `revision`, `registry_revision`, `need_reload`,
and a resource summary. Full Profile updates are complete replacements; there
is no implicit JSON merge. Only the dedicated `/profile/enabled` endpoint is a
partial Profile mutation.

### Network Security Boundary

Local Server is designed for a trusted private LAN. The shared credential does
not provide per-device authorization, and the standard proxy and management
ports are plain TCP/HTTP unless the operator adds a secure transport.

At the router and host firewall:

1. Allow `22323/tcp` and `29888/tcp` only from the trusted LAN subnet(s).
2. Deny both ports from WAN interfaces, port forwarding, public cloud security
   groups, guest Wi-Fi/VLANs, IoT VLANs, and other untrusted segments.
3. Do not publish either port through UPnP or an Internet-facing reverse proxy.
4. Keep the host firewall default inbound policy blocked and add only scoped
   allow rules. On Windows, for example, an allow rule should set
   `-RemoteAddress` to the trusted subnet rather than `Any`.

For access across an untrusted network, first enter the trusted network through
a VPN such as WireGuard/Tailscale, or place the management surface behind a TLS
reverse proxy with an explicit access policy. Do not send the canonical
credential over untrusted plaintext networks. A TLS reverse proxy must also be
configured carefully if it fronts proxy traffic; IP mapping remains based on
the TCP peer and does not trust forwarded-address headers.

### Troubleshooting

| Symptom | Meaning and action |
| --- | --- |
| Management API returns `401` | The session is missing/expired, the Basic pair is wrong, or a credential rotation invalidated the old generation. Sign in again with the canonical pair. Do not use `Proxy-Authorization` for management calls. |
| Proxy returns `407` | The HTTP/CONNECT credential is missing or wrong. Use the canonical base username and password; append `+dev=<id>` only after the valid base username. SOCKS5 reports an authentication failure instead of HTTP `407`. |
| Proxy returns `400` for a device username | The authenticated username has a malformed or repeated `dev=` token. Use one normalized ID containing only `a-z0-9._-`. |
| Mutation returns `409` | The revision is stale, a reload window is active, or canonical legacy credentials conflict during migration. Read the current resource/revision and retry intentionally; never overwrite blindly. |
| Mutation returns `422` | The Profile, device ID, username, password, listen address, provider, IP/CIDR, or topology is invalid. Correct the validation error before retrying. |
| Proxy returns `502` | The selected route could not dial. For `DIRECT`, check DNS/network reachability. For `PROXY`, check pool availability, filters, node health, connectors, and upstream reachability. |
| DIRECT works but PROXY returns `502` | The dispatcher is healthy but the pool is idle, empty, or has no node matching the Profile filter. Inspect `/api/local-server/status`, `/api/nodes`, `/api/source-sync/status`, and `/api/debug`; restore a usable node without restarting the dispatcher. |
| Several devices unexpectedly use one Profile | Docker/NAT/reverse proxying probably collapsed their observed source IP. Check `peer_address_mode` and `source_ip_warning`, inspect last-seen addresses, and switch clients to explicit `+dev=<device_id>`. |
| Independent Profile did not follow a shared edit | This is expected. Independent Profiles are snapshots with separate revisions. Delete the independent Profile to return to shared, or edit/copy it explicitly. |
| Password field is blank after refresh | This is expected redaction. `password_set: true` confirms a stored secret; enter a value only when rotating it. |

<a id="device-selection-tiers"></a>

## Device selection tiers

The local runtime supports two optional ordered node-selection tiers, followed
by the existing general proxy pool. This is a local policy feature: cloud
manifests, subscriptions, connectors, and their refresh contracts do not change.
There is one global pool, not one pool or container per device.

### Configuration and compatibility

Set `selection_tiers` on a shared or independent forwarding Profile. The shared
Profile persists as `routing.selection_tiers` in the runtime YAML. Independent
Profiles persist in SQLite as complete snapshots with their existing revisions.
The same editor serves both scopes; copying shared creates a one-time snapshot.

```yaml
routing:
  selection_tiers:
    - kind: country
      country: US
    - kind: latency
```

At most two configured tiers are accepted. Supported kinds are `country`,
`latency`, and `stability`. Country requires a two-letter country code, normalized
to uppercase. The other kinds reject a country value. Ordering is preserved.
The implicit final tier cannot be removed or configured. Disabling the first
configured tier in the editor promotes the second to first.

An omitted or empty chain retains legacy selection behavior. The older
`/api/routing/config` alias preserves a saved chain when old clients omit the
field; an explicit empty array clears it. Full Profile PUT remains a complete
replacement: omitting `selection_tiers` clears it, just like an explicit empty array.
Full Profile clients should GET the current definition, edit it, and PUT with
the current revision; stale revisions return 409. Validation precedes publication and existing revision/CAS conflict
handling still applies.

Existing `node_filter` fields, request-level country filters, strict required
node tags, authentication quarantine, and network/protocol support remain hard
constraints across every tier. Thus the general tier means all otherwise
eligible nodes in the global pool, not permission to escape a hard filter.
For country *preference*, use a country tier rather than a country hard filter.
Country labels use the node server address's existing GeoIP metadata, not a new
measurement or promise about actual public egress geography.

### Choosing and falling through

1. A country tier chooses eligible nodes with that country label.
2. A latency tier chooses the lowest fresh successful probe duration.
3. A stability tier chooses the smallest population standard deviation of
   recent successful probe durations, measured in time units.
4. When a configured tier has no eligible candidate or its bounded dial budget
   is exhausted, selection advances. The final tier uses existing general-pool
   selection.

Each tier may try at most two distinct nodes; failed node tags are excluded
across the whole dial. With two configured tiers there are at most six attempts,
with one at most four. Each setup attempt is bounded to five seconds and the
whole tiered selection to thirty seconds, or the caller's earlier deadline.
This is a bounded availability attempt, not exhaustive proof that every node in
a large country is unusable. It prevents a large failing preferred set from
starving the second and general tiers. Legacy empty chains retain existing
`MaxRetries` behavior. Caller cancellation is not evidence of node failure.

For Hysteria2 TCP, a tier is not ready merely because a cached QUIC session
returns a lazy stream. Before exposing the connection, the pool sends only the
destination request header and validates the protocol's TCP response within the
setup budget. No application payload is sent or read during this check, so a
failed handshake can safely advance to another tier without replaying a request.
This is specific to the pinned Hysteria2 implementation, not a generic empty
read/write trick for other protocols. It adds a handshake round trip before
application traffic instead of coalescing the first payload into that handshake.
A failed attempt may already have opened an empty TCP connection at the target
before its confirmation was lost. This avoids replaying application requests;
it is not an exactly-once guarantee for TCP connection establishment.

Known unavailable nodes are not preference candidates. Existing mandatory
protocol constraints remain in effect, including available native UDP protocol
requirements. Country and general tiers use `auto`, `stable`, or `session`
within their candidate set. Latency and stability tiers rank by their stated
metric and do not let a sticky binding or an ordinary `pin=`/`X-Proxy-Pin`
override it. A strict required tag remains a hard constraint.

Stable/session bindings are isolated by Profile ID, revision, and tier. A
recovered preferred tier is reconsidered for new flows. Established connections
are not forcibly migrated. Unknown peers have no synthetic session key; NAT
clients with the same observed IP and shared Profile may still share an IP-based
session, as before. Independent Profile IDs or explicit session keys avoid that
ambiguity.

### Probe measurements and cold starts

Latency history is a bounded ring of at most eight successful probe durations.
Stability requires at least three valid samples. Samples are actual probe
measurements, not traffic success counters, health scores, or synthetic copies
of a persisted last latency.

The latest sample must be no older than `max(10 minutes, 2 × health interval)`.
History spans at most `max(10 minutes, 8 × health interval)`. At the default
2-hour health interval these limits are 4 hours and 16 hours; jitter becomes
usable after three successful probes for the same node. A reload preserves
in-memory samples and their timestamps only when the tag and nonempty node URI
are unchanged. Changed or unknown node identity starts cold; persisted last
latency never becomes artificial history. No new background probe frequency
is introduced. Missing, stale, or insufficient
measurements skip that metric tier and allow the next tier/general pool.
Concurrent updates and stale probe generations cannot overwrite current samples.

### Device resolution and entry paths

With Local Server enabled, explicit HTTP, HTTPS CONNECT, SOCKS5, native TUN,
and legacy transparent TCP use the same live Profile registry. Explicit proxy
identity takes precedence, then enabled source IP/CIDR mappings, then shared
fallback. TUN uses the original inbound source address, never the destination or
forwarded HTTP headers. Legacy transparent TCP uses the socket peer. Missing
source metadata selects shared fallback without inventing a device.

TUN applies the selected Profile's rules, enabled state, filters, tier chain,
long-lived policy, and session policy to both TCP and UDP. Disabled Profiles
retain the existing DIRECT behavior. UDP keeps its separate affinity namespace
and native-protocol availability requirements. When Local Server is disabled,
legacy gateway behavior remains in effect, with optional shared tier selection.

A newly started replacement TUN instance admits no TCP or UDP flows until the
matching configuration and Profile publication has committed. Failed candidates
remain closed, and rollback admits flows only after the restored configuration
has been published. This admission check runs before DIRECT rules and fail-open
handling, preventing traffic through a half-published reload.

IP/CIDR mappings are policy selectors, not authentication. Docker/NAT/VPN or
reverse-proxy topology can collapse several clients to one peer. Explicit
`+dev=<id>` is preferred for stable explicit-proxy identification. Transparent
clients need verified source preservation and stable IP mappings; the service
cannot recover identities erased by NAT. Existing shared canonical credentials
do not enforce separate device authorization.

### Direct routing and failure boundary

The three tiers apply only after a routing decision selects PROXY. Existing
DIRECT rules, a disabled Profile, and gateway fail-open settings remain separate
controls. The third tier is a proxy-pool fallback, never DIRECT itself. If the
entire proxy selection fails, explicit proxy requests fail as before; a gateway
with `no_available_proxy_policy: DIRECT` may then use its existing direct
fallback. This feature therefore does not promise strict-US routing or prevent
all direct traffic, IPv6 bypass, or unsupported UDP paths.

Selection cannot guarantee delivery after a connection has been returned. An
established TCP stream, UDP session, or another protocol whose readiness is not
acknowledged may still fail before health monitoring detects the outage. The
pool never replays application bytes or datagrams onto a different node. Such
failures remain visible to the caller; new flows can use the next healthy node.

### Validation before deployment

Use isolated configuration/data and loopback ports. Cover shared and independent
Profiles concurrently, missing/unknown source, IPv4/IPv6 source identities,
HTTP/CONNECT/SOCKS, TCP/UDP TUN, first/second/general fallthrough, missing metrics,
failed dials, cancellation, hot edits, and stale revisions. A successful unit
suite or WebUI build does not prove an actual NAS TUN deployment. Back up both
runtime configuration and SQLite state before a separately approved deployment.

### Legacy pool 与 TUN 共享策略热更新

共享 `selection_tiers` 不以 Local Server 或 smart routing 开关为前提。
旧版 pool 模式的默认 HTTP/SOCKS 入口及额外 pool 监听器，在每条新连接上读取已发布的共享选择快照；
无策略或清空策略时保持原 pool 模式行为。显式设备 directive 始终优先，不由共享选择覆盖。
非 Local Server TUN 每条新 TCP/UDP 流读取共享选择快照，但保留原 gateway 路由策略，
不会因 shared profile 的 routing.enabled=false 而改成全直连，也不会隐式启用设备映射。
UDP 仍要求可用的原生 UDP 节点。地域池、单节点池和 bootstrap 池不受共享默认策略覆盖。

热更新只影响新流，不中断或重放现有连接。新增 generation admission 防止未提交或失败候选
box 使用上一代策略接收新连接；回滚快照包含策略、共享修订号及硬过滤条件。
