# EasyProxy Local Server 与设备 Profile 设计

> 状态：已完成交互设计确认，等待书面规格复核
> 日期：2026-07-19
> 分支：`feat/smart-routing-dispatch`
> 基线：Smart Routing 已完成并通过最终隔离 E2E；本设计在该基线上扩展 Local Server 能力。

## 1. 摘要

EasyProxy 将在不改变远程 MiSub、订阅、Aggregator、connector 和节点来源语义的前提下，增加一个局域网中心化的 Local Server 模式。

Local Server 继续运行现有代理数据面、节点同步、健康检查、Smart Routing、管理 API 和嵌入式 Web Console。局域网设备不安装独立 Local Client；所有设备共同访问同一个代理入口和同一个 Web Console。

每台设备默认使用 shared Profile。设备可以拥有完整、独立的 Forwarding Profile；独立 Profile 存在时不继承、不合并也不跟随 shared Profile。shared 或 independent Profile 的开关关闭时，该 Profile 的流量全部 DIRECT，但代理监听器保持可用。

设备身份使用混合解析：显式非秘密 `device_id` 优先，服务端 IP/CIDR 映射回退，两者都未命中时使用 shared Profile。所有设备共同使用一套管理和代理凭证。

## 2. 目标

1. 在一个 LAN Local Server 上集中提供 HTTP、HTTP CONNECT、SOCKS5、管理 API 和 Web Console。
2. 保持远程 MiSub、订阅、Aggregator、connector、节点同步和健康状态为全局共享基础设施。
3. 提供 shared Profile 总开关、完整共享配置和完整共享策略。
4. 为设备提供完整、独立、持久化的 Forwarding Profile 和独立开关。
5. 保证独立 Profile 与 shared Profile 完全解耦。
6. 保证 Profile 关闭等价于所有请求 DIRECT，而不是恢复旧 pool 代理行为。
7. 使用显式 `device_id` 与 IP/CIDR 回退识别设备。
8. 使用一套 canonical credential 认证 Web Console、管理 API、HTTP proxy 和 SOCKS5。
9. 复用现有 `monitor.Server`、嵌入式前端、reload barrier、BoxManager 和 Shared Pool。
10. 保持未启用 Local Server 的旧配置和 Smart Routing 行为不变。

## 3. 非目标

第一阶段不实现以下能力：

- 每设备独立 MiSub、订阅或 connector；
- 每设备独立节点集合、BoxManager、健康池或统计数据库；
- 每设备密码、证书、RBAC、多用户或权限分级；
- 自动硬件指纹、MAC 地址采集或浏览器 `localStorage` 设备识别；
- 新的 Local Client 进程、桌面应用或设备端 daemon；
- 新的 TLS 子系统；跨不可信网络访问应由 VPN、WireGuard 或反向代理提供保护；
- SOCKS5 BIND 或 UDP ASSOCIATE；
- 自动把首次出现的源 IP 绑定到某个设备；
- 不为 legacy multi-port/hybrid/extra listener 入口提供设备 Profile 选择。

## 4. 当前实现约束

当前代码有以下必须保留或拆分的契约：

- `routing.enabled=false` 会恢复 plain pool inbound，流量仍然 PROXY；它不能直接表示新的“关闭后 DIRECT”。
- `RoutingController` 当前将 `routing.enabled` 和 BoxManager idle 状态用于 dispatcher 生命周期；Local Server 模式必须让 dispatcher 与两者解耦。
- `SubscriptionManager`、`BoxManager`、pool、monitor 和 node store 都是全局单例。
- `Config.SaveSettings` 会把 YAML 反序列化为 `Config` 后重新序列化；所有新增 YAML 字段必须加入 `Config`、Clone、normalize 和保存测试。
- 管理、listener 和 multi-port 目前有三套凭证字段，且代理认证只在 username 非空时启用。
- Web session 在内存和 SQLite 中持久化；当前凭证修改不会让旧 session 失效。
- HTTP dispatcher 和 SOCKS5 已支持 `+` 分隔的 routing token，适合扩展 `dev=<device_id>`。
- 管理 API 和嵌入式前端已经由同一个 `monitor.Server` 提供，不需要第二个 Web 服务。

