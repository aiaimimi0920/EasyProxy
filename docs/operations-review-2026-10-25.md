# EasyProxy 部署与运行审查（2026-10-25）

## 范围与安全边界

本轮读取 NAS 网关的容器状态、管理 API、最近 24 小时日志，以及 GitHub Actions、
Cloudflare Pages/Worker 部署元数据和公开订阅产物。凭据仅从独立凭据库读取；
报告不包含密码、Token、订阅节点 URI 或完整原始日志。

本轮未重启、替换、删除生产容器，未写入 MiSub/R2，未触发 Cron、注册或付费
业务，未提交或推送 Git。生产验证仅包含只读接口及一次显式代理 HTTP 请求。

## 现场结果

2026-10-25 05:06–05:15 UTC 的快照：

- NAS VM `192.168.15.201` 的 `easy-proxy-gateway` 从 10 月 2 日起持续运行；
  `RestartCount=0`、`OOMKilled=false`，镜像为
  `easyproxy-local:upstream-20261002-2749bc9`，revision 为 `2749bc9`。
- 管理 API 返回 200，Local Server dispatcher ready，TUN applied/ready。
  节点接口报告 72 个节点、51 个可用节点；这是当时的接口计数，不代表所有节点
  都适用于所有业务目标。直接向 LAN 代理端口发送绝对 URI 请求得到 HTTP 204，
  耗时约 0.66 秒，没有借用开发机默认代理或直连绕过网关。
- 来源同步在 04:38 UTC 出现 `TLS handshake timeout`，当时使用 fallback。
  24 小时内刷新开始/完成各 24 次，其中 13 次来源未变化而跳过 reload；没有
  `reload failed` 或 `panic:`，统计到 1 条 TLS timeout。尚无足够证据支持
  放宽健康门禁、无限重试、删除来源或任意改变刷新周期。
- 单点资源快照约 0.84% CPU、737.5 MiB 内存，根盘使用率 53%；这些数值不是
  长期趋势，也不能用于认定内存泄漏。没有盲目套用 512 MiB 限额。
- 生产容器 `HostConfig.LogConfig` 为 `json-file` 且 `Config={}`，没有显式
  容量限制。24 小时日志约 34,917 行；本轮未读取到完整 Docker 日志文件大小，
  因此不声称已经占满磁盘。
- MiSub Pages 当前部署为 `70f03a6f-6cc5-401d-b693-fa7034a7fbe2`，源码
  `fa684ce`。带正确认证的 Manifest 返回 200 / success，含 90 个 proxy URI
  来源和 2 个 connector。初始 urllib 默认 User-Agent 被 Cloudflare 返回
  `403 / error code: 1010`；更换正常 User-Agent 后成功，不把该探测误判为宕机。
- Aggregator 的 stable manifest 仍指向
  `39894082699-1-b14d6df222dd`，生成时间 10 月 23 日 03:29 UTC，计数为
  37 个 effective 节点、25 个来源。实际 effective 正文 5,322 字节，SHA256
  与 manifest 一致。它是最后成功版本，不是 10 月 25 日的新发布。

05:27 UTC 进一步读取了全部 7 类产物的 stable 与 immutable 地址：14 个响应
均为 200，字节数和 SHA256 全部符合 manifest，核验前后的 manifest 完全一致。

## 云端失败与本轮优化

最近读取的 100 次 Actions 运行中，Aggregator 为 38 次失败、9 次成功；
MiSub Discovery 为 46 次失败、1 次仍在运行。这是读取窗口，不是全历史统计。

### 提前拒绝空候选产物

运行 `40156849438` 的 V2Ray 候选 URL 实测为 HTTP 200、正文 0 字节。
流水线原先先忽略空 optional V2Ray，再运行约 5 分 20 秒的节点审计并发布
effective 候选，最后在 `--require-all` 阶段因 `response body is empty` 失败。
stable promotion 未执行，保护旧 stable 的行为正确。

