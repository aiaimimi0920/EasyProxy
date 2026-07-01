# Smart Routing (智能分流入口)

## Goal

Turn the EasyProxy entry from a plain pool inbound into a *smart proxy entry*
whose behaviour is driven by a per-request / per-session selection policy. It
adds Clash/mihomo-style traffic splitting on top of the existing pool, without
replacing any of the pool's scheduling, health, blacklist, or stats logic.

The feature is opt-in. With `routing.enabled: false` (the default) the runtime
behaves exactly as before: the sing-box pool inbound serves all traffic with no
splitting. With `routing.enabled: true` the smart dispatch entry takes over the
listener host:port and serves both HTTP/HTTPS CONNECT and SOCKS5 on one port.

## Three Orthogonal Layers

The selection policy is composed of three independent, combinable layers.

1. **Split (要不要代理)** — based on the destination host/IP, decide DIRECT vs
   PROXY. Same rule model as Clash/mihomo. Answers only "does this flow need to
   be proxied?".
2. **Strategy (用哪个节点)** — applies to PROXY flows:
   - `stable` — pin all traffic in the same filter bucket to one long-lived
     node (anti-ban: stable egress IP). On failure the bucket is promoted to the
     next best healthy node automatically.
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

## Long-lived rating (B rule)

A node is "long-lived" when the runtime self-measures it as stable, not when an
upstream labels it. The rule, per node:

```
now - firstSeenAt >= routing.long_lived.min_uptime (default 2h)
  AND reported success rate >= routing.long_lived.min_success_rate (default 0.9)
  AND currently effective-available
```

Thresholds are configurable; zero values fall back to the defaults above.

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

Three channels, merged in increasing priority: **port-bound < path/username <
HTTP header**.

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

## Observability

- `GET /api/routing/status` — enabled flag, listen, default strategy, final
  policy, active rule count, and the current sticky bucket / session bindings.
- `GET /api/debug` and `GET /api/nodes` — each node now also reports
  `long_lived` and `uptime_seconds`.

## Limitations / notes

- GEOIP rules apply only to literal-IP destinations (no per-request DNS
  resolution, to avoid blocking the hot path). Domain destinations fall through
  to domain rules + FINAL.
- The default entry shares one node across all default traffic (the anti-ban
  goal), which concentrates concurrency/bandwidth on that node by design.
- All new behaviour is gated by `routing.enabled` and the presence of a ctx
  directive; disabled = current behaviour, unchanged.
