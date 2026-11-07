# EasyProxy 运行核查与刷新修复（2026-11-06）

## 本轮状态

### 1.0.17 的准确结果与 1.0.18 候选

1.0.17 源码 `7b36a847bc5d527a34e79f498feb783026594a09` 已推送，Validate run
`42378438041` 两项通过；Linux SBA 全套 1920 passed / 51 skipped。单次恢复 task
`dc-47580752d6844c73a8c3da3b6ea47569` / run `42379246256` 的认证业务回执仍为
`EASYPROXY_SBA_PROMOTION_UNKNOWN`，不是成功。Worker/Pages 现为 1.0.17，active/stable
仍为 1.0.13；本次 writer operation 为 `32ca11b37c6c4e1283c0d5ee8205d0bf`。

本次已持久保存有限审计摘要：47 个稳定 URI、compatibility probe 通过、无格式错误且
审计容器清理已确认，因此不能再将此次失败归因为数量门禁。晋级阶段已登记，准确
`releases/<operation>/` 只有 51395 字节的 `subs/clash.yaml`，没有其余七个 release 对象；
旧 stable 七份产物、manifest SHA/ETag、D1 44/1/0/0 和 migration 1/2 仍未变。
11878 字节 age 备份完整回读摘要为
`b5d5d6614e63fe8c020b8ccacd436d7d53fe50653c937b00e5cc08d497620e76`。

本地只读 control GET 现场实测一次 HTTP 500；代码确认 PROMOTION 会丢弃确切
`CoordinationError`，该类错误可在首个不可变副本后的 ownership GET 发生。但原日志
没有保留底层分类，**不能将这次历史失败唯一归因于 R2 500**。1.0.18 候选仅对 control
GET 的 500/502/503/504 增加一次 0.25 秒退避重读，权限/确定性/无法分类错误不重读；
PUT/CAS/阶段写入与 SDK 单发送限制不变。PROMOTION 仅透传准确类型与完整固定参数的
四项协调错误码，不公开异常正文。该部分 Windows/Linux 相关测试各 205 passed。

用户另行批准准确本次父回执的 1.0.18 单次恢复。新入口额外绑定首个 release 副本、
七份阶段正文与 ETag、五份候选、完整旧备份、last-known-good 清单、D1 全表逐行摘要，
公开/provider 1.0.17、active/stable 1.0.13。任何漂移停止；create-only 消费证据后原 ETag
单次 CAS 移交非空 update writer，不清锁、不重放旧晋级、不删除任何原对象。新 update
仍先持久新加密备份，再发布、重新生成/审计、晋级、同步及公开验收，最后才 commit。
这里记录批准与候选设计，不是 1.0.18 已上线声明。

NAS 已实际更新到准确 1.0.17 完整镜像，停止态备份和 SQLite integrity_check 通过，
HTTP CONNECT 与 SOCKS5/TLS 均 204；启动收敛后 TUN IPv4 ready、manifest healthy、无
fallback，RestartCount=0、无 OOM/panic/fatal。云端稳定产物尚旧，不能据此称全部完成。

SpringBok PR #144 已合并为 `aa73dcbb6cfee2fb08d5afdc1362121640b294a4`，七项 main CI
通过，控制面已发布并独立核对 Worker bytes。修复只禁止点击过期预览确认，不延长签名、
不修改权限或恢复边界；Linux executor pin 仍为 `de26192f0be5c1f4df23e82e3ac3104ab59bcf30`。

用户于 2026-11-07 明确恢复本任务，并批准仅退役旧 `easyproxy-prod` 登记、保留历史任务
与回执，不触碰 main、NAS 数据或备份。旧 refresh writer 已按独立证据恢复；随后普通
1.0.14 update 产生新的 MIGRATION UNKNOWN；获批的 1.0.15 恢复已单次执行，解决备份
漏读并完成持久加密备份及 Worker/Pages 发布，但在晋级前返回新的 PROMOTION UNKNOWN。
**active/stable 仍是 1.0.13，Worker/Pages 是 1.0.15，新 writer 保留；旧 prod 登记已退役**。
上述为 1.0.15 停点。最新 1.0.16 执行与 1.0.17 受限修复见下一节；不能将部分更新称为整套上线成功。
不得把本文的修复候选当作已上线，也不得据历史部署成功卡片重新创建一套资源。

## 1.0.16 实际执行与 1.0.17 发布基线修复（2026-11-07）

1.0.16 源码 `6b35a53ecd4f085af2526118cbd84a8e644675c3` 的 Validate 两项通过，
Linux SBA 全套 1886 passed / 51 skipped；隔离真实生成 262 raw / 27 sources，再由新
完整 NAS 镜像实际审计得到 29 个稳定 URI，满足原 43 个节点的 40% 门禁，不沿用旧健康结论。

