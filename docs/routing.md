# 路由、透明网关与 GeoIP

本文按主题集中维护相关合同，按目录查阅；不包含后续阶段计划。

## 目录
- [smart-routing](#smart-routing)
- [default-gateway-routing](#default-gateway-routing)
- [transparent-gateway](#transparent-gateway)
- [geoip-data-sources](#geoip-data-sources)

<a id="smart-routing"></a>

## Smart Routing (智能分流入口)

> Local Server adds shared and independent per-device Profiles on top of this
> routing engine. For the canonical LAN topology, identity grammar, Profile
> matrix, and device APIs, see [`local-server.md`](./local-server.md).

### Goal

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

### Three Orthogonal Layers

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

### Architecture

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

### Packages

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

### Long-lived rating (B rule)

A node is "long-lived" when the runtime self-measures it as stable, not when an
upstream labels it. The rule, per node:

```
now - firstSeenAt >= routing.long_lived.min_uptime (default 2h)
  AND reported success rate >= routing.long_lived.min_success_rate (default 0.9)
  AND currently effective-available
```

Thresholds are configurable and hot-applied to existing monitor entries; zero
values restore the defaults above without rebuilding sing-box.

### Stickiness semantics

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

### API parameters (only when `routing.enabled`)

Two public request channels are merged in increasing priority:
**path/username < HTTP header**. The dispatcher also has an internal bound
overlay, but no public configuration currently exposes port-bound directives.

#### Path prefix (HTTP)

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

#### HTTP headers

`X-Proxy-Strategy`, `X-Proxy-Country`, `X-Proxy-Region`, `X-Proxy-Long-Lived`,
`X-Proxy-Pin`, `X-Proxy-Session`, `X-Proxy-Split`.

`X-Proxy-Split: off` disables splitting (force all-proxy; common for crawlers).

#### SOCKS5 (username carries the token)

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

### Config

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

### Observability

- `GET /api/routing/status` — enabled flag, listen, default strategy, final
  policy, active rule count, and the current sticky bucket / session bindings.
- `GET /api/debug` and `GET /api/nodes` — each node now also reports
  `long_lived` and `uptime_seconds`.

### Runtime update and reload contract

The following matrix describes compatible legacy routing edits while the runtime is available; Local Server Profile/CAS behavior is documented in [local-server.md](local-server.md). A pending reload or topology incompatibility can require a reload instead of hot application.

The management API applies edits with the cheapest sufficient mechanism
(`RoutingController.ApplyHot` in `routing_controller.go`):

| Edit | Effect |
|---|---|
| rules / rule_providers / final_policy / default_strategy | **Hot-applied** to the running engine + dispatcher. No restart, no dropped connections. `need_reload=false`. |
| long-lived `min_uptime` / `min_success_rate` | **Hot-applied** to the live monitor manager and existing entries. `need_reload=false`; zero restores the default threshold. |
| enabled / listen / session TTL | **Needs reload**. boxmgr coordinates the candidate box, dispatcher topology, and monitor listener transactionally; `need_reload=true` and the UI surfaces it. |
| `skip_cert_verify` | The monitor probe manager updates immediately after a successful save, but the settings API still returns `need_reload=true` so sing-box outbound TLS behavior is rebuilt from the candidate. |
| log level / GeoIP enablement, database, or auto-update settings | **Needs reload** through `/api/settings`; these fields are persisted first and applied transactionally with the next box candidate. |
| management probe target / source-sync and subscription topology | Probe settings may update immediately after persistence; source/subscription topology is applied by the follow-up reload, and the settings API reports `need_reload=true` for the full runtime transaction. |
| connector create/update/delete/enable and preferred-IP refresh | Candidate-save first, then automatic source refresh and reload. An edit that arrives during the reload window receives HTTP `409` with `need_reload=true`. |
| node CRUD and enable/disable | Serialized behind the same manager mutation barrier; the operation waits for an active reload and then writes the current config rather than a retired snapshot. |

While a reload transaction is between `PrepareReload` and its final config
commit, settings, routing, and connector HTTP edits are rejected with
`409 Conflict` and `need_reload=true`. Lower-level node/connector mutation
callers are still serialized by the manager barrier. A persisted edit that
loses the reload race is picked up by the follow-up reload rather than being
overwritten by the captured target.

Both routing and management listener runtime state are kept on the same server
object across reloads. A new management address is bound synchronously, its
target config/auth snapshot is applied before activation, and the old listener
is drained asynchronously; a bind failure leaves the old address serving and
aborts the candidate transaction. Handler dependencies are read as local
snapshots before calling reload-capable managers, so a listener rebind cannot
tear an interface value or hold a dependency lock across a reload.

### Limitations / notes

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

<a id="default-gateway-routing"></a>

## 默认网关规则分流

### 本次修改边界

修复 `gateway.mode: tun` 且未启用 `local_server` 的默认网关路径。
不修改 HTTP / CONNECT / SOCKS5 的客户端参数解析、`split` / `nosplit`、
显式节点选择、严格固定节点以及设备 profile 的既有行为，也不通过开启
`routing.enabled` 或 `local_server.enabled` 来间接改变这些入口。

默认 TUN 网关复用已有规则引擎和配置，匹配顺序为：

1. `routing.rules` 中的手动规则。
2. `routing.rule_files` 中的本地规则。
3. `routing.rule_providers` 中的远程规则，沿用已有定期刷新机制。
4. `routing.use_default_rules` 开启时的内置规则，包括私网、常见国内域名和 `GEOIP,CN,DIRECT`。
5. `gateway.routing.final_policy`，可选 `PROXY` 或 `DIRECT`；不再被内置规则里的 `FINAL,PROXY` 覆盖。

`geoip.enabled` 开启时，TUN 网关现在真正加载 `geoip.database_path`。
数据库配置为空或加载失败会明确报错，而不是悄悄让国内 IP 落入代理兜底。
数据库自动更新沿用 `geoip.auto_update_enabled` 与 `geoip.auto_update_interval`。
网关停止时关闭其 GeoIP 查询资源，并停止规则 provider 刷新。

TUN 保留真实目的 IP，同时使用嗅探域名匹配域名规则；不会为了嗅探而用域名
替换真实 IP、丢失 IP-CIDR / GEOIP 判断依据。Fake-IP 的域名还原仍由 sing-box
处理，不把合成地址当作真实国家归属依据。
决定使用 PROXY 后仍向代理链路传递嗅探域名，保留原有代理端域名解析行为；
DIRECT 则使用真实目的 IP，避免再次解析后改变客户端选择的目标。

### 配置示例

以下仅是现有运行配置中的相关片段，不应直接覆盖完整配置或凭据：

```yaml
routing:
  use_default_rules: true
  rules:
    - DOMAIN-SUFFIX,force-direct.example,DIRECT
    - DOMAIN-SUFFIX,force-proxy.example,PROXY

gateway:
  routing:
    final_policy: PROXY
```

未知目标的策略是明确的配置选择，不是可达性探测：

- `PROXY`：未命中直连规则的目标继续代理，保留原默认行为。
- `DIRECT`：未知目标直连，需要用代理规则列表覆盖被墙目标。

此次没有新增“直连失败后自动重发请求”的机制。GeoIP 也不是网站可达性的
充分证明；境外但可直连的网站可加入 DIRECT 规则。Fake-IP / 纯域名目标没有
真实目的 IP 时，需要域名规则覆盖，不能仅依赖 `GEOIP,CN,DIRECT`。

### 验证和上线边界

相关回归覆盖默认网关 TCP / UDP、国内 IP 与嗅探域名并存、手动覆盖、
provider 规则优先级和更新、DIRECT 最终兜底、损坏数据库显式报错，以及
builder 不启用客户端 dispatcher / device profiles。现有客户端参数、profile、
gateway 和 app / boxmgr 回归也应一起运行。

本地代码与构建通过不代表 NAS 已更新。上线需使用新的 Linux 运行程序或镜像，
保留现有运行配置与数据，随后实际核对国内访问出口、需要代理的网站和显式选节点请求。
`local_server` 的 profile 路由与旧式非 TUN 透明 TCP 路径不属于本次修复范围。

<a id="transparent-gateway"></a>

## EasyProxy Transparent Gateway

EasyProxy provides a native Linux TUN gateway and retains the legacy transparent
TCP ingress as a rollback mode. It is
network-fabric independent: physical LAN, Tailscale, 星空组网, and future
overlays only need to route trusted client packets to the NAS. EasyProxy does
not call a Tailscale or 星空组网 API and must not be bound to one interface.

### Runtime Contract

Enable `gateway.enabled` only on a Linux/NAS host that can provide:

- host networking, or an equivalent network namespace with forwarding;
- `CAP_NET_ADMIN` and `CAP_NET_RAW`;
- `iproute2` and `nftables` binaries;
- `/dev/net/tun`, policy routing, and nftables support for TUN mode;
- a kernel with `IP_TRANSPARENT` and nftables TPROXY support only for legacy
  `mode: transparent`;
- an ingress route from each trusted physical/overlay CIDR.

Each enabled TUN address family requires at least one trusted client CIDR from
that family. This prevents an apparently healthy IPv4 or IPv6 mode from silently
capturing no clients.

The container image includes `ip` and `nft`, but gateway deployments must set
`EASY_PROXY_RUN_AS_ROOT=1` and add the two capabilities in compose. Ordinary
proxy deployments remain unprivileged. Do not expose `22323`, `29888`, or the
TPROXY listener to WAN or guest networks.

In `mode: tun`, sing-box owns `easyproxy0`; the host supervisor installs IPv4
and optional IPv6 fwmark routes to that interface for configured trusted
interfaces/CIDRs. TCP, UDP, QUIC, and intercepted DNS use the same EasyProxy
route outbound and node pool. Local/private destinations, marked EasyProxy
egress, DHCP/NDP/multicast, and control-plane traffic are bypassed before
capture. Rules are removed on clean shutdown and before reload or TUN teardown.

In legacy `mode: transparent`, the gateway listens on `gateway.listen` (default
`0.0.0.0:15001`) and captures IPv4 TCP through TPROXY.

### Routing And Failure Policy

TUN TCP and UDP use the same rule engine and node pool as explicit HTTP, HTTPS
CONNECT, and SOCKS5 requests. Sniffed or fake-IP-recovered domains are evaluated
before literal destination IPs. TCP and UDP maintain independent failure cooldowns
so a transport-specific failure does not suppress a working path on the same
node. TCP may retain the configured sticky strategy; UDP uses health-first `auto`
selection so one intermittent node cannot pin the whole gateway. UDP first
uses an effectively available native UDP transport such as Hysteria2 or TUIC.
When none is proven available, it follows `no_available_proxy_policy` rather than
silently handing datagrams to an unverified fallback transport.

The default policy is fail-open:

    usable proxy node -> PROXY
    no usable node    -> DIRECT
    gateway stopped   -> normal host forwarding after rule cleanup

`GET /api/gateway/status` reports mode, TUN interface/stack/MTU, IPv4/IPv6,
TCP/UDP/DNS readiness, listener state, legacy transparent connection counters,
and the last lifecycle error. The endpoint is protected by the existing
management authentication.

### Docker/NAS Example

    services:
      easy-proxy:
        network_mode: host
        cap_add:
          - NET_ADMIN
          - NET_RAW
        environment:
          EASY_PROXY_RUN_AS_ROOT: "1"
        volumes:
          - ./config.yaml:/var/lib/easyproxy/config/config.yaml
          - ./data:/var/lib/easyproxy

Tailscale and 星空组网 may run as DSM services, host processes, or separate
host-network containers. They are transport adapters only. Configure their
client routes/exit-node behavior so packets arrive at the NAS; configure
EasyProxy with the resulting interface names and trusted CIDRs. Do not put the
gateway behind a Docker bridge and assume it is a LAN router.

### Safe Rollback

1. Set `gateway.enabled: false` and reload EasyProxy, or stop the container.
2. Confirm `GET /api/gateway/status` reports `applied: false` and
   `tun_ready: false`.
3. On the NAS, inspect `ip rule`, `ip -6 rule`, `ip route show table 100`,
   `ip -6 route show table 101`, and
   `nft list table inet easyproxy_gateway`; the EasyProxy entries must be gone.
4. Restore the client's previous default route/exit-node selection.

Never delete another application's nftables table or policy rule by hand.

### Capability Boundary

TUN mode handles IPv4/IPv6 TCP and UDP, including QUIC/HTTP3, and intercepts
trusted-client TCP/UDP port 53 when `dns_hijack` is enabled. Fake-IP domain
association is persisted under the EasyProxy data directory. ICMP, ESP, GRE,
and other non-TCP/UDP protocols remain DIRECT/bypassed rather than externally
proxied. DoH/DoT are ordinary encrypted TUN flows; certificate-pinned clients
may not provide a recoverable domain, so IP/GeoIP/final rules still apply.

### Validation

Use `scripts/validate-transparent-gateway.ps1` for a read-only management API
check from Windows. Run `scripts/validate-transparent-gateway-linux.sh
--require-tun` on a disposable Linux/NAS host with the gateway configuration; it
checks TUN readiness, IPv4/IPv6 forwarding and policy routes, plus live nftables
TCP/UDP/DNS capture rules. `scripts/validate-tun-gateway-docker.py --build`
creates an isolated dual-stack topology and proves IPv4/IPv6 TCP, UDP, real QUIC,
Fake-IP DNS and SOCKS5 UDP ASSOCIATE. Client-side reachability must still be
tested from each actual LAN/overlay whose default route points at the NAS; the
host validator does not alter client routes.

<a id="geoip-data-sources"></a>

## GeoIP 数据来源与署名

### 可选官方 DB-IP Country Lite

- 来源：https://db-ip.com/db/download/ip-to-country-lite
- 2026-11 官方下载：https://download.db-ip.com/free/dbip-country-lite-2026-11.mmdb.gz
- 许可：Creative Commons Attribution 4.0 International，https://creativecommons.org/licenses/by/4.0/
- 数据作者：DB-IP，https://db-ip.com/
- 格式说明：https://db-ip.com/db/format/ip-to-country-lite/mmdb.html

免费数据降低覆盖与准确度、每月更新。生产国家策略只采用节点服务器地址的国家，不代表实测公网出口。
本项目不附带该数据库，不把第三方镜像的名称当作官方来源证明；操作者需从官方地址取得文件。
应用使用该数据须署名，显示或使用结果的 web 页面须含 DB-IP 回链。管理界面通用页脚提供
“IP Geolocation by DB-IP”和 CC BY 4.0 链接，注明仅配置 DB-IP 时适用。
数据库原内容未修改，仅解压 gzip。随部署数据保留本文件或等价 NOTICE。

### 部署校验边界

安装时校验下载文件哈希、格式和本项目 reader 的 IPv4/IPv6 查询结果；
样例查询只证明数据库记录可读取，不证明代理出口所在地。

不得把测试 MMDB 或仅含中国记录的第三方 Country.mmdb 用作全球国家策略数据库。
启用 GeoIP、指向下载文件和重载生产均须走单独确认与现有回滚步骤。
如果自动更新仍使用旧第三方默认地址，必须保持关闭，避免后来悄悄替换已确认数据来源。
