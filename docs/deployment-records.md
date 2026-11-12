# EasyProxy 部署与恢复记录

## 适用范围

本文按主题集中维护相关合同；部署记录仅描述其标注日期的事实，不代表实时状态。

以下各节的“当前”“已上线”“保留”以及“尚未提交/没有提交”均指该次记录的核验时点，不表示今天的部署、备份存在性或 Git 状态。后续操作前应重新核对实际主机与仓库，不能直接重放历史脚本；后续补充仅覆盖其明确说明的早期状态。

## 目录

- [2026-11-08 云端可用池验收](#cloud-usable-pool-2026-11-08)
- [2026-11-11 NAS 与 PC2 验收](#nas-pc2-acceptance-2026-11-11)
- [2026-11-12 默认网关部署](#default-gateway-deployment-2026-11-12)

<a id="cloud-usable-pool-2026-11-08"></a>

## 云端可用池 1.0.20 上线与验收记录

日期：2026-11-08，北京时间。本文是有时间边界的上线回执，不替代后续健康状态。
模式与失败语义见 [云端可用池与本地 connector](cloud-operations.md#cloud-usable-pool)。

### 最终部署身份

| 项目 | 已验收值 |
| --- | --- |
| 应用版本 | `1.0.20` |
| 运行源码 | `103fdb250ad48536ee06d14a887021e803c72511` |
| Release | [v1.0.20](https://github.com/aiaimimi0920/EasyProxy/releases/tag/v1.0.20) |
| SpringBok 环境 | `easyproxy-main` |
| 唯一活动实例 | `dc-92bbbbc744784ec6adf13a37ddd68f97` |
| 本次更新任务 | `dc-3b6f52c17fd94c20b4d59583b061da6b` |
| 执行 run | `42410244681` |
| 发布 operation | `1e26a42977d5442eb59200f6ed63e367` |
| NAS 宿主 | Debian VM `192.168.15.201`，不是 DSM `.200` |
| NAS 容器 | `easy-proxy-gateway` |
| NAS 镜像 | `easyproxy/nas:1.0.20-103fdb250ad4` |
| NAS 实际镜像 ID | `sha256:2932f315c0257bb47c5d836395eca6051f2dfddc8f9cf9472a327e87405af105` |
| 网关二进制 SHA-256 | `5c955e3734daa13e339d6d6c45723f5e7f236fb348ef89f2218c0620d568c0d1` |
| 当前 Compose | `/opt/easyproxy-gateway/compose/docker-compose.current-1.0.20.json` |
| Compose project | `easyproxy-gateway-1-0-20` |

本次是同实例正常更新，复用原 D1、R2 和域名，没有创建第二套服务。
SpringBok 应用回执 `status=succeeded`，`backup-created`、`data-preserved`、
`service-ready`、`suite-published` 全部通过。

`1.0.19` 曾创建 Release，但其空修复声明被 SpringBok 在创建部署任务前拒绝，未部署、
未产生新的生产 UNKNOWN。`1.0.20` 移除了已退休的 `actions.repair` 和空 `repairs` 声明，
未重放旧修复任务。旧 tag、Release 和历史回执保留，不代表仍有旧运行实例。

### 本次修复了什么

此前普通订阅只在 MiSub 独立登记，没有接入 agg 的生成与真实消费审计链路；NAS 的
Global 因而不会自动获得这些订阅的可用节点。本次将全部已启用的普通订阅接入每轮生成：

1. 从准确 MiSub 项目读取普通订阅登记，重新下载每个来源，并检查每源确实贡献候选节点。
2. 使用批准的 agg 转换器，与 agg 候选合并、去重，再经 EasyProxy 运行时实际审计。
3. Global 只发布审计通过的实际代理 URI；`subscriptions=[]`，不下发普通订阅原始 URL。
4. 剔除上一轮用户订阅候选摘要对应的旧节点，避免每日轮换或来源禁用后由 `remains` 缓存带回。
5. ZenProxy 与 ECH 仍发布 connector 参数，由 NAS 展开并与全部节点一起接受本地健康检查。

同时修正了批准 agg 的非标准本地文件读取协议适配，并用真实生成验证两条普通订阅均有贡献。
未把下载成功或格式转换成功当作节点可用，也未在失败时发布漏源的半批池。

### 云端验收

北京时间 2026-11-08 05:01:16 的独立只读核验结果：

- active 为 `1.0.20`，源码精确匹配，writer idle。
- Pages、Worker 公开版本匹配；stable、immutable、公开七产物对应本次 operation 且内容一致。
- 本轮共 **381 个候选节点，141 个云端审计可用节点**。
- Global 为 **141 个实际代理 URI + ZenProxy/ECH 两个 connector**，无普通订阅原始 URL。
- 普通订阅合计贡献 **13 个云端审计可用节点**。来源内容会轮换，不能用较早下载的逐源匹配
  数量冒充本轮每个来源的完整贡献数。
- D1 保留 145 条来源记录（141 托管 URI、2 普通订阅登记、2 connector）、2 个 profile、
  1 条 settings、0 条 cron execution；非托管业务记录、用户 profile 字段和设置已逐项对账。
- 更新前的加密备份已持久保存并独立回读：
  `_sba/backups/1e26a42977d5442eb59200f6ed63e367/misub.age`，16634 bytes，
  SHA-256 `ea0bd20f245e6abbf6a976aa8474906b45691adb902fe7eedc7da7336fd960be`。

云端审计可用不等于在 NAS 网络环境下一定可用，本地仍必须重新检查。

### NAS 验收与实际传输

北京时间 **2026-11-08 05:10:40** 的最终节点快照：

| 类别 | NAS 已收到/展开 | NAS 本地有效可用 |
| --- | ---: | ---: |
| 云端审计池 | 141 | 49 |
| 其中：两条普通订阅贡献，已包含在上一行 | 13 | 7 |
| ZenProxy connector 展开 | 10 | 0 |
| ECH connector 展开 | 6 | 1 |
| 合计，不重复计算普通订阅子集 | **157** | **50** |

141 个云端 URI 均按协议、端点和认证字段与 NAS 逐一匹配。NAS 本地普通订阅数为 0，
不保存两条普通订阅的原始 URL。source-sync 健康，无最后刷新错误；网关 applied、
TUN ready，IPv4 启用、IPv6 透明接管未启用；容器 restartCount 为 0，无 panic/fatal。

北京时间 05:09:54 的有界真实传输验证，两项均启用 TLS 证书校验：

- HTTP CONNECT + TLS：Google `generate_204` 返回 **204**，0.719 秒。
- SOCKS5 + TLS：返回 **HTTP/1.1 204 No Content**，0.453 秒。

这证明当时可实际代理出网，不代表全部第三方节点、所有业务消费者或长期稳定性均已验收。
ZenProxy 较早发生 timeout/403，随后准确请求返回 200 并成功取得 10 节点；这批节点未通过
NAS 本地可用性检查，未强行标记健康。早期失败回执仍保留。

#### NAS 健康检查窗口调整

节点池扩大后，原 `subscription_refresh.health_check_timeout=1m0s` 不足以完成整轮
有界并发探测，曾报 `reload health check failed: context deadline exceeded` 并正确保留旧池。
通过正常设置接口仅将该窗口调为 **3m0s**，`min_available_nodes=1` 保持不变；其他配置
语义逐项对账未变。随后正常刷新成功，没有绕过最少可用节点门禁。该项是部署容量配置调整，
没有新增程序热补丁。

### 定期刷新边界

- 云端每六小时执行：UTC `25 */6 * * *`，北京时间 02:25、08:25、14:25、20:25。
  每轮重新读取普通订阅、下载并审计，使用当前 active 批准源码。
- NAS source-sync 每 **1 小时**更新可用池与 connector，并继续本地健康检查。
- 周期配置和对应代码路径已核对；本次正常更新与手动刷新已真实成功。
  **本回执未等待 1.0.20 的下一次自然定时任务，不能把它写成已执行成功。**

### 测试与发布证据

- 入池实现 `bcc3d5fcc1eeabe8479fc608e1ffb6642492c505` 的 Linux SBA：
  **1994 passed / 51 skipped**；真联网双来源生成得到 381 候选，每源均有贡献。
- NAS 完整镜像原生 TUN E2E：`ALL_TUN_E2E_PASS`。
- `1.0.20` 相比该实现仅修改 manifest 和声明测试；3 项定向测试通过，真实 SpringBok
  validator 返回 `SPRINGBOK_MANIFEST_PASS 1.0.20`。未重复声称整套 SBA 在 20 又运行了一次。
- `1.0.20` 的 GitHub Validate run `42409758894`：`Validate Repository` 与
  `Native TUN Dual-Stack E2E` 均 success。
- 20 的 NAS RootFS 和运行配置（除标签外）与通过原生验收的 19 一致，网关二进制 SHA 相同。
- Release tag 精确指向运行源码；本次收尾文档提交不会改变已部署程序身份，也不另建版本。

### 备份与经批准的清理

NAS 切换前已做 SQLite online backup 和停止态快照，完整性检查为 `ok`，备份位于
`/opt/easyproxy-gateway/backups/cutover-1.0.20-103fdb250ad4`，并有本机 DPAPI 密封副本。
停止态快照 558275 bytes，SHA-256
`309b9ee9380ef51a6c72845986298a07cafdd812829e34b9cd342944a0c66a7d`。

用户逐项明确同意后，已删除：

- 停止的 `easy-proxy-gateway-rollback-pre-1.0.20` 容器。
- NAS 旧镜像 `easyproxy/nas:1.0.18-b3c796c15cbf`、`easyproxy/nas:1.0.19-bcc3d5fcc1ee`。
- `/opt/easyproxy-gateway/compose/docker-compose.current-1.0.18.json`。
- `/home/mjc/easyproxy-upload-1.0.19-bcc3d5fcc1ee/`，其中仅 `image.tar`。

清理未使用 force/prune。当前容器身份、运行状态和 restartCount 未变；NAS 的
`easyproxy/nas` 镜像只剩 1.0.20，一套主网关运行。未删除当前配置、数据库、备份、
云端 UNKNOWN/发布历史或 Git 历史。清理旧运行副本不等于删除恢复和审计记录。

### 本地证据与接入入口

脱敏回执保存在 `C:/Users/Public/nas_home/AI/GameEditor/linshi/easyproxy-ops-20261106`：

- `r20-dc-3b6f52c17fd94c20b4d59583b061da6b-receipt.safe.json`
- `r20-final-cloud-verification.safe.json`
- `r20-final-acceptance.safe.json`
- `r20-final157-transport.safe.json`
- `r20-health-window.safe.json`
- `r20-nas-cleanup-result.safe.json`
- `r20-release.safe.json`

唯一当前接入凭据仍为本地 `C:/Users/Public/nas_home/aikey/密钥/EasyProxy-当前接入.json`；
本地运维说明位于 `C:/Users/Public/nas_home/aikey/部署/EasyProxy/README.md`。
凭据和原始订阅内容不进入产品 Git。本次保留其他任务的未跟踪材料，不做额外清理。

<a id="nas-pc2-acceptance-2026-11-11"></a>

## NAS 新版与 PC2 默认接入验收（2026-11-11）

### 部署边界

- NAS 宿主机：192.168.15.200。
- EasyProxy 实例：NAS 内 Debian 虚拟机 192.168.15.201，目录 `/opt/easyproxy-gateway`。
- Linux PC2：192.168.15.104，用户 mjc。只消费 NAS 服务，没有安装另一套 EasyProxy 本体。
- Windows 台式机 192.168.15.20 与笔记本 192.168.15.136 保持既有接入。

### 本次修复与版本

部署构建基于分支 `fix/shared-tier-hot-refresh` 的提交 `7f6c585ea316aa8320e4bfaa0edc4bc4f18cafd2`，叠加随本验收记录一同提交的源码修复：

1. manifest 超时最多重试两次，退避与请求共用配置的总超时预算；鉴权失败、无效响应、取消不重试。部署请求预算为 60 秒。没有改为忽略错误或使用未经确认的历史 runtime 节点。
2. TUN Fake-IP DNS 按启用地址族设置单栈策略，IPv4-only 下 AAAA 返回 NOERROR 空答案，不再因缺少 IPv6 Fake-IP 地址池而超时。双栈保持原行为。

最终镜像：`easyproxy/nas:shared-dbip-7f6c585-manifest-dns1`。

- OCI image ID：`sha256:f9f413111621592285adf9a2c18b274f12dd267bcbce26730138f5579115c1b4`
- ELF SHA256：`74ce8765414e41f60c4ffe6ad5e3edf170ba557ea816f6c20ea64eaaf7901689`
- Compose：`/opt/easyproxy-gateway/compose/docker-compose.shared-dbip-manifest-dns1.json`
- 启动时间：2026-11-11 13:07:28 UTC。
- 实际启动日志出现一次 `manifest request timed out; retrying (1/2)`，随后正常就绪，验收时 RestartCount 为 0。
- 美国优先、通用池回退及 DB-IP 数据配置已生效。

### PC2 持久配置

- NetworkManager 连接 `pc2-lan` 的 IPv4 默认网关为 `192.168.15.201`。
- NAS TUN 当前只启用 IPv4，PC2 设置 `ipv6.never-default=yes`；保留 IPv6 地址与局域网功能，不声明已提供公网 IPv6 代理。
- DNS 使用 `1.1.1.1` 作为查询目标，流量经默认网关被 NAS TUN DNS 接管。它不是绕过 NAS 的直连 DNS。
- NAS 的 `.201:53` 没有独立 DNS socket，不能将该地址直接当作本部署模式的 DNS 服务器。
- `/etc/NetworkManager/conf.d/90-easyproxy-nas-dns.conf` 固定全局 DNS，避免 StarVPN 覆盖连接级 DNS；保留原有 `no-aaaa` resolver 选项。显式 AAAA UDP/TCP 查询仍另外验证成功。
- GNOME 系统代理为 `none`，无需应用配置 HTTP/SOCKS 或设置代理环境变量。
- FlClash UI/Core 已停止，`flclash-helper.service` 已禁用。其文件与原始配置未删除，原配置 `autoLaunch=false`。
- StarVPN、Tailscale 及其他应用未停用。

### 实际验收

- `subscription`、`app`：85 项通过；`builder`、`gateway`：51 项通过。不是全仓库测试声明。
- 三台电脑各自 HTTP 与 SOCKS 请求均返回 HTTPS 204，共 6 条；NAS 宿主机透明请求也返回 204。
- PC2 经透明网关的 A 查询返回 Fake-IP；AAAA 的 UDP 和 TCP 查询立即返回 NOERROR、零 Answer。
- PC2 普通 curl 不带 `-4`、不指定代理：HTTPS 204 成功。
- PC2 Cloudflare trace 返回 `loc=US`。
- PC2 完整接收 1,048,576 字节下载，HTTP 200。
- PC2 到台式机、笔记本、NAS 主机的 SSH 登录命令成功；到虚拟机 SSH 端口可达。
- 重新加载 NetworkManager 并重新应用连接后，默认网关、DNS 与普通 HTTPS 请求仍正常。
- 最终观察：NAS 147 个节点，38 个可用，其中 15 个美国节点可用。该数量会动态变化。
- 未重启 NAS 或 PC2 整机；未验证长期运行或完整业务应用矩阵。

### 保留的恢复资料

NAS 最终切换前备份：

`/opt/easyproxy-gateway/backups/cutover-shared-dbip-7f6c585-20261111T130722Z`

更早原版备份与容器也保留，包括 `20261111T124829Z` 和交接中的两次失败备份。不要直接重放旧切换脚本；它们绑定旧容器与配置哈希。

PC2 本次最终切换前备份：

`/home/mjc/easyproxy-nas-access-20261111-v2`

包含 NetworkManager 原属性、系统代理、FlClash 配置副本、验收记录和恢复脚本。安全回滚计时器已停止，`accepted` 标记已写入。如明确决定恢复 PC2 旧接入，可在核对当前配置无后续改动后执行：

```sh
sudo python3 /home/mjc/easyproxy-nas-access-20261111-v2/switch.py rollback --force
```

本地构建、脚本、源码哈希与验收 JSON：

`C:/Users/Public/nas_home/AI/GameEditor/linshi/easyproxy-device-tiers/shared-dbip-7f6c585`

关键文件：`final-source-provenance.json`、`final-source.patch`、`final-device-flows.json`、`final-pc2-acceptance.json`。实际部署不是纯 `7f6c585`；对应修复与测试随本验收记录一同纳入版本控制，临时构建产物与备份不纳入仓库。

### PC2 StarVPN 卸载结果

同日用户另行批准卸载 StarVPN。通过 apt purge 仅移除 `starvpn 6.1.0`，未执行依赖自动清理。已核实程序目录 `/usr/local/lib/stars`、命令软链接、`stars-service.service`、StarVPN 虚拟网卡及其地址路由均消失，无相关运行进程。Tailscale、Docker 和 NAS EasyProxy 未停用，FlClash 恢复资料未删除。

卸载前备份与日志位于 PC2 `/home/mjc/starvpn-removal-20261111`，目录权限 0700；包含程序与配置归档、网络基线、卸载日志及检查结果。备份可能含凭据，不应公开或提交仓库。

卸载后重复验证：DNS A、AAAA UDP/TCP、默认 HTTPS 204、完整 1 MiB 下载以及四个局域网 SSH 端口均通过；IPv4 默认网关仍为 `192.168.15.201`。本地记录为 `post-starvpn-removal-acceptance.json`。

两条既有规则（IPv4/IPv6 各一条，priority 5209、fwmark 0x100cf、lookup 65743）指向空表。未能从配置及归档日志确认其归属，因此没有将其当作 StarVPN 残留擅自删除；不影响本次 EasyProxy 验收。未重启整机。

### PC2 与 Cloudflare One Client WARP 共存修复结果

以下配置覆盖前文“PC2 的所有 DNS 查询均被 NAS Fake-IP 接管”的初始部署状态。

#### 已证原因

1. One Client 既有模式为本地代理（127.0.0.1:40000），使用 MASQUE/QUIC。改用 NAS 默认网关后，外层隧道 UDP 流量也进入 EasyProxy TUN。PC2 日志持续握手失败，NAS 日志确认捕获对应 162.159.197.2 UDP 流量。给正在使用的入口临时增加经原路由器的直连路由后，WARP 恢复 Connected。
2. DNS 查询仍被 NAS Fake-IP 接管，WARP 自身的 connectivity.cloudflareclient.com 被解析为 198.18.0.210。WARP SOCKS 按域名请求失败，但使用公共 DoH 获取的真实地址进行同目标对照请求成功，返回 warp=plus。让 PC2 获得真实 DNS 答案后，按域名请求恢复。

#### 最终最小配置

NetworkManager `pc2-lan` 新增并持久化：

```text
162.159.197.0/24 via 192.168.15.1 metric 50
1.1.1.1/32 via 192.168.15.1 metric 50
```

默认路由保持 `via 192.168.15.201`。只有 WARP MASQUE 入口及 PC2 所用 DNS 服务器直连原路由器，其余普通流量继续默认经 NAS EasyProxy。PC2 DNS 现在返回真实地址，不再依赖 NAS Fake-IP 映射；既有全局 DNS 配置与 no-aaaa 选项保留。

MASQUE 网段经当日联网核实，来源：
https://developers.cloudflare.com/cloudflare-one/team-and-resources/devices/cloudflare-one-client/deployment/firewall/

仅使用当前协议的已公布网段，没有将整个 Cloudflare 地址空间直连。未改变 WARP 模式、注册、组织策略或 Access 鉴权；未接受新的 CLI 条款；未改 NAS 源码或重启 NAS 容器。

#### 验证与证据

在重新应用 NetworkManager 配置、重启 warp-svc 后验收：

- 2026-11-11 13:49:38 UTC：服务日志报告 Connected / NetworkHealthy。
- 普通默认路径：Cloudflare trace 为 warp=off、loc=US，完整 1,048,576 字节下载 HTTP 200。
- WARP SOCKS5 hostname 路径：trace 为 warp=plus、loc=US，完整 1,048,576 字节下载 HTTP 200。
- 四个局域网 SSH 端口可达；Docker 与 Tailscale 服务运行正常。
- 修复过程的一次 WARP 下载在重启前 90 秒内未完成；重启后上述完整下载通过。不将单次成功表述为长期带宽保证。

PC2 修改前网络状态及最终验收：`/home/mjc/easyproxy-warp-repair-20261111`。
本地最终验收：`linshi/easyproxy-device-tiers/shared-dbip-7f6c585/warp-coexistence-acceptance.json`。
WARP 验收阶段未重启整机，未执行所有受 Access 保护应用的业务验收。Git 合并与推送按用户后续明确授权单独执行，不触发再次部署。

<a id="default-gateway-deployment-2026-11-12"></a>

## 默认网关规则分流部署回执（2026-11-12）

### 已上线

北京时间 2026-11-12 00:07:25，NAS 内 Debian VM `192.168.15.201` 已切换到本次默认 TUN 分流修复版。
00:15 最终核查为 running、RestartCount=0、TUN enabled/applied/ready，manifest healthy。
本轮没有部署云端、修改客户端路由或改变客户端显式参数解析。

- 容器：`easy-proxy-gateway`
- 容器 ID：`b78ca561bdf5530d35da5126a5af725464bba2e994c8e747189baa2de08d0bba`
- 镜像：`easyproxy/nas:default-routing-20261112-e93a90a78ee0`
- NAS image ID：`sha256:2a6985251b96d8de28ba794aa4d47ed8be3b6a4af805568bc59a038a3b9c6940`
- 二进制 SHA256：`e93a90a78ee0984dc0709dccebb3da96914244aaa6cbe60fe95d243cd5d50466`
- Compose：`/opt/easyproxy-gateway/compose/docker-compose.default-routing-20261112.json`
- Compose project：`easyproxy-gateway-default-routing-20261112`
- 源码基线：`bb4c3e21211b6c6d763519e560a2d135022bb112`，加本轮尚未提交的默认网关修复。

新镜像基于切换前准确运行镜像，只增加替换 `easy-proxy` 的一层；原 RootFS 基础层和镜像 Config 一致。
容器内二进制哈希与本地 Linux amd64 构建一致，配置文件哈希和持久挂载保持不变。
运行配置保留 `routing.enabled=false`、`local_server.enabled=false`、内置规则开启、GeoIP 开启、
默认选节点层级 US、网关最终兜底 PROXY、无可用代理时 DIRECT。没有悄悄把未知目标全部改成直连。

### 真实验收

PC2 `192.168.15.104` 的 `ip route get 223.5.5.5` 确认下一跳为 `.201`。以下普通请求均未使用显式代理：

| 验证 | 结果 |
| --- | --- |
| `https://www.baidu.com/` | TLS 校验开启，HTTP 200，约 1.28 秒；网关日志明确为 direct 到 `103.235.46.96:443` |
| `https://dns.alidns.com/dns-query`，固定真实 IP `223.5.5.5` 并提交有效 DNS query | TLS 校验开启，HTTP 200，约 0.127 秒；网关日志明确为 direct 到 `223.5.5.5:443`，覆盖非内置国内域名的 GeoIP 判断 |
| `https://www.google.com/generate_204` | TLS 校验开启，HTTP 204，约 2.16 秒 |
| Cloudflare trace | HTTP 200，`loc=US`、`warp=off`，说明默认国外流量仍走美国出口 |

使用现有凭据另行验证显式 HTTP CONNECT 和 SOCKS5，两者 TLS 请求均返回 204；匿名 CONNECT 返回 407。
这证明现有显式入口和认证保持工作，不等同于穷举所有客户端参数组合；参数选择的兼容性另由上一轮相关回归覆盖。

最终节点快照为 157 个节点、42 个 available。此为有时效的快照，不代表所有第三方节点可用。
新容器日志观察窗口未发现 panic/fatal。TUN API 的流量计数字段仍为 0，本次直连证据来自实际请求和 outbound/direct 日志，
没有把未接入的计数字段当作验收依据。IPv6 接管仍关闭，未新增 IPv6 或长期稳定性承诺。

一次内联 SSH curl 因引号传递导致通配符展开，产生无效 hostname 请求；已准确停止该测试进程，
改用 argv 固定的 Python 脚本完成上述验收。没有改动生产配置来掩盖测试错误。
第一次 DoH 请求未提交 DNS query，HTTP 400 仅证明 TLS 可达；最终有效 query 的 HTTP 200 单独验证。

### 备份与回退材料

- 停止态备份：`/opt/easyproxy-gateway/backups/default-routing-20261112/stopped-state.tar.gz`
- 备份 SHA256：`fbe10a6121f8f3b6ce41dec0b42b7ea41b106fa3b5951876384e958a30b78c1b`
- 220 个归档普通文件已逐项与停止态原文件校验，SQLite `PRAGMA integrity_check` 为 `ok`。
- 旧容器：`easy-proxy-gateway-rollback-default-routing-20261112`，已停止并保留。
- 旧镜像：`easyproxy/nas:shared-dbip-7f6c585-manifest-dns1`，未删除。
- 旧 Compose：`/opt/easyproxy-gateway/compose/docker-compose.shared-dbip-manifest-dns1.json`，未覆盖。

本次没有删除容器、镜像、历史备份或业务数据，没有提交或推送 Git。
回退时先核对现场身份和数据变化，再停止新容器并保留它，恢复旧容器名称和运行；不能同时运行两个网关。
本轮未触发回退，因此不宣称实际演练过回退。

本地脚本与脱敏回执：`C:/Users/Public/nas_home/AI/GameEditor/linshi/easyproxy-default-routing-20261111/`。
主要回执为 `preflight.json`、`stage.json`、`cutover-result.json`、`final-status.safe.json` 和 `explicit-probes.safe.json`。
配置与带秘密的旧容器 inspect 只保存在 NAS 的权限受限备份目录，不复制进产品仓库。
