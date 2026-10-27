# EasyProxy NAS 透明网关部署记录（2026-10-27）

> 这是 2026-10-27 的历史部署记录。2026-10-28 已替换为保数据的本地认证修复镜像；当前准确镜像、Compose、回验与回退边界见
> [上游认证失败修复与 NAS 回验](upstream-auth-repair-2026-10-28.md)。后续操作以新记录为准，不直接启动本页旧 Compose。

## 范围与现状

本轮按用户选择，恢复 NAS 内 Debian VM `192.168.15.201` 上的 IPv4 透明旁路由。
DSM 主机 `192.168.15.200` 仍是独立容器宿主机，没有在 DSM 安装主网关，也没有恢复旧辅助 relay。
云端继续使用已由 SpringBok 部署的 `easyproxy-main`；本地 VM 直接使用 Docker Compose，不经过 SpringBok。

新鲜盘点时 VM 没有容器，旧运行 config/data 已不存在；保留的 49 个旧部署文件和历史归档未覆盖、未删除。
本轮没有更改其他 NAS 服务、DSM 默认路由或 DNS，没有升级 Docker、重写全局 nftables 配置或重新部署云端。

## 准确版本与运行位置

- 源码：`24b26417b57bd3fc4ffd3f3fb0334c3155373460`，与云端应用 1.0.13 对应。
- 完整镜像包含 `easy-proxy`、`ech-workers`、`cfst`、`cloudflared`。
- image config digest：`sha256:4a14878ee5f35ef15035ddfbca3be5eb7a4dbcc3865994b540e9611adbe8b76d`。
- VM 使用的 OCI manifest digest：`sha256:0beb76ec7dcc9c76206ee8581e13b0cd3b10e6b49af8f620f2f461b0d7cd9d78`。
- 容器：`easy-proxy-gateway`。
- 新 Compose：`/opt/easyproxy-gateway/compose/docker-compose.20261027.yaml`。
- 生效配置：`/opt/easyproxy-gateway/config/config.yaml`。
- 持久数据：`/opt/easyproxy-gateway/data`，其中包含 SQLite 与 `data/tun-cache.db`。

两个 digest 不是两个版本：导入后已核对 OCI manifest 引用的 config、全部 RootFS 层，以及四个二进制 SHA-256。
新 Compose 直接固定 manifest digest，设置 `pull_policy: never`，不读取旧 `.env`，避免拉取同名浮动镜像或误启动旧部署。

配置目录整体挂载到 `/var/lib/easyproxy/config`，而不是单独 bind `config.yaml`；这样管理 API 可以使用原子 rename 保存配置。
配置与数据都是持久 bind mount，不是前一轮 Windows 验收容器的 tmpfs。

## 入口与凭据

| 用途 | 地址 |
| --- | --- |
| 管理 WebUI/API | `http://192.168.15.201:29888/` |
| HTTP / HTTPS CONNECT / SOCKS5 mixed 入口 | `192.168.15.201:22323` |
| 透明网关 IPv4 下一跳 | `192.168.15.201` |
| 云端 manifest | `https://easyproxy.yamiyu.com/api/manifest/aggregator-global` |

显式代理用户名为 `easyproxy`。新管理密码和代理密码统一保存在：

```text
C:/Users/Public/nas_home/aikey/密钥/EasyProxy-当前接入.json
```

统一文件中 `lan.password` 为管理/代理密码，`lan.username` 为显式代理用户名。
云端 manifest token 见 `misub.manifest_token`；全部 SpringBok 业务秘密见 `springbok.business_secrets`。
管理 API 使用 `Authorization: Bearer <lan.password>`，当前为 password-only 模式，不要求管理用户名。
manifest token 沿用当前部署值，不复制到 Git、Compose 环境变量、命令参数或本说明。
管理和代理入口仅绑定 VM 的 IPv4 LAN 地址；专属 nft 规则限制来源为 `192.168.15.0/24`。
匿名管理请求返回 401；匿名 HTTP 代理返回 407；SOCKS5 不接受无认证协商。
直接走旁路由的客户端不需要代理账号；显式使用 22323 的程序需要上述凭据。

验收时配置文件权限为 0600，配置目录权限为 0700。后续通过 WebUI 保存配置时也应保留目录访问限制。

## 网关与失败策略