首次 UI 确认在两分钟签名有效期后点击，未登记/执行。重新只读预检、即时预览确认后，
任务 `dc-a140d70d79d1403d96ace01d07c4a677` / run `42376441530` 实际执行，认证结果为
`EASYPROXY_SBA_EDGE_UNKNOWN`，不能因 executor success 称业务成功。

已精确复现责任代码：`publish_ech()` 普通 update 分支以 active 1.0.13 检查线上 settings，
而恢复前已核验线上部分发布为 1.0.15；同份生产 settings 对 1.0.13 返回 OWNERSHIP，
对 1.0.15 通过。Pages 同样存在 active/provider 基线混用，必须同时修复，不能只过第一处。
此次 writer operation `f14880e4dd234124adf038b20c4b3264` 仅有 GitHub environment 阶段，
未 claim ECH、Pages、candidate、audit 或 promotion，releases 为空；新 11875 字节 age
备份完整 GET 与摘要通过，44 条订阅 / 1 条配置的原逻辑摘要未变。active/stable 仍为 1.0.13，
Worker/Pages 仍为 1.0.15。原 UNKNOWN、加密备份、旧阶段全部保留，不重放该任务。

用户已批准 1.0.17 单次受限恢复：固定父任务/请求/结果、原 writer/control SHA/ETag、
备份、仅 GitHub 阶段、无 releases、D1 全部原数据、stable 七产物及 provider settings/Pages
canonical 1.0.15；漂移零写入。create-only 移交证据后单次 CAS 非空 writer。返回的新内部
`PublicationBaseline` 绑定 operation 与 permit 摘要，只允许准确 1.0.17 update，不能从外部
请求传任意版本。Worker 和 Pages 在覆盖前各完整校验 1.0.15 所有权及绑定，普通 update
仍核 active；不改 active 来绕过校验。新 update 重新加密备份、发布、审计、晋级、同步，
全部通过才 commit。当前段落记录实现/授权，最终源码与云端结果另验。

NAS 已单实例切换到准确 1.0.16 镜像，完整停止态私密备份及 SQLite integrity_check 通过；
HTTP CONNECT、SOCKS5 的 TLS 实测均 204，TUN ready、manifest healthy、无 panic/fatal，
RestartCount=0。旧容器保留为停止态回滚；此处不宣称云端已更新，也不代表最终 1.0.17 已部署。
最终仅保留一套活动服务与当前产物；清理 NAS/云端历史运行产物仍需准确清单及单独删除批准。

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

2026-11-07 已经正常 owner UI 执行旧登记退役。平台 PR #139/#140/#141 经精确 head 审阅、
七项 PR/main CI 和控制面发布后，修复仓库大小写 scope 与历史连接 revision 导致的入口
拒绝。退役计划逐项确认旧 D1（UUID `523a9578-9880-4e9a-8554-10051d3abb6f`）、R2、
Worker、Pages、GitHub environment 全不存在，刷新 registry 只引用 main；单次确认后
页面显示“旧服务登记已退役”，活动列表仅一套 EasyProxy。保留历史与 claims，不发资源删除。

## 1.0.14 普通升级与 1.0.15 限定恢复

已提交推送 `bd79f3330e846673b5c77e1fe582060895bb90cf`，完整 Validate run `42359569341`
两项 job 均通过。正常 UI 单次 update 任务 `dc-441fc65dc20e4b63b58ba65920159a7f` / run
`42360503031` 虽 executor success，认证业务回执为 `EASYPROXY_SBA_MIGRATION_UNKNOWN`，
因此不能称升级成功。新 R2 writer 为 `daba9f8f380441cbb5ae64f8bbe6b792`。

只读核对：原 operation 完整阶段列表为空，准确 `misub.age` key HEAD 404；D1 migration
编号仍为 1/2，公开 Worker/Pages 和 active 都是 1.0.13，stable 字节/ETag 与原记录一致。
未持久备份意味着当前代码正常路径尚不能执行 migrate；不把“阶段为空”单独当无写入证明。

确定问题：CLI 直接 D1 逻辑备份只取 `subscriptions/profiles.id='main'`。线上分别是 44/1
条逐项行，没有 main 行；API debug export 和 WebDAV export 也不是 CLI 所需格式。
已用同一生产 D1 的 SELECT-only 对照复现旧 reader 0/0；候选读取全部 collection 行，
完整取得 44/1，逻辑备份校验通过，未调用生产 backup/migrate、未写数据或上传备份。

