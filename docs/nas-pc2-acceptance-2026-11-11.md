# NAS 新版与 PC2 默认接入验收（2026-11-11）

## 部署边界

- NAS 宿主机：192.168.15.200。
- EasyProxy 实例：NAS 内 Debian 虚拟机 192.168.15.201，目录 `/opt/easyproxy-gateway`。
- Linux PC2：192.168.15.104，用户 mjc。只消费 NAS 服务，没有安装另一套 EasyProxy 本体。
- Windows 台式机 192.168.15.20 与笔记本 192.168.15.136 保持既有接入。

## 本次修复与版本

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

## PC2 持久配置

- NetworkManager 连接 `pc2-lan` 的 IPv4 默认网关为 `192.168.15.201`。
- NAS TUN 当前只启用 IPv4，PC2 设置 `ipv6.never-default=yes`；保留 IPv6 地址与局域网功能，不声明已提供公网 IPv6 代理。
- DNS 使用 `1.1.1.1` 作为查询目标，流量经默认网关被 NAS TUN DNS 接管。它不是绕过 NAS 的直连 DNS。
- NAS 的 `.201:53` 没有独立 DNS socket，不能将该地址直接当作本部署模式的 DNS 服务器。
- `/etc/NetworkManager/conf.d/90-easyproxy-nas-dns.conf` 固定全局 DNS，避免 StarVPN 覆盖连接级 DNS；保留原有 `no-aaaa` resolver 选项。显式 AAAA UDP/TCP 查询仍另外验证成功。
- GNOME 系统代理为 `none`，无需应用配置 HTTP/SOCKS 或设置代理环境变量。
- FlClash UI/Core 已停止，`flclash-helper.service` 已禁用。其文件与原始配置未删除，原配置 `autoLaunch=false`。
- StarVPN、Tailscale 及其他应用未停用。

## 实际验收

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

## 保留的恢复资料

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

## PC2 StarVPN 卸载结果

同日用户另行批准卸载 StarVPN。通过 apt purge 仅移除 `starvpn 6.1.0`，未执行依赖自动清理。已核实程序目录 `/usr/local/lib/stars`、命令软链接、`stars-service.service`、StarVPN 虚拟网卡及其地址路由均消失，无相关运行进程。Tailscale、Docker 和 NAS EasyProxy 未停用，FlClash 恢复资料未删除。

卸载前备份与日志位于 PC2 `/home/mjc/starvpn-removal-20261111`，目录权限 0700；包含程序与配置归档、网络基线、卸载日志及检查结果。备份可能含凭据，不应公开或提交仓库。

卸载后重复验证：DNS A、AAAA UDP/TCP、默认 HTTPS 204、完整 1 MiB 下载以及四个局域网 SSH 端口均通过；IPv4 默认网关仍为 `192.168.15.201`。本地记录为 `post-starvpn-removal-acceptance.json`。

两条既有规则（IPv4/IPv6 各一条，priority 5209、fwmark 0x100cf、lookup 65743）指向空表。未能从配置及归档日志确认其归属，因此没有将其当作 StarVPN 残留擅自删除；不影响本次 EasyProxy 验收。未重启整机。

## PC2 与 Cloudflare One Client WARP 共存修复结果

以下配置覆盖前文“PC2 的所有 DNS 查询均被 NAS Fake-IP 接管”的初始部署状态。

### 已证原因

1. One Client 既有模式为本地代理（127.0.0.1:40000），使用 MASQUE/QUIC。改用 NAS 默认网关后，外层隧道 UDP 流量也进入 EasyProxy TUN。PC2 日志持续握手失败，NAS 日志确认捕获对应 162.159.197.2 UDP 流量。给正在使用的入口临时增加经原路由器的直连路由后，WARP 恢复 Connected。
2. DNS 查询仍被 NAS Fake-IP 接管，WARP 自身的 connectivity.cloudflareclient.com 被解析为 198.18.0.210。WARP SOCKS 按域名请求失败，但使用公共 DoH 获取的真实地址进行同目标对照请求成功，返回 warp=plus。让 PC2 获得真实 DNS 答案后，按域名请求恢复。

### 最终最小配置

NetworkManager `pc2-lan` 新增并持久化：

```text
162.159.197.0/24 via 192.168.15.1 metric 50
1.1.1.1/32 via 192.168.15.1 metric 50
```

默认路由保持 `via 192.168.15.201`。只有 WARP MASQUE 入口及 PC2 所用 DNS 服务器直连原路由器，其余普通流量继续默认经 NAS EasyProxy。PC2 DNS 现在返回真实地址，不再依赖 NAS Fake-IP 映射；既有全局 DNS 配置与 no-aaaa 选项保留。

MASQUE 网段经当日联网核实，来源：
https://developers.cloudflare.com/cloudflare-one/team-and-resources/devices/cloudflare-one-client/deployment/firewall/

仅使用当前协议的已公布网段，没有将整个 Cloudflare 地址空间直连。未改变 WARP 模式、注册、组织策略或 Access 鉴权；未接受新的 CLI 条款；未改 NAS 源码或重启 NAS 容器。

### 验证与证据

在重新应用 NetworkManager 配置、重启 warp-svc 后验收：

- 2026-11-11 13:49:38 UTC：服务日志报告 Connected / NetworkHealthy。
- 普通默认路径：Cloudflare trace 为 warp=off、loc=US，完整 1,048,576 字节下载 HTTP 200。
- WARP SOCKS5 hostname 路径：trace 为 warp=plus、loc=US，完整 1,048,576 字节下载 HTTP 200。
- 四个局域网 SSH 端口可达；Docker 与 Tailscale 服务运行正常。
- 修复过程的一次 WARP 下载在重启前 90 秒内未完成；重启后上述完整下载通过。不将单次成功表述为长期带宽保证。

PC2 修改前网络状态及最终验收：`/home/mjc/easyproxy-warp-repair-20261111`。
本地最终验收：`linshi/easyproxy-device-tiers/shared-dbip-7f6c585/warp-coexistence-acceptance.json`。
WARP 验收阶段未重启整机，未执行所有受 Access 保护应用的业务验收。Git 合并与推送按用户后续明确授权单独执行，不触发再次部署。