## 5. 术语与强制不变量

### 5.1 Profile 类型

- `shared`：没有独立 Profile 的设备使用的共享 Forwarding Profile。
- `device`：由稳定 `device_id` 标识的完整独立 Forwarding Profile。

### 5.2 不变量

1. independent Profile 存在时完全替代 shared Profile。
2. independent Profile 不是 overlay、patch 或持续同步的副本。
3. 修改 shared 不得改变任何 independent Profile 的持久化或运行时内容。
4. 修改 independent Profile 不得改变 shared 或其他设备 Profile。
5. 删除 independent Profile 后设备立即回到 shared。
6. Profile 关闭时无条件 DIRECT，并忽略所有请求级强制 PROXY 参数。
7. 显式 `device_id` 永远优先于源 IP/CIDR 映射。
8. `device_id` 是策略选择器，不是安全主体。
9. 数据面请求只读取最后一次成功发布的不可变 Registry 快照。
10. Profile 更新失败不得改变当前运行时行为。

## 6. 总体架构

```text
Remote MiSub / subscriptions / aggregator / connectors
                         |
                         v
              SubscriptionManager + BoxManager
                         |
                         v
                  Shared Pool Outbound
                         ^
                         |
LAN clients -> Local Dispatch Server
               |  authenticate shared credential
               |  parse explicit device_id
               |  resolve IP/CIDR fallback
               |  select shared/device Profile
               |  selected.enabled == false -> DIRECT
               |  selected.enabled == true  -> rule engine
               +-----------------------------> DIRECT / PROXY

LAN browsers -> existing monitor.Server :29888
                |  embedded Web Console
                |  /api/local-server/*
                +-> ProfileManager -> Store + atomic ProfileRegistry
```

### 6.1 新组件

`ProfileManager` 负责：

- 读取 shared 和 device Profile；
- 完整校验 candidate；
- 持久化 device Profile 和 IP 映射；
- 管理 optimistic revision；
- 原子发布 `ProfileRegistry`；
- 管理每个 Profile 的 rule provider generation；
- 暴露给管理 API 和 dispatcher 的只读快照。

`ProfileRegistry` 是不可变运行时快照，包含：

- compiled shared Profile；
- compiled device Profiles；
- IP/CIDR mapping index；
- registry revision；
- credential generation；
- provider 状态摘要。

`ProfileResolver` 输入已认证的 `RequestIdentity`，输出：

- 解析出的 `device_id`；
- identity source：`explicit`、`ip_mapping` 或 `shared_fallback`；
- selected Profile；
- selected Profile ID 和 revision。

`DeviceActivityTracker` 在内存中维护最近出现的设备、源 IP 和时间。它不参与身份授权，不在每次请求时写 SQLite，重启后可以从空状态重新观察。

### 6.2 Local Server 与 legacy 模式

```text
local_server.enabled 缺失或 false
  -> 保持现有 Smart Routing 生命周期和 routing.enabled 语义

local_server.enabled = true
  -> Local Dispatch Server 常驻并接管 listener 入口
  -> routing.enabled 作为 shared Profile enabled 的兼容字段
  -> BoxManager idle 不停止 dispatcher
```

当 `local_server.enabled=false` 时，现有 `multi-port` 和 `extra_listeners` 保持 legacy 行为，不参与设备 Profile 解析。当 `local_server.enabled=true` 时，设备 Profile 语义只在统一 `listener`/dispatcher 入口上成立；Web Console 和文档必须把 legacy 入口标记为不可用。标准 Local Server 部署使用 `mode: pool` 和统一 mixed listener。

为避免存在绕过 Profile 开关的第二套代理入口，`local_server.enabled=true` 时配置校验要求 `mode: pool`，并拒绝启用 `extra_listeners`。`multi-port`、`hybrid` 和 extra listener 只在 `local_server.enabled=false` 的 legacy 模式使用；它们的旧凭证和行为不被 Local Server 迁移覆盖。