根级 verifier 新增 `--before-effective-audit`，严格检查已经由上游生成的
所有已配置产物，只延后尚未生成的 `public-effective` 和
`public-effective-json`。workflow 在昂贵审计前使用该模式；最终
`--require-all` 和 promotion 门禁不放宽。失败信息附带 artifact 名称，
例如 `public-v2ray: response body is empty`。

第一阶段这项改动只消除无效审计开销，不是根因修复。后续已经完成真实 converter
复现及本地根发布流程修复，证据见本文“第二阶段根因闭环”；尚未部署上线。

### 补齐部署日志轮转

Debian gateway 和 NAS Compose 模板对齐现有 service/base 模板，默认使用
`json-file`、`max-size=20m`、`max-file=5`，支持同名环境变量覆盖。
对应 YAML 合同测试覆盖三个模板。

该配置只对新建容器生效。应用到现役网关需要经批准的替换窗口；不能把模板修改
或 `docker restart` 说成线上已经限额。旧日志、镜像、配置和数据应按回滚要求
保留，不通过截断日志或清空数据来应用此优化。

## 代码审查修复

### 手动刷新完成通知竞态

`RefreshNow` 原先先投递刷新，再读取 `refreshDone`。若刷新很快完成并替换
channel，调用者会误等下一次刷新，最终返回 timeout。

修复在与完成侧相同的锁保护下先取得 channel、再做非阻塞投递。保持原有合并
刷新语义，没有扩张成新的逐请求任务系统。确定性屏障测试覆盖成功与失败结果：
修复前都返回错误的 `refresh timeout`，修复后正确收到本次完成结果。

### connector 超长日志导致管道停止读取

原 `bufio.Scanner` 遇超过默认约 64 KiB 的日志行会退出，之后 helper 的输出
可能填满 pipe 并阻塞。新逻辑使用有界读取，只记录首段并标记 `truncated`，
持续排空剩余片段，换行后正常继续；同时保留 EOF 尾行和读取错误诊断。

回归用 `io.Pipe` 输入 256 KiB 单行及后续普通日志，修复前 writer 无法完成，
修复后正常完成且输出大小受限。没有仅通过调大 Scanner 上限掩盖问题。

## 仍未宣称解决的事项

- MiSub Discovery 运行 `40132280308` 最后得到结构化
  `returncode=1 / verdict=unknown / error_present=true` 后退出；不能把
  unknown 当成节点确认不可用，更不能据此删除来源。本轮没有改写维护策略。
- NAS 的一次 Manifest TLS 超时及 fallback 已确认，但未定位链路底层原因；
  从开发机读取云端成功不能证明 NAS 后续同步已经恢复。
- 没有做全池业务适配、长期资源趋势或完整 UI 验收。小型 HTTP 204 测试不等于
  OAuth、长响应、大流量或每个 ECH 节点均正常。
- 第一阶段本地 HEAD 为 `edb34b8`。第二阶段在备份 diff 和逐文件 SHA256 后，
  仅快进至 `b14d6df222dd701b4723df783829385f433543ce`；确认原有全部修改文件
  字节未变。未创建 commit、未 push，也没有更新独立 submodule 的 gitlink。

## 开发侧验证结果

下表是第一阶段、快进之前的验证记录；第二阶段重新验证结果见文末，不混用基线。

| 检查 | 结果 |
| --- | --- |
| 根目录 Python 全量测试 | 最终 170 passed |
| 本轮相关 Python 聚焦测试 | 34 passed |
| `service/base` 的 `go test -count=1 -timeout=300s ./...` | 全部通过 |
| 两项新回归的 Windows `go test -race -count=10` | 通过 |
| 两项新回归的 Linux 测试二进制、无网络隔离容器、10 轮 | 通过 |
| `go vet ./...` | 通过 |
| Linux amd64 生产标签构建 | 通过；标签为 `with_utls with_quic with_grpc with_wireguard with_gvisor` |
| Debian gateway / NAS `docker compose config --quiet` | 均通过；仅解析配置，未部署 |
| Go formatter、有效行数门禁、`git diff --check` | 通过；6 个原有软行数警告未扩大 |
| 本轮全部修改文件的 UTF-8 无 BOM 检查 | 通过 |

