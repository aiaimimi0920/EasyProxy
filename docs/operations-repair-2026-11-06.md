# EasyProxy 运行核查与刷新修复（2026-11-06）

## 本轮状态

用户于 2026-11-07 明确恢复本任务，并批准仅退役旧 `easyproxy-prod` 登记、保留历史任务
与回执，不触碰 main、NAS 数据或备份。已完成线上核查、本地修复候选和聚焦验证；旧失败
writer 已按独立证据恢复为空。**EasyProxy 1.0.14 尚未更新部署，旧登记尚未实际退役**。
不得把本文的修复候选当作已上线，也不得据历史部署成功卡片重新创建一套资源。

## 为什么显示两套云服务

SpringBok 显示两个不同实例，而不是同一实例的两次任务：

| 实例环境 | 历史应用版本 | 实例 ID | 当前核查 |
| --- | --- | --- | --- |
| `easyproxy-main` | 1.0.13 | `dc-92bbbbc744784ec6adf13a37ddd68f97` | 当前正式套件；NAS manifest 使用该套件的公开域名 |
| `easyproxy-prod` | 1.0.10 | `dc-f72a80867cd24c3abe191584a9cbbd64` | 历史登记；对应 Pages、Worker、R2 精确查询均返回不存在 |

同一账号当前 D1/R2/Pages/Worker 清单只发现 main 的 EasyProxy 资源，GitHub 也只有
`easyproxy-main` environment，刷新 registry 只包含 main。两套历史实例分别来自独立首次
部署，而不是一套套件本身必须复制运行。旧资源清理后，平台仍按历史部署索引投影服务卡片。

当前 SpringBok 的正式删除不支持同时含 Pages 和 GitHub Actions 的混合套件，因此删除
按钮禁用。不得仅放宽 Worker 删除门禁或直接删历史任务：前者可能遗留数据与后台刷新，
后者会破坏回执/归属证据。应仅退役准确旧实例登记并保留历史，不触碰 main、NAS 和备份。

## 云端刷新持续失败

GitHub Actions `SBA Aggregator Refresh` 在 2026-10-28 13:11:13Z 启动的 run
`40694138957` 于 13:16:55Z 失败。R2 control 最后写入于 13:13:25Z，仍持有该次刷新 writer：
`6ed589dfbf3d44eeb5208100721892f7`。其持久阶段只有 candidate、audit，没有 promotion 或
MiSub sync。核查窗口中含该次原始失败在内，连续 24 次 schedule failure；最新 run
`42177837323` 只输出 `EASYPROXY_SBA_SCHEDULE_FAILED_OR_UNKNOWN`。

`Coordinator.current_release()` 对非空 writer 明确拒绝 `BUSY`，因此后续任务无法选取源码，
既不会刷新候选，也不会重新审计。稳定产物 manifest 仍为 2026-10-28 05:59:28Z、run
`6b5065baa028449489f5c0670507f1a7`，43 个当时审计有效节点、6 个来源。此历史有效数量
不是当前网络可用承诺。原始审计私有日志未保存在 Actions 公共日志中，不能唯一倒推最初
审计中断原因；已确定的是其后持久 writer 对定时刷新造成持续阻断。

本地 1.0.14 修复候选：

- 对未尝试晋级的候选失败，以及审计失败且进程组已停止、准确审计容器已确认不存在的情况，
  保存 immutable 失败记录后按原 ETag 结束**当前进程**的刷新。active、stable、MiSub 和旧证据不变。
- 审计终止/清理未知、promotion/sync 已尝试、归属漂移或失败记录/CAS 未知依然停止并保留 writer。
- 调度入口、选源、运行环境、生成、审计和晋级传播固定枚举阶段码。只提取私有子进程日志中的
  完整有限阶段码，不公开异常正文、provider 响应、订阅、配置或秘密。
- 旧 writer 不由上述自动分支接管。恢复前必须另行绑定准确 control 字节/ETag、已结束的原
  GitHub run、完整阶段清单、未尝试晋级证据和稳定产物一致性，再保存恢复证据并执行一次条件写。

2026-11-07 独立恢复同时核对原 hosted run/jobs 已结束、没有在途刷新、完整阶段清单及稳定
manifest 七项产物与公开字节。恢复计划 SHA-256 为
`6f28800345ce84479fa5919e713e092c939f0c277dfba3d4f5d126db6eacff93`。
先 create-only 保存证据并回读，再对准确旧 ETag 单次 CAS；独立 GET 确认 writer 为 null、
active 仍为 1.0.13 / `24b26417b57bd3fc4ffd3f3fb0334c3155373460`。
未重放原任务、未修改 stable/MiSub、未删除控制键；该恢复不是后续失败的通用清锁许可。

## NAS 实况与外部出口边界

2026-11-07 08:51Z 复查：Debian VM 的唯一运行网关仍为此前已部署的
`easyproxy/local-hotfix:persisted-20261028-788fc1745afe`，容器 `RestartCount=0`，无 OOM。
实际二进制 SHA-256 与此前回执一致：
`00054479d8e34053e52181f435c452bd5abc6ab3453e07ea3e9e1a99804f834d`。
TUN enabled/applied/ready；manifest healthy、无 fallback，订阅同步没有 last_error。

49 个节点仅 1 个 effective available；34 个处于上游认证隔离，另有超时、拒绝连接和
502/503 等错误。不能通过清 blacklist 或把 LAN 密码用于第三方上游来“恢复”它们。
同一当前凭据经 NAS 显式代理访问 Google 返回 HTTPS 204、空正文，TLS 验证启用；
这只证明一次真实传输成功，不代表全部节点或所有目标长期健康。

Cloudflare 官方 TCP sockets 文档明确禁止连接 Cloudflare IP、localhost 和私网地址；
`proxy request failed, cannot connect to the specified address` 是该限制的准确错误。
ECH Worker 是 TCP/WebSocket 入口，不能用普通 HTTP fetch 冒充端到端 CONNECT 隧道，
也不能用不受控第三方代理绕过。应区分这类目标限制与 Worker 崩溃/鉴权失败。
参考：https://developers.cloudflare.com/workers/runtime-apis/tcp-sockets/

本轮原有未提交的 pool 状态恢复改动已完成三包 334 项 Go 回归及同范围 vet；它们此前已
部署在 NAS。没有将无关的本地分析目录、报告或工具产物加入产品修复范围。

## 验证及证据

- Windows：刷新、调度、协调器与失败终止的聚焦测试 81 passed、1 个 Linux timer 跳过。
- Linux：同一聚焦集加真实进程组 timeout/nonzero/signal/deadline、清理未知、镜像、部署与
  Python runtime 相邻回归，256 passed、无跳过。
- 独立一次性恢复脚本：8 passed；恢复前 dry-run 通过，恢复后的独立 GET 确认准确结果。
- Linux bootstrap 按项目原 lock 建立真实 CPython 3.12 venv 成功。
- 一次扩大到整个 `.sba/tests` 的尝试已停止；首个失败为 slim 测试镜像未安装 `git`，不是
  产品回归通过，亦未修改生产逻辑或测试断言来绕过它。

调查脚本、脱敏选定回执和 DPAPI 原始证据位于：
`C:/Users/Public/nas_home/AI/GameEditor/linshi/easyproxy-ops-20261106`。
原始响应、运行配置及完整日志不提交到产品仓库。