## 7. 配置模型

### 7.1 新配置

```yaml
local_server:
  enabled: true
  listen: ""                    # 空值复用 listener address/port
  auth:
    username: easyproxy         # canonical base username
    password: change-me         # canonical shared secret
  shared_revision: 1            # 内部 optimistic revision
  credential_generation: 1      # 内部 session generation
```

`LocalServerConfig` 必须加入：

- `config.Config`；
- defaults 和 validation；
- `Config.Clone`；
- `SaveSettings`；
- management settings DTO；
- 根配置渲染器和部署模板。

### 7.2 canonical credential

`local_server.auth.username/password` 是唯一用户可编辑的凭证来源。

Local Server 模式要求 username 和 password 都非空。base username 必须匹配 `[A-Za-z0-9._-]{1,64}`；password 不允许包含 NUL，最大 256 字节。旧配置无法迁移出非空 credential 时，启用 Local Server 必须返回验证错误，不能启动开放代理。

Local Server 模式下，运行时派生：

- `management.password`；
- `listener.username/password`；
- Web login username/password；
- HTTP proxy Basic username/password；
- SOCKS5 RFC 1929 username/password。

管理 GET API 只返回 username 和 `password_set`，不返回 password。

`PUT /api/local-server/config` 中 `auth_password` 是 write-only：字段缺失表示保持当前密码，非空值表示执行凭证轮换，空字符串返回 `422`。Local Server 模式不支持通过该 API 清除密码。

Local Server 模式下 `local_server.listen` 是 dispatcher 的唯一显式监听配置。它为空时复用 `listener`；如果它与非空 `routing.listen` 同时存在且值不同，配置校验必须报错，不能静默选择其中之一。legacy 模式继续使用现有 `routing.listen` 语义。

### 7.3 Forwarding Profile

shared 和 device 使用同一领域结构：

```json
{
  "schema_version": 1,
  "enabled": true,
  "default_strategy": "stable",
  "use_default_rules": true,
  "final_policy": "PROXY",
  "rules": [],
  "rule_providers": [],
  "node_filter": {
    "countries": [],
    "regions": [],
    "long_lived": null
  },
  "long_lived": {
    "min_uptime": "2h",
    "min_success_rate": 0.9
  },
  "session": {
    "ttl": "10m"
  }
}
```

设备 Profile 不包含 listener、management、MiSub、subscription、connector、node、database 或 shared credential。

shared Profile 继续持久化在现有 `routing` YAML 中。`local_server.shared_revision` 与 shared Profile 在同一次原子 YAML 写入中递增。device Profile 持久化在 SQLite。

## 8. 设备身份与认证协议

### 8.1 username grammar

HTTP proxy 和 SOCKS5 使用同一 grammar：

```text
<base-username>[+dev=<device-id>][+routing-token...]
```

示例：

```text
easyproxy
easyproxy+dev=laptop-work
easyproxy+dev=laptop-work+stable+us
```

解析规则：

- 第一段必须常量时间比较匹配 canonical base username；
- password 必须常量时间比较匹配 canonical shared password；
- `dev=` 最多一次；
- `device_id` 必须匹配 `[A-Za-z0-9._-]{1,64}`；
- routing token 继续使用现有语法；
- 未携带 `dev=` 时进入 IP/CIDR 回退。

认证解析器必须先验证 base username/password，再单独提取和校验 `dev=`，最后只把剩余 token 交给现有 routing token parser。非法或重复的 `dev=` 不能被当作未知 routing token 后静默回到 shared。

### 8.2 解析优先级

```text
if explicit dev=<device_id> is present:
    use that device identity and never consult IP mappings
else if an exact IP mapping exists:
    use the mapped device identity
else if a CIDR mapping matches:
    use the longest-prefix/highest-priority mapping
else:
    use shared fallback
```

显式 ID 和 IP 映射得到的设备即使没有 independent Profile，也保留设备身份并使用 shared Profile。显式 ID 未命中 Profile 时直接使用 shared，不再继续尝试 IP 映射。删除 independent Profile 不删除设备映射。