用户已另行明确批准仅本次 UNKNOWN 的恢复更新。1.0.15 候选在原 operation 固定键保存
不可变消费证据后单次条件移交 writer，绑定完整父 context、配置、原 control SHA/ETag、
无备份/无阶段、旧公开版本和稳定七产物及 D1 schema/history/44/1/0/0。正常新 update
仍先 age 加密全库备份、R2 留存并完整读回，再迁移、发布和提交。未知结果不得重放。
本段记录候选与批准，不等于已执行移交或新版本上线。

候选验证：Linux 相关 13 个测试文件共 464 passed，包括新恢复入口、真实 update 编排、
备份迁移及旧修复请求。首次扩大验证有 3 项旧索引断言失败，原因是新 manifest 入口插在
原入口之前；已将新入口追加至末尾，保留原 10 项顺序，未放宽旧断言。容器测试最初因
只读根目录及 Windows bind-mount 的临时文件语义在启动阶段失败；改用容器内 tmpfs 后
全部执行通过。Go CLI 全模块 84 passed（11 packages），同范围 `go vet ./...` 通过。
生产只读恢复预检已通过，尚未写入移交证据或新 writer。

## 1.0.15 单次执行后的准确停点（2026-11-07）

源码 `87f60967723e1fb11188ca375057dfaac52e93ab` 已提交推送并完成 Validate run
`42364492504` 两项 job。SpringBok PR #142 将显式 repair 上限从 10 窄调为 11，
保留末项完整验证与第十二项拒绝；精确 PR/main 七项 CI、控制面及固定 Linux executor
发布均完成。源码发布为 `de26192f0be5c1f4df23e82e3ac3104ab59bcf30`，纯证据文档
收尾 PR #143 的 main 为 `af1c5e14964e20c670d8ddaf5536f578228e95c5`，七项 CI 通过，
未因此重复发布控制面或移动 pin。

再次只读预检后，经正常 owner UI 准确计划单次提交 task
`dc-d17eef8a70434871883f85d92219af62` / run `42365774495`。认证 artifact
`13570719703` 的业务结果为 `EASYPROXY_SBA_PROMOTION_UNKNOWN`；executor success
不等于应用成功。新 operation 为 `276a780cf1d649bf9bb5a7e6a0d4d983`，旧固定 marker
已消费，原 UNKNOWN 与历史保留，不可重放该恢复或旧 update。

11:04Z 的只读核查：

- control SHA-256 `c040256342e7ac384eef4f779abc81f0217ec4719493143ee40e510e63d95a25`，
  ETag `"bc38d2d127f38e019792a45124caad42"`；active 1.0.13，writer 为本次 update/1.0.15。
- 私有 `_sba/backups/276a780cf1d649bf9bb5a7e6a0d4d983/misub.age` 为 11873 字节，
  age 加密头、完整 GET 大小及 SHA-256 均校验：
  `9a5efb5a25e3ab6afa59ee44bb62e18d02673d2f0127053ac6a3188a3e237163`。
- D1 migration 仍为 1/2，subscriptions/profiles 为 44/1。迁移后重新执行固定 SELECT
  的真实 reader，逻辑数据 SHA-256 仍为
  `02232d0f21f5f9796bb4054e405049f8406b7ea153dd8b2c800900c6f6b98468`，与迁移前相同。
- 持久阶段仅 candidate、audit、GitHub environment、ECH 和 Pages publication；没有
  aggregator-promotion 或 MiSub sync，准确 releases 前缀完整列表为空。
- stable SHA/ETag 仍为本文件前述旧值，七项 stable/immutable/public 字节全部一致。
  Worker 与 Pages 的公开版本均为 1.0.15 / `87f6096...`，因此必须报告部分更新状态。
- 候选五项 raw 产物格式通过，114 个 raw proxy、24 个来源；raw 数量不是审计有效数量。
  65 秒运行日志中 Edge 23 个事件（8 ok、12 responseStreamDisconnected、3 canceled），
  Pages 1 个 ok，均无 Worker exception。断流/取消不能直接等同 Worker 崩溃。

现有 PROMOTION 顶层错误会丢弃内部有限拒绝原因，hosted 日志也没有保存原私有审计结果。
虽然可以确认尚未到持久晋级写入，但不能唯一断言是无有效节点、40% 节点跌幅保护、
审计身份/兼容探测或其他前置校验；不能通过猜测、降低门禁或使用旧审计结果继续。
后续应先补可验证的有限诊断/审计证据，再为本次新父回执设计独立受限恢复并取得新授权。
当前不清锁、不删除控制键、不回滚数据、不重发任何部署、晋级或刷新。

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

10:10Z 新的有界探测中，SOCKS5 用户认证加 TLS 访问 Google 仍返回 204；HTTP CONNECT
客户端报 `ConnectionError`，尚需在云更新后检查有限错误分类，不能称两种协议均通过。
TUN IPv4 ready、IPv6 未就绪；manifest healthy、无 fallback，订阅源为 44、connector
源为 1、实例为 6。容器仍未重启、无 OOM，最近 15 分钟日志未发现 panic/fatal。

