# NAS 默认代理地区排除部署回执

## 部署范围与身份

- 用户明确授权部署到 NAS；实际目标为 Debian VM `192.168.15.201`，不是 DSM `.200`。
- 本地源码：`C:/Users/Public/Project/EasyProxy`，基线 `db09ee2e752cd566097b9eb91b370f4a71ea26e4` 加本轮未提交的地区排除修改。
- 新容器启动时间：`2026-11-12T18:07:37.90544146Z`（北京时间 2026-11-13 02:07:37）。
- 镜像：`easyproxy/nas:region-exclusions-20261113-95138dfaac18`。
- 镜像 ID：`sha256:78e220c66348cf3e75c9cdc9876f8b6d2b103749444cfbf2fee8c8f41ea6910c`。
- 实际运行 ELF SHA-256：`95138dfaac1805ef1d31d495044b778756f83ddfe6461d3e8c5e43c754cc6d54`。
- Compose：`/opt/easyproxy-gateway/compose/docker-compose.region-exclusions-20261113.json`。
- Compose project：`easyproxy-gateway-region-exclusions-20261113`。

使用 Linux/amd64、CGO_ENABLED=0 及 `with_utls with_quic with_grpc with_wireguard with_gvisor`
构建。镜像基于切换前的实际运行镜像，只增加替换程序的文件层，运行镜像配置与基础层一致。
未提交、未推送或发布云端套件。

## 数据与回退

- 配置 SHA-256 前后一致：`4db2c57e296dd8a6d8350c0fb57fe712eeb274d092b8e5e3c3480229c34e1938`。
- 配置和数据挂载保持不变。
- 停机备份：`/opt/easyproxy-gateway/backups/region-exclusions-20261113/stopped-state.tar.gz`。
- 备份 SHA-256：`6a4b1c19ea7af6ad4ee2d405135834d4e7764284fb6c98d45254f3d70e92a6f9`。
- 232 个备份文件逐一与停机原文件核验；SQLite `PRAGMA integrity_check` 为 `ok`。
- 回退容器：`easy-proxy-gateway-rollback-region-exclusions-20261113`，停止保留。
- 未删除旧容器、镜像、配置或备份；未演练回退。

## 实际验证

- `gateway.enabled/applied/tun_ready=true`，IPv4 TUN 保持原配置；RestartCount=0。
- manifest healthy；美国优先层级保留。
- NAS 认证 HTTP CONNECT + TLS：Google 与 Cloudflare 的 generate_204 均为 204，证书校验开启。
- 观察窗口内通用池选路记录：United States 221 条、Unknown 2 条；无 CN/HK/MO 已识别地区违规记录。
- 节点列表仍有 239 项，其中 11 项元数据为被排除地区，证明没有以删除节点实现规则。
- 同一 ELF 在 NAS 上启动仅绑定 loopback 的隔离实例，只装入两个真实香港节点：
  - 默认请求：502，符合没有合格自动候选的预期。
  - 显式 `cc=HK+nosplit`：Google HTTPS 204。
  - 显式 `pin-strict=<tag>+nosplit`：Google HTTPS 204。
  - 显式请求之后再发默认请求：仍为 502，没有粘性绑定泄漏。
- 隔离进程已结束，33323/33388 端口无监听。生产 routing/local_server 启停配置未改变；
  参数验证来自隔离的参数入口，不声称生产的 legacy 入口新增了参数解析能力。
- 上轮相关 4 个包共 255 项测试通过，本轮源码未变化，不重复执行。

PC2 只读检查发现到 `1.1.1.1` 的当前下一跳是 `192.168.15.1`，并非 NAS `.201`。
其 `curl --noproxy '*'` Google 请求为 204，但不能据此声称经过本次 NAS TUN。
未修改 PC2 或其他消费者配置，不声明所有设备已逐台验收。

地区规则依据节点 GeoIP 元数据，不是对最终出口的实测地理保证；Unknown 保持原行为。
既有无可用代理时的 DIRECT 策略未修改。

## 证据入口

本地：`C:/Users/Public/nas_home/AI/GameEditor/linshi/easyproxy-region-exclusions-20261113/`。
包含 `preflight.json`、`stage.json`、`cutover-result.json`、`verify.safe.json`、
`explicit-proof.safe.json`、`build-source.safe.json`、`source-changes.patch` 和 `changed-source.zip`。
远端 staging：`/home/mjc/easyproxy-region-exclusions-20261113`。