只使用真实 TCP peer address。第一阶段不信任 `X-Forwarded-For`，也不自动学习映射。Docker bridge、published port、NAT 或反向代理可能让多个 LAN 客户端在容器内呈现相同的 gateway/代理地址；因此 IP mapping 是 best-effort 回退，不是可靠认证。Local Server status 和部署文档必须显示该风险，稳定身份仍应使用显式 `device_id`。

IP/CIDR 映射只决定策略 Profile，不提供额外认证。DHCP、NAT、VPN 和 IPv6 隐私地址可能造成映射漂移；需要稳定归属时必须使用显式 `device_id`。

### 8.3 信任模型

所有设备共享同一管理和代理凭证，因此任意已认证设备可以声明其他 `device_id`。这是明确接受的自用系统边界。`device_id` 只影响策略选择，不提供隔离或授权。

Local Server 模式下 Web login 请求体和可选 `Authorization: Basic` 使用 canonical `auth_username/auth_password`；登录成功后仍通过 session cookie/Bearer session 访问管理 API。legacy 模式继续接受现有 password-only 登录兼容路径。管理 API 永远不读取 `Proxy-Authorization`，代理认证不能冒充管理认证。

## 9. 持久化

### 9.1 devices

```sql
CREATE TABLE devices (
    device_id     TEXT PRIMARY KEY,
    display_name  TEXT NOT NULL,
    revision      INTEGER NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);
```

`devices` 保存稳定设备资源和显示名称，不代表该设备拥有 independent Profile。删除 independent Profile 不删除该行。

### 9.2 device_profiles

```sql
CREATE TABLE device_profiles (
    device_id       TEXT PRIMARY KEY,
    profile_json    TEXT NOT NULL,
    schema_version  INTEGER NOT NULL,
    revision        INTEGER NOT NULL,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);
```

### 9.3 device_ip_mappings

```sql
CREATE TABLE device_ip_mappings (
    mapping_id  TEXT PRIMARY KEY,
    cidr        TEXT NOT NULL UNIQUE,
    device_id   TEXT NOT NULL,
    priority    INTEGER NOT NULL,
    enabled     INTEGER NOT NULL,
    revision    INTEGER NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);
```

CIDR 在写入前规范化。匹配顺序为 exact IP、最长网络前缀、较高 priority。规范化后的同一 CIDR 只能有一条记录。

Profile 和 mapping 写入必须在同一 store transaction 中确认目标 `devices.device_id` 存在。通过 Profile API 首次创建某个设备时，服务端可以在同一事务中创建对应 devices 行。

### 9.4 session generation

现有 `sessions` 表增加 `credential_generation INTEGER NOT NULL DEFAULT 1`。新 session 保存当前 generation；验证时必须与当前 `local_server.credential_generation` 相等。

凭证轮换递增 generation 后，所有旧 session 立即失效。后台清理可以随后删除旧 generation 的行，正确性不依赖清理完成。

### 9.5 migration 要求

- migration 在 SQLite transaction 中执行；
- 可重复运行；
- 不修改现有 node、stats、subscription 或 session token；
- 不创建默认 device Profile；
- 不创建自动 IP 映射；
- 任一步失败则完整回滚。

## 10. Profile 生命周期

### 10.1 创建

设备 Profile 可以通过两种方式创建：

- 提交完整 Profile；
- `copy-shared`：读取 shared 当前快照并创建 revision 1。

`copy-shared` 只是一次性复制，后续没有同步关系。

### 10.2 更新

device Profile 更新必须提交完整 Profile 和 expected revision。处理顺序：

```text
authenticate
-> load current snapshot
-> validate schema and bounds
-> compile static rules
-> validate provider URLs/specs
-> SQLite compare-and-swap transaction
-> build immutable registry candidate
-> atomic registry swap
-> start asynchronous provider generation
```

数据库 commit 之前不得改变运行时。registry swap 必须是不可失败的内存发布操作。

