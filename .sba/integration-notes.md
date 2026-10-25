# EasyProxy SBA 实施边界与验证记录

本文件按时间追加实施证据；早期的“尚未实现”是当时停点，不代表当前状态。当前入口、
五动作、刷新与声明见 [生命周期合同](lifecycle.md)。应用尚未正式上线；负责人均为主 AI。

## EP-SBA-02：R2 单写入者

### 经联网核实的依赖

2026-10-25 读取 Cloudflare 官方 cloudflare-docs production 源：

- [S3 API](https://developers.cloudflare.com/r2/api/s3/api/)：PutObject 的 If-Match、
  If-None-Match 条件与提交原子求值；并发创建最多一个成功，其余返回 412。
- [一致性模型](https://developers.cloudflare.com/r2/reference/consistency/#conditional-writes)：
  直接 S3 读取强一致；负读取不表示先前未知的在途写入绝不可能稍后提交。

公开网页在当前网络返回 403，因此实际核实的是同一官方仓库的
src/content/docs/r2/api/s3/api.mdx 和 reference/consistency.mdx，保留本地证据；没有关闭 TLS 校验。

### 记录和状态转换

固定键 _sba/control-v1.json。记录只包含公开身份、资源清单摘要、随机 revision、已提交
版本、当前 writer 及 retired 状态，不含 API token、业务密码或订阅正文。

1. 第一次 deploy/preview 检查 bucket 为空，以 If-None-Match: * 原子创建 held 记录。
   不接管非空但没有该记录的旧 bucket；资源归属仍须调用方在任何业务写入前核验。
2. 已有实例只能以 If-Match: 原 ETag 将空闲记录改为 held。持久版本必须等于请求的
   previous；update 必须更高版本且 SHA 不同，refresh 必须与 active 完全相同。
3. writer 持有期间拒绝其他操作；没有 TTL、心跳抢占或后台回收。进程重启不会解锁。
4. 真实发布和动作要求的检查完成后，调用 commit_verified。它重新读取并核对完整记录、
   ETag、operationId，再以 If-Match 同时提交 active 和释放 writer。每次空闲也生成新
   revision，旧 ETag 不能因空闲记录重复而再次生效。
5. 预升级环境不允许周期刷新；正式环境不允许 destroy-preview。预升级 retired 是终态，
   保留控制记录，不提供销毁云资源的实现，也不将该标记当成清理回执。

调用方必须在 begin 返回许可后才进行业务写入，且在 commit_verified 前确认所有远端
异步发布已经结束、所有子进程退出、所有要求的回读完成。不能只取得许可后继续后台
写入，也不能在 finally、取消处理或失败路径中释放。该模块不是任意代码/凭据的沙箱。

### 失败语义

只有明确的 412 PreconditionFailed 视为已拒绝的冲突；网络中断、缺少 ETag、异常响应
或 SDK 错误均为安全 UNKNOWN。写入不重试，也不通过负读取认定之前未知的写入失败。
如果完成提交已实际生效但响应丢失，远端可能已经空闲且 active 已更新；调用方仍报告
unknown，不重放原动作。后续新任务只能按实际持久状态和平台审批继续。

S3 客户端使用显式凭据、固定 HTTPS 账号 endpoint、有限连接/读取截止、SDK 总尝试一次；
额外 before-send 栅栏阻止 SDK 区域重定向绕过普通 retry 配置再次发送控制 PutObject。
不接受环境中的 AWS session token 或 endpoint 替换，也不输出 provider 原始错误。

当前没有自动或手工解锁命令。未知操作的恢复必须先停止并证明原写入者已经静止，核实
云端实际状态和资源完整性，再设计独立审批恢复路径；不能删除控制键后直接重跑。

### 已验证和未验证

本地覆盖 24 个并发更新/刷新竞争、版本切换、资源漂移、空 bucket 归属、重启、旧句柄、
检查失败、损坏状态、获取/释放响应丢失及禁止 SDK 自动重发。真实 boto3/SigV4 请求送往
本机合成 HTTP S3，检查 If-Match/If-None-Match、会话 token 隔离和固定 endpoint。

这些不是真实 R2 云端写入验收，也没有把旧 GitHub Actions 工作流改接本栅栏。旧 workflow
仍只依靠原仓库 concurrency；新标准套件只面向独立新资源，既有部署导入不在本切片范围。

## EP-SBA-03：私有 R2 出口与 ECH 组合

新套件将同一个 ECH Worker 的入口设为 .sba/edge.mjs，并为它绑定 AGGREGATOR_STORE。
原 workers/ech-workers-cloudflare 入口和配置不变，没有新增常驻 Worker 或代理控制服务。
套件发布前还必须在 Wrangler 生成配置中明确绑定批准的 bucket；目前尚未生成该配置。

公开产物的 base URL 可由批准的 ECH origin 加 /aggregator 得到，不需要用户预先取得
随机 r2.dev URL，也不需要公开整个 bucket 或增加 DNS 操作。其值不是新的外部配置入口。

公开路径是与现有根发布合同一致的七个固定 artifact，以及明确的 candidate、releases、
last-known-good 和 stable manifest 路径。internal/crawledsubs.json 是既有发布合同中
已公开且 verifier 必需的来源清单，继续按公开产物对待；不得将私密来源放入该公开清单。
原始 crawledproxies、_sba 控制记录、备份和任意新键不公开。未来新增产物必须单独审核
允许列表，不能改成整个前缀透传。

仅支持 GET/HEAD，不从请求拼接远端 URL，不复制对象的任意 HTTP metadata。稳定/候选
路径 no-store，固定 release 路径可 immutable 缓存。大小限制 64 MiB，错误只返回固定
文本，不泄漏 provider 异常。R2 ReadableStream 在 workerd 可能以流式传输返回，因此 GET
不保证 Content-Length；正确性以正文和内容摘要验证，不把该可选响应头当作业务成功。

/.well-known/easyproxy-release.json 仅在合法 SHA/稳定版本配置存在时返回公开版本信息，
不包含 ECH token；它不是完整业务健康证明。ECH 仍须完成独立 WebSocket/鉴权检查，
MiSub/订阅消费也须分别验收，不能拿普通 200 或版本元数据代替完整验证。

已通过 Node 单元测试，以及真实本地 workerd/R2 的正文、HEAD、私有键拒绝、版本元数据、
原 ECH 根路径、无 token 拒绝、有效 token 的真实 WebSocket 升级/关闭和禁止写入回归。
未进行外网 TCP 连通性或云端业务部署验收。

## 接续与证据

- SpringBok PR #112/#113 已合并；本轮重新读取 d9b327d24fa2a7707a7dff4c6202b818d581f178
  main 的七个工作流，全部 completed/success；没有更新生产控制面或执行器 pin。
- 固定源准备再次在真实根 b14d6df222dd701b4723df783829385f433543ce 及三个 gitlink 对象上
  执行。缓存里的未提交优化不进入批准 checkout，不修改根或子模块 HEAD。
- 本轮没有提交或推送 EasyProxy，没有读取/轮换生产秘密，没有部署 Worker/Pages 或改动
  生产定时任务。此前 dirty edits 全部保留。
- 测试、源码准备、联网参考和 workerd 数据位于 linshi/easyproxy-sba-20261025/。

本轮最终验证：

| 范围 | 实际结果 | 证据 |
| --- | --- | --- |
| .sba Python 源码准备/协调/S3 传输 | 63 passed | sba-05.xml |
| 原有根 Python 回归 | 247 passed、59 subtests passed | root-04.xml |
| 产物路由/真实 workerd/原 ECH 回归 | 23 passed、无 skip | node-05.xml |
| 真实固定根及三个 gitlink 准备 | 成功、无云写入 | real-pinned-sources-03.safe.json |
| 有效行门禁 | 472 个既有纳入文件通过、6 个既有 soft-limit 警告 | 本轮终端输出 |

另检查本轮 .sba 新文件均为 UTF-8 无 BOM、无行尾空白，既有 diff check 通过，Gitleaks
扫描本轮快照无泄漏。原有行门禁只枚举已跟踪的既有目录，不冒充已覆盖未提交 .sba；本轮
对新增源码另行计数并检查低于 500 行软限制。上述本地结果不等于云端验收或 GitHub CI。

验证期间的环境诊断也保留：

- 根回归第一次的 Linux installer 测试因 PATH 无 cygpath，将 C: 误当远程 tar 地址；
  只在测试子进程中补齐已有 Git 工具链后，独立测试及全部根回归通过，未修改安装器。
- 一次 SBA fixture 的 Git rev-parse 返回码为 0 但 SHA 为空，测试准备因此失败。
  原 sba-04.xml 保留；两套已有 Git 各 30 次只读检查均未复现，不宣称已查明其根因。
  测试明确使用已有 Git 2.42.0，fixture 增加 UTF-8、截止和 SHA 断言后完整通过；没有
  加入自动重试或放宽产品检查，生产准备器本来就拒绝非准确 SHA。
- Gitleaks 最初把 RFC 示例中的固定 WebSocket nonce 识别为通用 API key；测试改为按
  协议为每次请求生成随机 16 字节 nonce，重新跑真实升级与扫描通过，没有加入忽略规则。

下一步是复用现有 easyproxyctl、Wrangler 配置生成与发布脚本实现真实五动作，再完成
GitHub 实例环境及批准 SHA 调度。尤其要审核旧同步脚本的写重试，确保 SBA unknown 不
被内部重试绕过。全部动作就绪前不添加可被 SpringBok catalog 接受的空壳 manifest。

## 2026-10-26 续进：EP-SBA-04 受保护迁移

统一构建工作区、真实 CLI 备份/迁移、私有 R2 持久备份与写前摘要绑定已实现，具体合同
见 [migration.md](migration.md)。这修正了上面“尚无可执行迁移阶段”的历史停点，不改变
“五动作仍未完整接通”的总体状态。没有修改独立 upstream、旧生产配置或现有工作流。

Cloudflare D1 query 的 endpoint、sql 请求及 success 结果结构已在本轮联网核实官方
cloudflare/cloudflare-go main 的 d1/database.go，保存为证据目录中的
cloudflare-official-d1.go.txt；没有关闭 TLS 校验，也没有调用真实云端写接口。

## 2026-10-26 续进：EP-SBA-05 ECH 实际发布协议

`ech.py` / `worker_api.py` 已接到共同许可：批准源构建、准确私有 R2/secret/版本绑定、
Cloudflare multipart 上传、observability 保持与读回、Worker 子域开关和公开版本验证。
这里使用直接 Workers API，不再需要上面 EP-SBA-03 当时尚未生成的 ECH Wrangler 发布
配置；MiSub Pages/Functions 的配置和实际发布仍待接入。详细合同见 [ech.md](ech.md)。

交叉复核发现并已修复：共同 writer 本身不能阻止同一 Permit 重入发布，因此补充不可
覆盖的阶段标记与本进程单次尝试保护；源码 clean 也不能代替构建输入检查，因此拒绝
链接路径和树外 import，并核对真实 esbuild 输入清单。固定编译器读取、核验后执行字节
副本，避免重新执行可变安装路径。公开版本允许有界只读传播轮询，不重发任何发布写入。

最新代码的验证分组执行，避免把较早 102/103 项全量结果冒充最后补充后的统一证据：

| 范围 | 实际结果 | 20261026 证据 |
| --- | --- | --- |
| SBA 固定来源/共同栅栏/真实 S3/受保护迁移 | 77 passed、无 skip | sba-core-05.xml |
| ECH 实际构建/HTTP multipart/归属/UNKNOWN/并发发布 | 27 passed、无 skip | sba-ech-05.xml |
| Node 产物路由、真实 workerd、原 ECH auth/session | 22 个叶子用例通过、无 skip；Node 另计 1 个父测试 | node-08.xml |
| Go 全部包，含 SQLite/age、写前备份摘要及 sqlite_sequence | 80 个 test pass events、9 packages、无 skip | go-06.safe.json / go-06.jsonl |
| Go vet、实际 CLI 构建与 version | 通过，CLI 0.3.0 | easyproxyctl-verified-06.exe、终端输出 |
| 原有根 Python 回归 | 247 passed、59 subtests passed | root-01.xml |
| 真实批准根及三 gitlink 的统一工作区 | 成功，开发工作树未改 | real-unified-workspace-01.safe.json |

workerd 使用 ECH 发布阶段真实编译的 bundle，而不是另做一个替代 bundle。HTTP API 是
本机合成服务，S3 也是本机合成服务；没有向真实 Cloudflare 账号发送写入。第一次 ECH
测试暴露的是测试 fixture 漏复制 auth.mjs，以及错误资源测试修改了 owner 而非批准清单；
已修正 fixture/测试边界，原失败 XML 保留，不以跳过测试掩盖问题。

当前新增/修改接入文件单独检查 UTF-8 无 BOM、行尾空白和有效行数；已跟踪文件继续跑
原行数门禁与 diff check。原门禁 472 个文件通过、6 个既有 soft-limit 警告；不宣称它
自动覆盖未跟踪的 .sba。接入快照另经 Gitleaks 扫描，证据位于同一 linshi 目录。

此轮没有修改 SpringBok，当前本地 main 仍为 d9b327d24fa2a7707a7dff4c6202b818d581f178、
工作树干净；没有更新其正式控制面或执行器 pin。EasyProxy 仍未提交/推送，先前 dirty
edits 和独立 upstream 全部保留。没有读取、修改或轮换 aikey，也没有迁移旧生产资源。

下一步仍是 Pages/Functions 实际发布、Aggregator 实例环境与批准 SHA 调度，以及旧写
重试/自动恢复路径适配；随后补齐独立 verify、preview、destroy-preview 与固定工具链
交付。五动作未齐之前继续不提供可部署 manifest/deployment 或成功空壳入口。

## 2026-10-26 续进：EP-SBA-06 MiSub Pages / Functions

Pages 实际发布阶段已实现，修正上一节的待办停点；详细协议和边界见 [pages.md](pages.md)。
`pages_build.py` 通过批准 gitlink 的隔离副本和准确锁文件，真实运行 npm ci、Vite 和固定
Wrangler 的本地 Functions build；不改 upstream，不运行 Wrangler 云端写命令。
`pages_api.py` 发送官方 Direct Upload 请求，`pages.py` 将其接到共同许可与单次阶段标记。
HTTP 安全传输最小提取为 `cloudflare_http.py`，ECH 继续保留自己的错误类型与发布合同。

官方源码本轮已联网核对，保存于新的
`C:/Users/Public/nas_home/AI/GameEditor/linshi/easyproxy-sba-pages-20261026/`：
Pages project/deployment SDK、Workers SDK Direct Upload、资产 hash/upload、嵌套 Worker
bundle、固定 Wrangler 4.93.1 本地 build，以及官方 CI Direct Upload 权限说明。
账户 token 和项目上传 JWT 分开；`_worker.bundle` 不错误地当成 JS/ZIP；最终成功必须看到
deploy/success、准确 canonical deployment、绑定读回和公开版本，不因 active stage 提前结束。

### 本轮实际验证

| 范围 | 结果 | 证据 |
| --- | --- | --- |
| Pages API、归属、业务秘密、UNKNOWN、并发、实际构建产物 multipart 上传 | 38 passed，0 skip | pages-04.xml |
| 来源、gitlink 再检查、共同栅栏、真实 S3、迁移阶段 | 81 passed，0 skip | core-01.xml |
| 共用传输后的 ECH 构建与发布回归 | 27 passed，0 skip；按 ECH classname 单独统计 | tests-01.xml |
| Pages/ECH 真实 workerd、产物路由与原 auth/session | 25 个叶子用例通过，另 2 个父测试，0 skip | node-02.xml |
| 实际 npm/Vite/官方 Functions 编译 | 99 个资产、2,094,199 字节；bundle 1,180,866 字节 | real-build-04.safe.json |
| UTF-8 无 BOM、行尾空白、语法、文件摘要和扫描快照 | 独立记录，不把 tracked gate 当成自动包含 .sba | hygiene-04.safe.json / reviewed-sba-04 |

`tests-01.xml` 的另一部分是较早 Pages 用例（33 passed、1 skip），当时还没有设置真实产物
路径；**不使用那部分作为最终 Pages 验证**。最终 Pages 证据是显式设置真实构建目录后
的 pages-04.xml。Node 同样使用 real-build-04 的实际 Functions bundle、实际前端和本地
D1；拒绝外部网络，验证版本 GET/HEAD/405、原登录/Cookie、数据读取与跨源写入拒绝。
HTTP provider 和 R2 仍是本机合成服务，不能将它们表述为真实云端发布或线上验收。

第一次锁文件验证拒绝了上游历史 npmmirror URL；已改为仅在构建副本规范化到官方 npm
相同路径，保留原版本/SHA512 integrity，未修改独立 upstream。第二次构建完成；随后
加上显式 strict-ssl 后的第三次构建遇到 npm `ECONNRESET`，在任何云端操作前停止。
保留失败目录 real-build-03，再选全新 real-build-04 完整安装、编译并验证通过；没有关闭
TLS、对云端写入加重试或复用失败输出目录来掩盖问题。

真实构建基于批准根 b14d6df222dd701b4723df783829385f433543ce 的准确三个 gitlink；为了
测试尚未提交的两个接入适配文件，仅在 linshi 的独立源码仓建立本地 fixture 提交
3103fb692e54bf1978ea54f1c6cb8f2f70716914。它不是已批准/推送的应用发布，不表示开发
工作树的未提交接入代码已经进入原批准根。生产执行仍须完整接入完成后的独立批准源。

此轮没有更改 SpringBok、现有生产配置或旧工作流，没有读取/修改 aikey，没有真实云端
写入、资源迁移、删除、提交或推送。现有其他 dirty edits 全部保留。

下一块是 Aggregator 实例 Actions environment/secrets/vars、批准 SHA 调度，以及旧内部
写重试/自动恢复适配；随后串接五动作和独立业务 verify、preview 数据副本/源不变证明、
受批准测试资源的销毁和固定执行器工具链。五动作仍未齐，继续不提供可部署 manifest。

## 2026-10-26 续进：EP-SBA-07A / 07B 与 Linux 平台前置

Aggregator 候选、真实消费审计 wrapper、immutable/LKG/stable 发布及 MiSub 单次受限
管理同步已经实现，详见 [aggregator.md](aggregator.md)。实际 workerd 发现原保存 handler
会重探测 disabled 用户订阅，因此采用 app-owned 受限 endpoint，而非仅包装原保存接口。
第二次 profile 未变同步、数字管理 ID、同源/准确源身份、固定 16 MiB 缓冲及超限取消均有
回归；没有修改独立 upstream。多行 D1 写不宣称原子 CAS，UNKNOWN 保留 writer。

`all-17.xml` 完整 Python 203 passed、无 skip；最终 `real-build-18` 的实际 Functions
bundle 与原前端、D1、签名 Cookie、两次同步、数据保持及拒绝用例共 11 项 Node/workerd
通过、无 skip，outbound 为空。接入快照 40 文件 UTF-8 无 BOM、源码最高 410 行，Gitleaks
无泄漏。上述证据位于 `linshi/easyproxy-sba-pages-20261026/` 与
`linshi/easyproxy-sba-aggregator-20261026/`，不等于真实云发布或线上同步。

用户正常 owner 登录已完成。SpringBok SUITE-03 正在独立交付固定 Linux 原生执行器，
以支持真实 Docker 消费审计而不二次 dispatch 或跨进程序列化 Permit；本地 Ubuntu24 / Node22
验证通过，发布状态以其总计划为准。本应用固定 Linux 构建工具/生成器/镜像、GitHub
实例环境与批准 SHA 调度、五动作和最终真实业务验收仍未完。旧生产任务和资源保持，
尚未提交/推送 EasyProxy，旧部署删除仍等待准确清单、备份及用户明确“同意”。
