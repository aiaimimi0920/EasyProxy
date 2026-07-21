# Smart Routing (智能分流入口)

> Local Server adds shared and independent per-device Profiles on top of this
> routing engine. For the canonical LAN topology, identity grammar, Profile
> matrix, and device APIs, see [`local-server.md`](./local-server.md).

## Goal

Turn the EasyProxy entry from a plain pool inbound into a *smart proxy entry*
whose behaviour is driven by a per-request / per-session selection policy. It
adds Clash/mihomo-style traffic splitting on top of the existing pool, without
replacing any of the pool's scheduling, health, blacklist, or stats logic.

The feature is opt-in. With `routing.enabled: false` (the default) the runtime
behaves exactly as before: the sing-box pool inbound serves all traffic with no
splitting. With `routing.enabled: true` the smart dispatch entry takes over the
listener host:port and serves both HTTP/HTTPS CONNECT and SOCKS5 on one port.

When `local_server.enabled: true`, the `routing` block is the shared Profile and
the dispatcher is enabled even when that shared Profile is disabled. A disabled
selected Profile always routes `DIRECT`; an independent enabled Profile can
still use its own rules while the shared Profile is disabled. The preferred
device-aware API is `/api/local-server/*`; `/api/routing/config` and
`/api/routing/status` remain shared-Profile compatibility aliases.

## Three Orthogonal Layers

The selection policy is composed of three independent, combinable layers.

1. **Split (要不要代理)** — based on the destination host/IP, decide DIRECT vs
   PROXY. Same rule model as Clash/mihomo. Answers only "does this flow need to
   be proxied?".
2. **Strategy (用哪个节点)** — applies to PROXY flows:
   - `stable` — pin all traffic in the same filter bucket to one node (anti-ban:
     stable egress IP). A new binding prefers a healthy long-lived node and
     falls back to any healthy node when none qualify. An existing healthy
     binding does not drift merely because a new long-lived node appears.
   - `session` — pin a session (by key, falling back to client source IP) to one
     node for the session TTL. For crawlers that need short-term IP stickiness.
   - `auto` — the pool's existing Mode selection (health-based / random /
     balance / sequential).
3. **Filter (在哪批节点里选)** — narrow the candidate set by country / region /
   long-lived before strategy selection.

Two entries map onto these layers:

- **Default entry (system proxy, no params)** = split + `stable`. Works out of
  the box, stable IP.
- **API entry (with params)** = three param channels override the defaults.

## Architecture

```
client → dispatch.Server (sniff HTTP vs SOCKS5 on first byte)
       → parse params → resolve directive + routing policy
       ├─ DIRECT → net.Dialer
       └─ PROXY  → pool.WithDirective(ctx) → pool.DialContext
                   → pool honours stable/session/filter, reusing the existing
                     health / blacklist / stats engine
```

Key point: sing-box's static `route.Rules` can express Layer 1 (target-based
DIRECT/PROXY) but **not** Layers 2/3 (per-request/per-session node choice). So
the smart logic lives in the dispatcher + pool, and the directive is threaded
through the dial `context`. sing-box stays a pure transport engine here.

The dispatcher always reads the *live* pool outbound from the box manager
(`PoolOutbound()`), so it stays correct across config reloads that swap the box.
The reload lifecycle stops/rebinds the dispatcher only when enabled/listen/auth
topology changes; source-only and rule-only reloads keep the existing listener.
Management listener replacement applies the target config/auth snapshot before
the new address accepts requests and restores the old snapshot on rollback.
If candidate startup, health validation, listener binding, or lifecycle
completion fails, boxmgr restores the last applied box and routing snapshot.

## Packages

- `internal/routerule/` — rule engine (DOMAIN-SUFFIX / DOMAIN-KEYWORD / DOMAIN /
  IP-CIDR / GEOIP / FINAL), built-in China-direct default set, and remote rule
  providers (`rule_providers`).
- `internal/dispatch/` — smart entry server. HTTP/HTTPS CONNECT + SOCKS5 on one
  port via first-byte protocol sniffing. Parses params, decides DIRECT/PROXY,
  injects the directive into the dial context.
- `internal/outbound/pool/` (extended) — `directive.go` (selection directive +
  ctx carrier), `sticky.go` (stable buckets + session bindings with lazy TTL),
  filter-aware candidate selection. **Backward compatible**: a nil directive
  falls back byte-for-byte to the original `selectMember`.
- `internal/monitor/` (extended) — node `firstSeenAt` + long-lived self-rating
  (uptime + success rate); `Snapshot` exposes `LongLived` / `UptimeSeconds`.
  Reload probes bind generation, round ID, and probe revision; candidate rounds
  supersede periodic rounds, so old/candidate late completions cannot overwrite
  the active box's health state.

## Long-lived rating (B rule)

A node is "long-lived" when the runtime self-measures it as stable, not when an
upstream labels it. The rule, per node:

```
now - firstSeenAt >= routing.long_lived.min_uptime (default 2h)
  AND reported success rate >= routing.long_lived.min_success_rate (default 0.9)
  AND currently effective-available
```

Thresholds are configurable and hot-applied to existing monitor entries; zero
values restore the defaults above without rebuilding sing-box.

## Stickiness semantics