`PUT /devices/{device_id}/profile` 是 CAS upsert：Profile 不存在时要求 `expected_revision: 0` 或 `If-None-Match: *` 并创建 revision 1；Profile 已存在时要求匹配当前 revision。`copy-shared` 在 independent Profile 已存在时返回 `409`，不能静默覆盖。

shared Profile 更新走现有 YAML candidate-save 事务，并递增 `shared_revision`。旧 `/api/routing/config` 是 shared Profile 的兼容别名。

### 10.3 删除

删除 independent Profile 后：

- device identity 和 IP 映射保留；
- registry 原子切回 shared；
- 不改变 shared 内容；
- 删除不存在的 Profile 返回幂等成功。

### 10.4 provider 生命周期

每个 compiled Profile 有独立 provider generation。provider 初始网络获取异步执行，不能让配置 API 阻塞至网络超时。

每个 callback 携带 `profile_id + profile_revision + provider_generation`。任一值与当前 Registry 不一致时丢弃回调。provider 网络失败时继续使用静态规则和最后一次成功的 provider 规则，并在状态 API 中标记 degraded。

## 11. 数据面算法

```text
authenticate shared credential
-> parse RequestIdentity
-> resolve explicit device_id or IP mapping
-> select independent Profile if it exists, otherwise shared
-> if selected.enabled == false: DIRECT
-> otherwise evaluate selected Profile rules
-> DIRECT: net.Dialer
-> PROXY: Shared Pool with profile-scoped directive
```

### 11.1 开关矩阵

| independent Profile | selected enabled | shared enabled | 结果 |
| --- | --- | --- | --- |
| 不存在 | false | false | DIRECT |
| 不存在 | true | true | shared 规则和策略 |
| 存在 | false | 任意 | DIRECT |
| 存在 | true | false | independent 规则和策略 |
| 存在 | true | true | independent 规则和策略 |

当 selected Profile 关闭时，必须忽略 `nosplit`、`X-Proxy-Split`、路径 token、SOCKS token 和 `final_policy=PROXY`。

### 11.2 Pool directive

进入 Shared Pool 的 directive 增加：

- `profile_id`；
- profile revision；
- strategy；
- node filter；
- per-profile long-lived thresholds；
- per-profile session TTL；
- session/bucket key；
- request-level routing overrides。

sticky 和 session key 必须自动加入 `profile_id` 命名空间。节点健康、失败和黑名单状态继续共享；绑定和策略状态不能跨 Profile 共享。

profile-specific long-lived thresholds在选择阶段根据共享 monitor 的原始 uptime/success 数据评估，不能依赖一个全局预计算的 long-lived 布尔值。

### 11.3 idle 行为

BoxManager 没有可用节点时：

- dispatcher 继续监听；
- DIRECT 请求正常工作；
- 选择 PROXY 的请求返回明确错误；
- 节点恢复后无需重启 dispatcher。

## 12. 协议行为与错误

HTTP 和 HTTP CONNECT 每个请求重新解析身份和读取 Registry。keep-alive 连接上的后续请求可以看到新 Registry。

SOCKS5 在 RFC 1929 握手时解析身份；一次 CONNECT relay 使用握手时选定的身份。新连接读取新 Registry，已建立 relay 不强制中断。

| 场景 | HTTP/CONNECT | SOCKS5 |
| --- | --- | --- |
| 缺少或错误凭证 | `407 Proxy Authentication Required` | auth failure |
| base username 错误 | `407` | auth failure |
| 非法或重复 `dev=` | `400 Bad Request` | auth failure |
| DIRECT dial 失败 | `502 Bad Gateway` | mapped SOCKS failure |
| PROXY 但无可用节点 | `502 Bad Gateway` | general failure |
| Profile 更新失败 | 继续使用旧快照 | 继续使用旧快照 |

日志允许记录 `device_id`、identity source、profile ID、route decision 和 selected node tag，但不得记录共享密码、完整认证头、Cookie 或 session token。

## 13. 管理 API

所有接口继续由现有 `monitor.Server` 提供并复用同一管理认证。

### 13.1 Local Server

```text
GET /api/local-server/status
GET /api/local-server/config
PUT /api/local-server/config
```