保留首轮失败记录，不将复跑成功写成从未失败：Python 全量首轮为 166 passed /
4 failed，其中 3 个 PowerShell smoke 超时，独立复跑 15 项通过；另 1 个
Linux 安装测试因为本机 PATH 缺少 `cygpath`，把 `C:` 传给 GNU tar 而失败。
只在最终测试进程中添加现有 Git `usr/bin` 后，全量 170 项通过；没有修改
全局 PATH 或放宽测试。Linux 安装/回滚夹具还在无网络容器中单独通过，其中
`curl` 使用“若意外调用就失败”的占位命令，因为该夹具只走本地 archive 路径。

隔离测试容器均已退出，没有遗留运行中的验证服务；未删除容器或镜像。
这些验证不等于新版本已部署，也不代替完整生产 TUN/业务切换验收。

## 证据位置

脱敏现场快照、失败日志摘录、验证日志与构建产物保存在：

`C:/Users/Public/nas_home/AI/GameEditor/linshi/easyproxy-review-20261025/`

主要文件为 `live-inspection.safe.json`、`failures.safe.json`、
`consumption.safe.json`、`go-tests.log`、`go-race.log`、`linux-build.log` 和
`go-vet.log`。文件名中标记 safe 的内容不包含凭据或节点原始 URI。

## 第二阶段根因闭环

### Aggregator：VMess-only 转换与项目兼容订阅合同不一致

用仓库自带的 Windows subconverter 二进制，对合成 VMess、SS、Trojan、VLESS
及四协议混合输入运行 `v2ray/mixed/clash × list=true/false`，共 30 组对照。
`v2ray` 只保留 VMess；SS、Trojan、VLESS 单协议输入都以 exit 0 生成 0 字节。
`mixed` 保留这四种协议。最初自建最小 pref 缺少必填节而导致 converter 退出，
改用仓库完整配置并禁用外部 rules/insert 后，30 组全部正常执行；不把夹具错误
当成线上原因。

联网读取失败候选 `40156849438-1-b14d6df222dd`：mixed 含 SS 7、VLESS 21、
Trojan 4、VMess 0；V2Ray 为 0 字节。旧 stable 恰好含 VMess 1，V2Ray 为
380 字节且只有该节点。GitHub artifact 中该候选仅记录了 V2Ray PushSuccess，
没有 converter 内部诊断，因此“上传成功”不能证明产物非空。

新增根级 `publish-aggregator-v2ray.py`，从同一 run 的 mixed 候选生成现有合同
支持的五类兼容 URI，拒绝跨 run、stable key、空内容和无兼容节点。模板移除原
VMess-only target 的写入，保留原 storage key 和最终完整性门禁。独立 Aggregator
submodule 未修改。

对真实失败候选在内存中重放：修复前严格校验失败 `response body is empty`；
修复后 32 条兼容 URI、10,176 字节，严格校验通过。SHA256 为
`5d6589b4547d9820c31811ffaa10af140c52f8a2960bcd04d4ee493597dd0c58`。
未执行 R2 PUT，且这些是协议兼容节点数，不是已连通节点数。

### MiSub：探测与运行时 User-Agent 不一致

按当前 MiSub 数据的候选顺序，历史最后完成来源后的下一项为
`sub_6090dc63-6500-4bff-aff8-1d2518826475`。当前候选共 7 项；历史 run
`40132280308` 没有 artifact，无法恢复当时私有 summary，故当前顺序不能冒充
历史快照。但对此来源的以下机制已真实复现：

- 独立 public Session，UA `clash.meta`：HTTP 200、633 字节、Clash YAML，
  一个普通 `aes-128-gcm` SS 节点。