- **stable buckets** — keyed by a stable hash of the (normalized) filter. The
  default entry (no filter) is one bucket; `cc=US` is another; `jp` is another.
  All traffic in a bucket shares one node; on failure the bucket promotes the
  next best healthy candidate. A manually pinned tag (`pin=`) is used if it is a
  live candidate, otherwise it falls through to bucket promotion (auto fail-over
  to the next stable node).
- **session bindings** — keyed by the client-supplied session key, or the client
  source IP when no key is given. Idle bindings expire after `routing.session.ttl`
  (default 10m). If the bound node dies mid-session the session is rebound to a
  new candidate (stickiness is necessarily broken on death).

Expiry is lazy (a sweep runs at most once per TTL on access), so there is no
per-pool cleanup goroutine.

## API parameters (only when `routing.enabled`)

Two public request channels are merged in increasing priority:
**path/username < HTTP header**. The dispatcher also has an internal bound
overlay, but no public configuration currently exposes port-bound directives.

### Path prefix (HTTP)

```
CONNECT stable+us/example.com:443
GET http://<proxy>/session+sid=job42/...
```

Tokens (case-insensitive, `+`-separated):

| Token | Meaning |
|-------|---------|
| `auto` / `stable` / `session` | strategy |
| `jp` `kr` `us` `hk` `tw` `other` | region filter (repeatable) |
| `cc=US` | country ISO filter (repeatable) |
| `long` / `nolong` | long-lived filter on/off |
| `pin=<tag>` | manual node pin |
| `sid=<key>` | session key |
| `split` / `nosplit` | enable/disable splitting (nosplit = force PROXY) |

### HTTP headers

`X-Proxy-Strategy`, `X-Proxy-Country`, `X-Proxy-Region`, `X-Proxy-Long-Lived`,
`X-Proxy-Pin`, `X-Proxy-Session`, `X-Proxy-Split`.

`X-Proxy-Split: off` disables splitting (force all-proxy; common for crawlers).

### SOCKS5 (username carries the token)

SOCKS5 has no headers, so the directive token rides in the **username** field
(common proxy-pool convention), with the exact same token syntax as the path
prefix:

```
socks5h://stable+us+sid=job42@<proxy>:22323
```

Auth rules when `listener.username` / `listener.password` are set:

- SOCKS password must equal `listener.password`.
- The username's leading segment (before the first `+`) is compared to
  `listener.username`; everything after `+` is the directive token.
- When no proxy auth is configured, the username is used purely as the token.

## Config

```yaml
routing:
  enabled: true                  # 默认 false；开启后接管 listener host:port
  # listen: 0.0.0.0:22323        # 留空则接管 listener 的 host:port
  default_strategy: stable       # 默认入口策略：stable / session / auto
  use_default_rules: true        # 附加内置“中国直连”默认规则集
  final_policy: PROXY            # 兜底策略：DIRECT / PROXY
  rules:                         # 自定义规则，按顺序优先于默认集
    - "DOMAIN-SUFFIX,example.com,DIRECT"
    - "GEOIP,CN,DIRECT"
  rule_providers:                # 远程规则集（失败软处理，按 interval 刷新）
    - url: "https://.../direct-domains.txt"
      policy: DIRECT
      behavior: domain           # domain | classical
      interval: 24h
  long_lived:
    min_uptime: 2h
    min_success_rate: 0.9
  session:
    ttl: 10m
```

Rule precedence inside the engine: `routing.rules` first, then provider rules,
then the built-in default set (when `use_default_rules`), then `final_policy`.
`final_policy` is stored separately from parsed rules, so legacy/default `FINAL`
entries cannot silently override the configured authoritative fallback.

When routing is enabled in pure `multi-port` mode, the builder still creates the
global `proxy-pool` outbound for the dispatcher, while leaving the per-node
listeners unchanged and omitting the ordinary global pool inbound.

That legacy behavior applies only while Local Server is disabled. Enabling
Local Server requires `mode: pool`, `listener.protocol: mixed`, and no
`extra_listeners`, so a second listener cannot bypass device Profile selection.

For Docker deployments, an empty `routing.listen` uses route A and the existing
published listener port. A different route-B port must also be explicitly
published by Docker/Compose; changing YAML alone cannot expose a container port.

## Observability

- `GET /api/routing/status` — enabled flag, listen, default strategy, final
  policy, active rule count, and the current sticky bucket / session bindings.
- `GET /api/debug` and `GET /api/nodes` — each node now also reports
  `long_lived` and `uptime_seconds`.

## Limitations / notes

- GEOIP rules apply only to literal-IP destinations (no per-request DNS
  resolution, to avoid blocking the hot path). Domain destinations fall through
  to domain rules + FINAL.
- SOCKS5 supports CONNECT only; UDP ASSOCIATE and BIND are not implemented.
- Route B requires an explicit Docker/Compose port mapping for the custom
  `routing.listen` port.
- Port-bound fixed directives exist only as an internal dispatcher overlay and
  are not currently exposed through user configuration.
- The default entry shares one node across all default traffic (the anti-ban
  goal), which concentrates concurrency/bandwidth on that node by design.
- All new behaviour is gated by `routing.enabled` and the presence of a ctx
  directive; disabled = current behaviour, unchanged.
- Under Local Server, `routing.enabled` is the shared Profile's enabled state,
  not the dispatcher lifecycle switch. See the companion Local Server document
  before applying legacy multi-port or route-B guidance.