状态包含 enabled、listen、dispatcher readiness、registry revision、credential generation、profile 数量和 mapping 数量。GET 配置只返回 `auth_username` 和 `password_set`。

Local Server 模式下管理认证接受 session cookie、Bearer session、`Authorization: Basic <canonical pair>`，以及现有 raw/Bearer canonical password 兼容路径；不接受 `Proxy-Authorization`。`POST /api/auth` 接受 username/password JSON。legacy 模式继续支持当前 password-only 请求体。

### 13.2 Profiles

```text
GET /api/local-server/profiles/shared
PUT /api/local-server/profiles/shared

GET    /api/local-server/devices
GET    /api/local-server/devices/{device_id}
PUT    /api/local-server/devices/{device_id}
PUT    /api/local-server/devices/{device_id}/profile
PATCH  /api/local-server/devices/{device_id}/profile/enabled
POST   /api/local-server/devices/{device_id}/profile/copy-shared
DELETE /api/local-server/devices/{device_id}/profile
```

设备列表合并 persisted devices、profiles、IP mappings 和 process-local activity observations。`last_seen_ip/at` 是 best-effort 运行时信息，不参与配置正确性。`PUT /devices/{device_id}` 创建或更新显示名称，并使用 devices revision 做并发控制。

### 13.3 IP mappings

```text
GET    /api/local-server/ip-mappings
POST   /api/local-server/ip-mappings
PUT    /api/local-server/ip-mappings/{mapping_id}
DELETE /api/local-server/ip-mappings/{mapping_id}
```

使用 `mapping_id` 而不是把带 `/` 的 CIDR 放入 URL path。请求体包含 `cidr`、`device_id`、`priority`、`enabled` 和 expected revision。

### 13.4 optimistic concurrency

所有 Profile 和 mapping 写入接受 `expected_revision` 或 `If-Match`。device revision 来自 SQLite；shared revision 来自 YAML `local_server.shared_revision`。

revision 过期返回：

```json
{
  "error": "profile_revision_conflict",
  "current_revision": 4,
  "need_reload": false
}
```

完整 Profile 只允许完整 PUT。只有 enabled 使用专门 PATCH。服务端不进行隐式 JSON merge。

### 13.5 状态码

- `401`：没有有效管理 session 或管理凭证；`Proxy-Authorization` 不被管理 API 接受；
- `409`：revision 冲突、reload window 或 canonical credential 冲突；
- `422`：Profile、规则、provider、device ID 或 CIDR 无效；
- `500`：持久化或发布失败，旧运行时继续生效。

成功写响应包含 revision、registry revision、`need_reload` 和资源摘要。

## 14. 热更新、reload 与凭证事务

| 变更 | 动作 | need_reload |
| --- | --- | --- |
| Profile enabled | Registry swap | false |
| rules/final policy | compile + Registry swap | false |
| strategy/filter/session/long-lived | Registry swap | false |
| IP/CIDR mapping | Registry swap | false |
| rule provider spec | Registry swap + async provider | false |
| local_server.enabled | topology reload | true |
| local_server.listen | listener transition | true |
| shared credential（Local Server 模式） | atomic credential snapshot + session generation | false |
| shared credential（legacy 模式） | existing credential reload path | true |
| MiSub/source/nodes/GeoIP | existing BoxManager reload | true |

ProfileManager 必须遵守现有 `configUpdateMu`、reload window 和 BoxManager mutation barrier。异步操作不得用 reload 前捕获的 Config 或 Registry 覆盖新状态。

### 14.1 credential rotation

Local Server 模式下凭证轮换不尝试在同一个 `host:port` 上 pre-bind 第二个 socket。管理 API 与 dispatcher 从同一个原子认证快照读取凭证，现有 listener 保持不变。

凭证轮换顺序：

```text
validate candidate
-> build new in-memory credential snapshot
-> atomically persist canonical credential + increment generation
-> atomically publish management and dispatcher auth snapshot
-> invalidate old-generation sessions
```