后续已定位上述 HTTP 探测遗漏：`lan.proxy_url` 不内嵌凭据，用户名/密码独立保存；
调查脚本未携带认证。改为正确编码并发送既有认证后，requests 与手工 CONNECT/TLS
均返回 204，无需改生产网关或凭据。11:07Z 最终只读复查，HTTP CONNECT 与 SOCKS5/TLS
均 204，TLS 验证开启；原容器 running、RestartCount=0、无 OOM，TUN IPv4 ready、
IPv6 未就绪，manifest healthy、无 fallback、44 个订阅源及 1 个 connector 源/6 个实例。
最近 15 分钟 1717 行日志无 panic/fatal；未重启 NAS 网关或其他消费者。

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

## 2026-11-07 继续修复：1.0.16 候选（尚未上线）

最新用户要求继续全部代码修复，并在最终源码冻结后统一更新云端、active、聚合订阅及
NAS；最终只保留最新版运行副本。清理安排在验收后，目录外删除仍需准确清单及明确同意。

11:29Z 只读重核仍是准确 1.0.15 writer/ETag、active 1.0.13、稳定 manifest 未变，
无晋级 marker/release，旧加密备份完整。新受限入口的全部 GET/SELECT 前置实测通过，
没有消费入口、写云数据或重放旧任务。

11:31–11:35Z 隔离候选消费审计：114 raw、11 个真实 direct-probe 可用 URI、兼容 lease
通过、URI 格式无异常，低于历史 43 节点对应的 18 个门槛。11:37–11:43Z 对准确旧 immutable
订阅的同环境审计：181 raw、36 个真实可用 URI、兼容 lease 通过。两次使用现有 hotfix
运行时，只是诊断，不冒充新版本镜像或原 hosted 审计；原 private audit 已缺失，不能唯一
倒推旧 run 的具体拒绝分支。此次重现足以证明不应降低数量门禁。

源码发现隔离生成将 storage 改为每次空目录，未带入已提交稳定版的 6 条采集来源缓存。
候选修复在应用持有的只读 R2 通道中完整验证旧稳定版七份产物、manifest/source identity
与控制状态不变，将来源缓存送入隔离 local storage。上游须重新请求/验证来源，之后
仍由准确候选镜像审计，不沿用旧健康结论，不向上游发送云写凭据。

同时补固定 PROMOTION 错误 allowlist、不可公开的 `_sba` 审计摘要；不保存 URI/URL/响应/
任意异常字段。周期刷新仅在明确数量门禁拒绝、成功审计已确认清理且原 Permit 未尝试
晋级时，保失败证据后 CAS 终止自己的 writer；晋级/同步未知继续保留 writer。

本次 `continue-update-before-promotion` 只绑定准确 parent task/run/request/result、原
control 字节/ETag、五份阶段正文、完整加密备份、无晋级及旧 stable、D1 原业务计数、公开
部分版本 1.0.15。create-only 移交证据后单次 CAS 到新 update writer，新升级仍须先持久
备份再迁移；不初始化 D1、不删控制键、不改原 UNKNOWN、不覆盖旧阶段。

当前 manifest 只声明这一项待用 repair；十一个已消费/过期声明不再暴露到 UI，原声明
放入测试夹具以继续验证历史安全边界，不为了第十二个历史入口再扩 SpringBok 上限。
实现、测试、真实重新生成、CI 与最终部署证据将分开记录；此节不宣称生产修复已完成。

12:15–12:18Z，完整 1.0.16 NAS 候选镜像对带缓存的真实生成结果进行隔离消费审计，
114 raw 仅得到 9 个稳定 URI，兼容 lease 通过但不满足数量门禁。独立只读源码快照重跑
生成成功，两个结果的 114 个节点配置集合相同；来源缓存本身不足以恢复节点数量。
故补用批准上游已有的 `remains`：只从同次完整校验的 stable 读取 raw Clash，作为本轮
local storage 的候选输入，并不传递旧 effective/审计结果；重新聚合和实际检测之后才
可发布。没有上一稳定输入的初次部署仍使用 `--overwrite`。40% 门禁保持不变。

前一提交 `a40588f5db56709d655c97f63f395071104a132d` 的 GitHub Validate run
`42371020397` 两 job 均通过。本地 Linux 整套为 1879 passed、51 skipped、1 failed；
唯一失败是遗漏的一项历史 manifest 测试仍读取当前声明，已改读冻结历史 fixture 并保留
原历史 hash 校验，定向 28 项通过。后续修改需重新验证，以上不代表最终源码已验收。