- 相同来源，运行时浏览器 UA：HTTP 200、530 字节、HTML，无代理 URI。
- 现有 Linux 镜像隔离审计：约 6.7 秒在管理 API 就绪前退出，日志为
  `subscription contained no usable nodes`，随后命中失败缓存并 bootstrap fatal；
  节点计数 0、pool 探测次数 0。没有向公共来源转发 MiSub 管理 Cookie。

修复 `subscription_fetch.go`：保留浏览器首选；仅当 HTTP 200 且解析成功但
零节点时，再尝试一次 `clash.meta`。两个 UA 和既有 HTTP/1.1 回退共享同一总
context deadline；非 200、传输/读取/超限错误不会触发 UA 回退。成功和失败缓存
仍在完整协商之后写入。已有负面缓存不会自动清空，应等 TTL 到期或隔离复测。

用 Go overlay 将原始源文件与新回归测试组合，稳定重现零节点失败；当前代码通过。
再将带生产标签的新 Linux 二进制只读挂载到隔离容器，对同一真实来源复测：
管理 API 就绪、加载 1 节点、执行 6 次 pool 探测。该节点在此环境中没有通过
连通性探测，最终是 `AuditProbeUnavailable`，而不再是启动/零节点错误。
该次使用普通独立 audit 模式，其 CLI exit 1 不等于 maintenance 的生命周期
错误；maintenance-owned 模式的预期负面 exit 2 及 cleanup 失败阻断由回归测试
单独覆盖。没有据此运行 maintenance `--apply` 或删除来源。

这证明已修复当前可复现的内容协商/启动故障；不宣称历史每次 unknown 都同因，
也不宣称节点连通性、云端维护批次或 NAS 后续同步已恢复。

### 安全诊断补充

审计 summary 增加实际执行阶段 `failure_stage`，父维护脚本只输出固定枚举和
经过类型/范围校验的计数：total/available/stable 节点数、pool 尝试数。任意异常
文本、URL、凭据、节点 URI 不进入新增日志字段。非法摘要仍 fail-closed，原有
unknown 不删除、终止未确认保留资源、禁止部分 apply 的合同不变。

### 第二阶段验证与产物

- `phase2-python-full-final.log`：当前基线完整 Python 测试 **247 passed，
  59 subtests passed**；包含最后新增的实际管理启动失败阶段诊断测试。
- `phase2-config-before.log`：原代码 overlay 下新回归按预期失败。
- `phase2-config-tests.log`、`phase2-go-tests.log`：配置包及 Go 全量通过。
- `phase2-race.log`：内容协商和前阶段生命周期回归，Windows race 10 轮通过。
- `phase2-linux-regression.log`：Linux 配置回归、无外网容器、10 轮通过。
- `phase2-linux-build.log`、`phase2-vet.log`：生产标签构建与 `go vet ./...` 通过。
- `phase2-actionlint.log`：修改的 workflow 通过 actionlint（shellcheck/pyflakes
  未启用）；Python 与 YAML 合同由测试另行验证。
- 有效行数门禁检查 472 个已跟踪文件通过，6 个原有软警告；另对本轮新增文件
  单独计数。全部 22 个本轮修改/新增文件为 UTF-8 无 BOM，改动源码均不超过
  500 有效行；`git diff --check` 通过。
- `converter-matrix.safe.json`、`v2ray-repair.safe.json`、`single-audit.safe.json`
  和 `single-audit-after.safe.json`：真实转换和单来源复现的安全摘要。

新增诊断测试首轮有 6 项失败，是测试把故意畸形的摘要预期成正常 unknown；已修正
为断言 `RuntimeError`，没有放宽生产分类器。对应失败日志保留。

全部真实复测容器均已停止，保留容器、镜像和隔离证据；生产容器没有重启或替换。

06:13 UTC 最后只读复核：NAS 仍为同一镜像和 10 月 2 日 StartedAt，Running=true、
RestartCount=0、OOMKilled=false，日志轮转配置尚未应用；stable manifest 仍为
`39894082699-1-b14d6df222dd`、37 节点/25 来源。无运行中的本轮验证容器。
详见 `phase2-final-status.safe.json`。这明确区分了本地修复与生产上线状态。