认证快照发布是不可失败的指针替换；持久化失败时不发布新快照。配置文件原子写失败或 reload barrier 冲突时保留旧配置、旧 snapshot 和旧 session。不能出现管理面接受新密码但代理仍接受旧密码的窗口。已有 relay 连接继续完成，新请求使用新凭证。

## 15. Web Console

现有 React/Vite 管理台新增一级 `devices` 页面，显示为“设备策略”。

页面包含：

1. shared Profile 摘要和编辑入口；
2. 设备列表；
3. independent Profile 创建、启停、编辑和删除；
4. IP/CIDR mapping 管理；
5. effective mode、effective enabled、last seen 和 revision 状态。

抽取 `ProfileEditor`，由 `scope = shared | device` 控制 API 路径。它复用现有 RoutingPanel 的规则和策略控件，不复制整个 SettingsPanel。

ProfileEditor 的 URL 映射固定为：shared 使用 `/api/local-server/profiles/shared`，device 使用 `/api/local-server/devices/{device_id}/profile`。成功 DTO 必须包含 `revision`、`registry_revision`、`need_reload` 和 `profile_scope`，不能依赖旧 RoutingPanel 的隐式全局 endpoint。

Settings 页面继续只管理服务器级字段：listener、management、canonical credential、MiSub/source sync、subscription、GeoIP 和日志。

创建 independent Profile 时提供：

- 从 shared 做一次性 snapshot；
- 使用默认空白 Profile。

UI 必须明确“独立配置不会跟随共享配置变化”。删除确认必须明确“删除后回到 shared，device ID 和 IP mapping 保留”。

revision 409 时不得自动覆盖或自动 merge；用户可以查看服务器当前版本或放弃本地编辑。未保存内容切换设备或页面时需要提示。

前端继续复用 `api/client.ts`；新增强类型 DTO 和结构化 `ApiError`。新增 Vitest 与 React Testing Library，不建立第二套 API client。

## 16. 迁移与兼容

### 16.1 旧凭证迁移

1. 已存在 canonical credential 时直接使用。
2. management 和 listener 的非空密码一致时自动迁移。
3. management/listener 中只有一套非空密码时使用它并补默认 base username。
4. management 和 listener 的非空密码冲突时拒绝迁移，保持旧运行时。
5. 成功迁移后 management/listener 字段成为派生兼容字段，不能再独立覆盖 canonical credential。
6. multi-port 和 extra listener 的 legacy 凭证不参与迁移，因为这些入口不能与 Local Server 模式同时启用。
7. canonical base username 优先使用合法的现有 listener username，否则使用默认 `easyproxy`。
8. 首次启用 Local Server 必须递增 credential generation，使启用前创建的所有 Web session 失效。

### 16.2 旧 Smart Routing

未启用 Local Server 时，`routing.enabled`、plain pool inbound、multi-port 和现有 API 行为保持不变。

Local Server 模式下：

- `/api/routing/config` 和 `/api/routing/status` 是 shared Profile 的兼容别名；
- 响应增加 `profile_scope: shared`；
- legacy multi-port/hybrid/extra listeners 被配置校验拒绝，避免绕过 Local Server Profile；它们仅在 Local Server 关闭时保持旧行为；
- 标准客户端和文档使用统一 dispatcher 入口。

## 17. 部署与安全

标准端口保持：

- `22323`：统一 HTTP/SOCKS5 proxy；
- `29888`：Web Console 和管理 API。

不增加容器、服务或 LAN 设备端 daemon。远程 MiSub、Aggregator 和 Worker 部署保持不变。Local Server 标准部署必须使用 `mode: pool`；legacy multi-port/hybrid 需要显式关闭 Local Server。

根 `config.yaml`、config renderer、deployment template、GHCR bootstrap、placeholder validation、README 和 quickstart 必须支持 canonical Local Server 配置并派生兼容字段。

Local Server 默认运行在可信 LAN。部署文档必须要求防火墙拒绝 WAN、Guest VLAN 和不可信网络访问 `22323/29888`。明文 HTTP 不适合跨不可信网络；此类访问必须通过 VPN 或 TLS reverse proxy。

