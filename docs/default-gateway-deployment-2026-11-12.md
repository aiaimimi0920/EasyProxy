# 默认网关规则分流部署回执（2026-11-12）

## 已上线

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

## 真实验收

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

## 备份与回退材料

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
