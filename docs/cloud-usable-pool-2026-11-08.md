# 云端可用池 1.0.20 上线与验收记录

日期：2026-11-08，北京时间。本文是有时间边界的上线回执，不替代后续健康状态。
模式与失败语义见 [云端可用池与本地 connector](cloud-usable-pool.md)。

## 最终部署身份

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

## 本次修复了什么

此前普通订阅只在 MiSub 独立登记，没有接入 agg 的生成与真实消费审计链路；NAS 的
Global 因而不会自动获得这些订阅的可用节点。本次将全部已启用的普通订阅接入每轮生成：

1. 从准确 MiSub 项目读取普通订阅登记，重新下载每个来源，并检查每源确实贡献候选节点。
2. 使用批准的 agg 转换器，与 agg 候选合并、去重，再经 EasyProxy 运行时实际审计。
3. Global 只发布审计通过的实际代理 URI；`subscriptions=[]`，不下发普通订阅原始 URL。
4. 剔除上一轮用户订阅候选摘要对应的旧节点，避免每日轮换或来源禁用后由 `remains` 缓存带回。
5. ZenProxy 与 ECH 仍发布 connector 参数，由 NAS 展开并与全部节点一起接受本地健康检查。

同时修正了批准 agg 的非标准本地文件读取协议适配，并用真实生成验证两条普通订阅均有贡献。
未把下载成功或格式转换成功当作节点可用，也未在失败时发布漏源的半批池。

## 云端验收

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

## NAS 验收与实际传输

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

### NAS 健康检查窗口调整

节点池扩大后，原 `subscription_refresh.health_check_timeout=1m0s` 不足以完成整轮
有界并发探测，曾报 `reload health check failed: context deadline exceeded` 并正确保留旧池。
通过正常设置接口仅将该窗口调为 **3m0s**，`min_available_nodes=1` 保持不变；其他配置
语义逐项对账未变。随后正常刷新成功，没有绕过最少可用节点门禁。该项是部署容量配置调整，
没有新增程序热补丁。

## 定期刷新边界

- 云端每六小时执行：UTC `25 */6 * * *`，北京时间 02:25、08:25、14:25、20:25。
  每轮重新读取普通订阅、下载并审计，使用当前 active 批准源码。
- NAS source-sync 每 **1 小时**更新可用池与 connector，并继续本地健康检查。
- 周期配置和对应代码路径已核对；本次正常更新与手动刷新已真实成功。
  **本回执未等待 1.0.20 的下一次自然定时任务，不能把它写成已执行成功。**

## 测试与发布证据

- 入池实现 `bcc3d5fcc1eeabe8479fc608e1ffb6642492c505` 的 Linux SBA：
  **1994 passed / 51 skipped**；真联网双来源生成得到 381 候选，每源均有贡献。
- NAS 完整镜像原生 TUN E2E：`ALL_TUN_E2E_PASS`。
- `1.0.20` 相比该实现仅修改 manifest 和声明测试；3 项定向测试通过，真实 SpringBok
  validator 返回 `SPRINGBOK_MANIFEST_PASS 1.0.20`。未重复声称整套 SBA 在 20 又运行了一次。
- `1.0.20` 的 GitHub Validate run `42409758894`：`Validate Repository` 与
  `Native TUN Dual-Stack E2E` 均 success。
- 20 的 NAS RootFS 和运行配置（除标签外）与通过原生验收的 19 一致，网关二进制 SHA 相同。
- Release tag 精确指向运行源码；本次收尾文档提交不会改变已部署程序身份，也不另建版本。

## 备份与经批准的清理

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

## 本地证据与接入入口

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