Docker bridge/published port 不保证保留 LAN 源 IP。Web Console 必须把 IP mapping 标记为 best-effort，并在 status 中显示当前 peer-address 模式和警告。需要可靠 IP fallback 时，部署必须使用经过验证的 source-preserving 网络模式；否则依赖显式 `device_id`。

## 18. 测试

### 18.1 Go

必须覆盖：

- config defaults、Clone、SaveSettings 和 legacy compatibility；
- credential migration 和冲突拒绝；
- SQLite migration、CRUD、CAS revision 和 session generation；
- username grammar、device ID validation 和 IP/CIDR precedence；
- shared/device selection 与开关矩阵；
- ProfileRegistry atomic publication 和 failed persistence rollback；
- provider generation fencing；
- per-profile sticky/session/long-lived isolation；
- HTTP、CONNECT、SOCKS5 一致身份语义；
- dispatcher 在 BoxManager idle 时继续支持 DIRECT；
- credential rotation rollback 和旧 session 失效；
- management API revision、reload window 和 compatibility aliases。

### 18.2 Frontend

执行：

```text
npm run lint
npm run test
npm run build
```

测试 shared/independent 矩阵、Profile CRUD、copy-shared、删除回 shared、scope URL、409、401、未保存提示和密码不回填。

### 18.3 Isolated Docker E2E

使用新的 image tag、container、network、config directory 和 host ports。至少包含 counted upstream proxy、DIRECT origin、PROXY origin、device A 和 device B。

必须以 upstream counter 证明：

- shared off + 无 independent -> DIRECT；
- shared off + independent on -> PROXY；
- shared on + independent off -> DIRECT；
- 修改 shared 不改变 independent；
- 删除 independent 后回 shared；
- explicit device ID 覆盖 IP mapping；
- Web、HTTP、CONNECT 和 SOCKS5 使用同一共享凭证。

测试不得启动、停止、复用或替换 legacy `easy-proxy` 容器。两个未跟踪 OCI tar 文件不得修改、删除或提交。

## 19. 完成标准

以下条件全部满足才能宣布完成：

1. shared 和 independent 开关矩阵通过单测、协议测试和 E2E。
2. independent Profile 不继承、不合并、不被 shared 更新改变。
3. explicit device ID 和 IP/CIDR fallback 真实生效。
4. Web、management API、HTTP proxy 和 SOCKS5 使用 canonical credential。
5. credential rotation 不产生新旧认证分裂窗口，旧 session 失效。
6. revision 并发保护和失败回滚通过。
7. legacy Smart Routing 未启用 Local Server 时保持原行为。
8. Remote MiSub、subscription、connector 和 node sync 没有语义变化。
9. 前端桌面和移动布局通过真实浏览器检查。
10. Go、frontend、config renderer、deployment smoke 和 isolated Docker E2E 全部通过。
11. legacy 容器和未跟踪 tar 文件保持不变。
12. 规格、实施计划、配置示例和用户文档同步完成。

## 20. 代码落点

预期实现边界：

- `service/base/internal/profile/`：Profile 类型、Registry、Resolver、Manager、provider lifecycle；
- `service/base/internal/config/`：LocalServerConfig、canonical credential、compat normalization；
- `service/base/internal/store/`：device profile、IP mapping、session generation migration 和 CRUD；
- `service/base/internal/dispatch/`：device token 解析、Profile resolution、always-on Local Server data path；
- `service/base/internal/outbound/pool/`：profile-scoped directive、sticky/session/long-lived 选择；
- `service/base/internal/app/`：ProfileManager、dispatcher、monitor wiring；
- `service/base/internal/boxmgr/`：Local Server topology 和 credential transaction integration；
- `service/base/internal/monitor/`：Local Server API、auth/session generation、embedded asset；
- `service/base/frontend/src/`：DevicesPanel、ProfileEditor、API DTO、tests；
- root/deploy scripts and docs：canonical config rendering、LAN deployment guidance、smoke tests。

该边界不会修改远程 source adapters 的业务语义，也不会把 Profile ID 注入节点存储或远程订阅协议。