- 最终运行 `mode: pool`、`listener.protocol: mixed`，没有对 LAN 暴露逐节点端口。
- `gateway.mode: tun`，接口 `easyproxy0`，stack `mixed`，MTU 1500。
- 入口 `ens3`，trusted CIDR `192.168.15.0/24`。
- IPv4 TCP/UDP、TCP/UDP DNS hijack、Fake-IP 开启。
- IPv4 policy table 100；EasyProxy egress mark 绕过捕获。
- 无可用代理时使用 `DIRECT`；干净停服会移除 EasyProxy 自己的 TUN 捕获表、策略规则、路由和接口。
- host 专属 `/etc/nftables.d/easyproxy-forwarding.nft` 提供 LAN 入口限制及 IPv4 fail-open NAT。
- host 专属 `/etc/sysctl.d/99-easyproxy-gateway.conf` 持久化 IPv4 转发与 redirect/rp_filter 设置。
- 保留所有共享 nft 表及原 `/etc/nftables.conf`；沿用现有 enabled/active 的 nftables 服务。

**IPv6 本轮没有透明接管。** VM 自身 IPv6 直连 TLS 可用，但本次没有改变 IPv6 policy rules、客户端 IPv6 下一跳或 RA。

**普通 UDP 当前按 DIRECT 回退。** 验收快照没有可用的 Hysteria2/TUIC 原生 UDP 节点；公网 STUN 联通不等于已经证明 UDP 代理出口。
QUIC/HTTP3 没有单独做本轮真实业务验收，不能据 UDP flag 或 STUN 成功宣称已通过。

## 运维命令

使用明确的新 Compose 文件，不在旧 compose 目录直接执行无参数的 `docker compose up`：

```sh
docker compose --env-file /dev/null -p easyproxy-gateway-20261027 \
  -f /opt/easyproxy-gateway/compose/docker-compose.20261027.yaml \
  up -d --no-build --pull never

docker inspect easy-proxy-gateway
docker logs --timestamps --tail 200 easy-proxy-gateway
```

重启同一实例：

```sh
docker restart --time 30 easy-proxy-gateway
```

停止网关、回到 host 正常转发：

```sh
docker stop --time 30 easy-proxy-gateway
```

停止后应只读核对 `easyproxy0`、`inet easyproxy_gateway`、table 100 和 priority 100/110 的 EasyProxy 规则已移除。
不要清空共享 nft 表、全局路由、持久 data 或历史文件。重新启动可以使用 `docker start easy-proxy-gateway`。

容器使用 `unless-stopped`，Docker 日志轮转为 `json-file`、`max-size=20m`、`max-file=5`。
这不限制 SQLite、connector 自身文件或其他 NAS 服务的日志。

启动包含 manifest/connector 初始化。端口不是立刻可用；应以受认证的 `/api/gateway/status`、source-sync 状态和实际客户端业务为准，
不要因为短时间端口未监听就连续执行重复部署。

## 实际验收结果与边界

2026-10-27 已验证：

1. 网关先 disabled，真实同步 47 个节点、6 个 ECH connector，无 fallback 订阅。
2. 普通来源专用端口 HTTPS 200、ECH 来源专用端口 HTTPS 204，均校验证书和响应内容。
3. 带认证的 HTTP 200、HTTPS CONNECT 204、SOCKS5 TLS 204；最终重启后再次通过。
4. DSM 的主默认路由原本为 `.201`、metric 10，备用 `.1`、metric 100；没有更改。
5. DSM 无显式代理的 HTTP/HTTPS、外部 TCP/UDP 53 DNS 拦截、Fake-IP 到域名恢复均通过；重启后再次通过。
6. 重启前的 `198.18.0.2` 映射，在重启后不先重查 DNS 即完成 Google TLS 204，证明 Fake-IP 缓存恢复。
7. 公网 UDP STUN：首轮 Google、Cloudflare 都收到 Binding Response；重启后 Google 再次通过，Cloudflare 一次请求 timeout，未宣称所有 UDP 目标稳定。
8. 管理 API 实际保存配置、目录 bind 原子落盘、同一容器重启、配置与凭据/manifest readback 均通过。
9. WebUI HTTP 200、真实管理登录成功、匿名 API/代理拒绝；共享 nft 和 IPv6 policy rules 保持不变。

验收快照的 47 个节点中，8 个被标记为 effective available；这里的 47 是同步总数，不代表 47 个出口逐一验通。

启动期间曾有一次 manifest `TLS handshake timeout` 导致 bootstrap 退出，Docker 自动重启后恢复。
随后手动重启和本节业务复测通过；没有据此声称底层网络抖动已被彻底解决。

已验证容器重启和开机持久化配置，不曾重启整台 VM/DSM，因此完整 OS 重启周期仍未实测。

完整脱敏回执与加密原始证据位于：

```text
C:/Users/Public/nas_home/AI/GameEditor/linshi/easyproxy-nas-20261027
```

重点：`verify-09.safe.json`、`enable-15.safe.json`、`lan-14.safe.json`、`persist-restart-19.safe.json`、`final-21.safe.json`。
这些是本轮证据，不应拿旧部署文档或 API ready 单项代替本轮真实业务测试。
