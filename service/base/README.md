# EasyProxy

EasyProxy 是一个轻量级、高性能的代理池与订阅管理工具，底层基于 [sing-box](https://github.com/SagerNet/sing-box)。
项目内置现代化 Web 管理面板，支持节点健康检查、订阅刷新、流量监控与可视化管理。

> 二开声明：本项目基于 [jasonwong1991/easy_proxies](https://github.com/jasonwong1991/easy_proxies) 二次开发，V2 版本重点重构了前端与工程化流程。

## ❤️ 赞助木木

木木是独立开发者 / 开源爱好者，长期投入开源项目维护与迭代。
如果 EasyProxy 对你有帮助，或者你认可我的工作，欢迎请我喝杯咖啡。你的支持是我持续创造的动力源泉 ⚡

- [赞助地址](https://mumuverse.space:1588/)

---

## ✨ 核心特性

- 现代化 Web UI（React + Vite + Tailwind + DaisyUI）
- 前后端一体化（前端静态资源已内嵌到 Go 二进制，单文件即可运行）
- 节点订阅与自动刷新
- 代理池智能调度与故障隔离
- GeoIP 分区路由（可选）
- Local Server 局域网统一入口与共享/独立设备 Profile
- SQLite 持久化存储运行状态与统计数据

## 🖼️ 项目预览

![项目预览 1](./docs/screenshots/1.png)
![项目预览 2](./docs/screenshots/2.png)
![项目预览 3](./docs/screenshots/3.png)
![项目预览 4](./docs/screenshots/4.png)
![项目预览 5](./docs/screenshots/5.png)

---

## 🚀 原生发布包（Linux / Windows）

无需本地安装 Go 和 Node。从所用 Fork 的 GitHub Release 下载对应平台的
服务包、同平台的 `easyproxyctl` 包，以及 `SHA256SUMS` 和
`release-manifest.json`：

| 平台 | 服务包 |
| --- | --- |
| Linux amd64 | `easyproxy-linux-amd64.tar.gz` |
| Linux arm64 | `easyproxy-linux-arm64.tar.gz` |
| Windows amd64 | `easyproxy-windows-amd64.zip` |

Windows arm64 不在发布支持范围。服务包内的可执行文件是
`bin/easy-proxy` 或 `bin/easy-proxy.exe`，并附带配置模板、ECH helper 和安装脚本；
不是旧版按平台命名的裸二进制。

安装、校验、升级和回滚统一遵循
[`原生安装与更新`](../../docs/deployment.md#native-install-update)，避免维护第二套安装步骤。
保留安装目录中的 helper 与配置路径，不要只复制 EXE 后假定所有 connector 都能运行。

管理面板默认监听 `http://127.0.0.1:29888`，配置模板的管理密码为空，
没有预设的通用登录密码。绑定非 loopback 地址必须配置非空密码；启用 Local Server
时使用其 canonical 用户名/密码。实际地址和认证以所选运行时配置为准。

---

## ⚙️ 配置说明（最小必读）

配置模板见 `config.example.yaml`，重点关注：

- `mode`: `pool` / `multi-port` / `hybrid`
- `listener`: 代理入口监听与认证（新增 `listener.protocol`: `http` / `socks5` / `mixed`）
- `multi_port`: 多端口入口参数（新增 `multi_port.protocol`: `http` / `socks5` / `mixed`）
- `management.listen`: Web 管理面板地址（模板默认 `127.0.0.1:29888`）
- `management.password`: 面板登录密码（为空则不需要登录）；服务间调用时也可直接作为 `Authorization` 头值访问管理 API
- `subscriptions` / `nodes_file` / `nodes`: 节点来源（三选一或混用）
- `source_sync.*`: 从 MiSub 拉取 machine manifest，并在失败时启用本地 fallback 订阅
- `source_sync.connector_runtime.*`: 当 manifest 含有 `connector_type = ech_worker` 时，本地拉起 `ech-workers` 并转换成临时上游代理
- `connectors[]` / manifest `connector`: 也支持 `connector_type = zenproxy_client`，运行时会请求 ZenProxy `/api/client/fetch` 并把返回的 sing-box outbound 转成临时节点
- 纯 `source_sync` / 纯 connector 场景现在也可直接启动，不再需要本地占位节点
- `routing.*`: 智能分流入口（默认关闭）。开启后接管 `listener` 端口，HTTP/HTTPS 与 SOCKS5 同端口共存，提供「规则分流（直连/代理）+ 选节点策略（stable/session/auto）+ 节点属性筛选（国家/地区/长效）」三层能力。默认入口（系统代理、无参数）走「中国直连 + 其余 stable 长效稳定」；API 调用可用路径前缀 / `X-Proxy-*` 头 / SOCKS5 username 令牌覆盖。详见 [`docs/routing.md#smart-routing`](../../docs/routing.md#smart-routing)
- `local_server.*`: 局域网 Local Server（默认关闭）。开启后要求 `mode: pool`、`listener.protocol: mixed`，由一套 canonical 用户名/密码同时保护 Web、管理 API、HTTP/CONNECT 和 SOCKS5；支持 shared Profile、完全独立的设备 Profile，以及显式 `+dev=<device_id>` / IP-CIDR 映射选择。设备无需安装独立客户端。详见 [`docs/local-server.md`](../../docs/local-server.md)
- `gateway.*`: Linux/NAS 原生透明 TCP 网关（默认关闭）。支持物理 LAN、Tailscale、星空组网等承载层，通过接口/CIDR 接入同一 EasyProxy 规则和节点池；无可用节点时按 fail-open DIRECT。需要 host networking、`NET_ADMIN`/`NET_RAW` 和宿主机 forwarding/policy routing。详见 [`docs/routing.md#transparent-gateway`](../../docs/routing.md#transparent-gateway)

Local Server 的 IP 映射只读取实际 TCP peer address。Docker bridge、端口
映射和 NAT 可能让多个设备显示为同一个网关 IP，因此稳定设备选择应优先
使用显式 `easyproxy+dev=<device_id>`。部署时必须在路由器和宿主机防火墙上
阻止 WAN、Guest VLAN 和其他不可信网络访问 `22323/29888`；跨不可信网络
请先使用 VPN，或配置带明确访问策略的 TLS 入口。

管理 API 的可观测性补充：

- `GET /api/source-sync/status`
  - 返回当前 manifest / fallback / connector 的总体同步状态
- `GET /api/source-sync/source-health`
  - 按 `source_ref` 聚合返回 `total/effective_available/blacklisted/pending`
    等计数
  - 可用 `?source_ref=manifest:conn_zenproxy_primary` 精确查看某个
    provider 当前 materialize 出来的线路健康度
- `GET /api/debug`
  - 现在也会附带 `source_ref` / `source_name` / `source_kind` 与
    `effective_available` 等字段，方便逐节点排查
  - 每个节点还附带 `long_lived` / `uptime_seconds`，用于长效节点判定
- `GET /api/routing/status`
  - 仅在 `routing.enabled` 时有意义，返回智能分流入口的监听地址、默认策略、
    规则数、兜底策略，以及当前 stable 桶 / session 会话的粘性绑定快照
- `GET /api/gateway/status`
  - 返回透明 TCP listener、规则应用状态、活动连接、DIRECT/PROXY 计数、
    无可用节点时的 DIRECT 回退次数和最近错误
- `GET /api/local-server/status`
  - 返回 Local Server listener/dispatcher、Registry revision、Profile/mapping
    数量，以及 Docker/NAT source-IP 风险提示
- `GET /api/local-server/devices`
  - 返回设备的 shared/independent 模式、有效启用状态、revision、last-seen
    和 mapping 数量；完整 CRUD 与 CAS 说明见 Local Server 文档

---

## 🧪 从源码构建（开发者）

项目由 Go (1.24+) + Node (22+) 构成。

### 1) 构建前端
```bash
cd frontend
npm ci
npm run build
```

### 2) 构建后端
```bash
cd ..
go mod download
go build -tags "with_utls with_quic with_grpc with_wireguard with_gvisor" -o easy-proxy ./cmd/easy_proxies
```

---

## 📦 Docker（可选）

容器构建与 Compose 资产由仓库的 `deploy/service/base` 维护，本模块目录没有
独立的 `docker-compose.yml`。从仓库根目录按
[`本地 EasyProxy 部署`](../../README.md#local-easyproxy-deployment) 使用统一部署入口；
NAS 固定镜像部署见 [`deploy/nas`](../../deploy/nas/README.md)。

如果你的运行时配置不准备使用默认的
`/var/lib/easyproxy/config/config.yaml`，
可以改为设置环境变量 `EASY_PROXY_CONFIG_PATH`。入口脚本会在真正启动
`easy-proxy` 时优先使用这个路径，而不是镜像内默认 `CMD` 里的占位值。
镜像中的 `/etc/easyproxy/config.yaml` 只用于首次启动和旧部署迁移，不是
WebUI/API 的持久化写入目标。

需要并行验证时，显式覆盖容器名、Compose project、端口与网络别名，
不要依赖历史迁移时期的默认命名来隔离实例。

## 📁 目录结构简述

- `cmd/easy_proxies/`: Go 程序入口
- `frontend/`: 前端源码
- `internal/`: 后端核心模块
- `internal/monitor/assets/`: 前端构建产物（会被 Go embed）
- `../../.github/workflows/publish-github-release.yml`: 仓库统一构建与发布流程

---

## 🙏 鸣谢

- 原作者 [jasonwong1991/easy_proxies](https://github.com/jasonwong1991/easy_proxies)
- 核心代理引擎 [sing-box](https://github.com/SagerNet/sing-box)
