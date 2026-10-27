# EasyProxy 云套件 SBA 接入

## 当前状态（2026-10-27）

1.0.10 已经正常 SpringBok 恢复、独立 verify 及本地双来源真实 TLS 消费验收。
准确部署任务为 `dc-9e968056fd18416a915a2f220b1bf81e`，独立验证任务为
`dc-9f85e79bf52a4570b0cdc313ba93159d`。原 UNKNOWN 回执保留，不改写成成功。

1.0.11 全新部署任务 `dc-92bbbbc744784ec6adf13a37ddd68f97` 在生成输出阶段 UNKNOWN。
1.0.12 已正常发布并通过 main CI 与无部署凭据 hosted 预检；经正常 SpringBok 单次继续的
`dc-730884501d3a4bea85ebea4795196e81` / run `40480678166` 返回
`EASYPROXY_SBA_EDGE_UNKNOWN`，仍未上线成功，原回执均保留。
旧部署已按批准清单清理；新 D1 已初始化，GitHub environment 与唯一新刷新项已配置，
Worker 及其自定义域名已建，Pages 尚不存在，R2 保留五个 control/阶段对象与非空 writer，
`active=null`。不能复用原 pristine 入口、清锁、重做初始化或自动重发部署；全部备份继续保留。
下述自定义域名支持复用 SpringBok
已有 zone/subdomain 选择器，配置仍为 11 键；仅自定义域名配置追加准确 domain inventory，
旧 provider-only inventory/digest 与八项准确 repair 合同保持。先真实核对账号、活跃 zone、
DNS 和 association，空闲域名单次绑定，传播等待仅 GET；UNKNOWN 停写并保留 writer。

公开 MiSub/Worker 入口可使用同账号子域名，Pages callback/public URL 随公开入口配置。
自动 managed-sync、周期 refresh 与升级备份的管理密码、Cookie、manifest token 始终发往
准确项目的 `pages.dev` 管理地址，不跟随可变的自定义 DNS；订阅和 ECH connector 使用
批准的公开 Worker 地址。自定义公开产物地址必须与对应 Worker inventory 精确匹配。

1.0.12 修复隔离生成的职责边界：这里只收集、去重和格式转换，不先用 upstream 的
旧 Clash 内核裁剪候选。原 1.0.11 的无凭据 hosted 预检和本地生成均复现
`public-mixed.artifact` 缺失；本地输出仅剩 HTTP 节点，旧转换器无法把它们导出为 mixed。
对照实验仅取消旧内核预筛即可生成全部必需格式。使用明确的 `--skip-alive-check`，
不继承环境开关、不添加假节点、不补空文件、不放宽 TLS 或格式校验。
这不代表候选可用：后续准确 EasyProxy 镜像的真实消费审计、有效节点数量、晋级、同步和
独立公开验证仍是原有硬门禁；审计失败不得发布稳定版本或提交套件版本。
准确 1.0.12 的 hosted 预检已通过生成阶段，但此次后续 Edge UNKNOWN 阻止了套件提交；
尚未执行新实例独立 verify 或真实双来源消费验收，不能把预检或 Worker 版本可读当作业务成功。

针对原准确新 deploy，独立 `continue-fresh-pristine-deployment` 只接受原
1.0.11 task/run/请求与结果摘要及完整配置摘要（含同一 D1/R2 与两个批准公开域名）。
这不是八个历史 repair 的泛化：先只读核原 executor/原 ZIP 回执、R2 零对象且无 control、
D1 身份和空 schema、Worker/Pages/environment 不存在、空刷新 registry、可用 workflow、
活跃 zone 与空 DNS/association；本地构建后、首次写入前再完整重核一次。任一漂移停写。
没有旧 writer，因此只走首次 `begin("deploy")`，不 CAS、不清锁、不删除或重建资源，
不重写原 UNKNOWN。仅执行此前尚未发生的初始化与完整发布审计；再次 UNKNOWN 不自动恢复。
该次已完成正常发布、无部署凭据预检和新鲜现场核对，并经 SpringBok 正常 UI 单次执行；
批准已消费。当前非空 writer 与已建资源不再满足 pristine 前置，不能用本入口再次恢复。

本次只读 Cloudflare 审计记录：域名 PUT 为 HTTP 200，但其资源响应中已废弃的
`environment` 为 `""`，随后 GET 为 `"production"`；其余域名 ID、service、zone、hostname
及 enabled/previews_enabled 一致。旧代码强制创建响应也为 `production`，会在已创建后拒绝。
本地窄修仅允许该 PUT 响应的空字符串；后续 GET、已有域名/升级核验仍严格要求 production，
并保持完整所有权、DNS、路由、只读传播、阶段证据与 UNKNOWN 停写约束。
用户已另行批准本次准确受限方案，候选1.0.13新增独立第十入口
`continue-fresh-domain-ack-deployment`。仅接受上述1.0.12 repair 的准确原 task/run/executor、
固定 ZIP/请求和结果摘要、完整配置、control 字节/ETag 与五个对象正文/ETag；九旧入口不变。
新鲜只读证明同一已初始化且空业务 D1、完整 GitHub 配置、Worker 同业务 bundle/两个 origin、
准确域名/DNS identity，以及 Pages/domain 仍不存在；任一漂移零写入。先 create-only 移交证据，
再单次 CAS 移交始终非空 writer，不改旧对象或原 UNKNOWN。D1/GitHub不重做；Worker仅单次
新版本一致性发布，不再次 enable-subdomain；已建 Worker domain/DNS只读，不attach，独立
归属证据写在新 operation。Pages/domain走尚未发生的首次发布，后续审计/晋级/同步门禁不变。
本地实现不等于已发布或已恢复；正常平台控制面/固定Linux executor及应用 PR/main CI、完整
原生回归和准确 main 无部署凭据预检后，才经正常 UI 单次提交。新 UNKNOWN立即停写，
独立 verify与真实双来源TLS消费全部通过前保留备份。

隔离 preview 仍使用 provider origins；其正式源可以使用自定义域名。自定义域名 preview
及其清理在任何云操作前拒绝，不声称已经支持正式套件的域名删除。旧资源清理按本轮已批准
的准确清单另行执行，不隐式接管或覆盖旧资源。新部署与真实测试全部通过前不删除备份。

## 历史实施记录

以下是各阶段当时状态，不代表当前线上仍停在对应 UNKNOWN。

### EP-SBA-21：Pages 资产协议窄修与准确 1.0.9 恢复（历史，已完成）

1.0.9准确任务`dc-61ea1053fb0d425a9c755531a9e998af` / run`40396870242`已在
`PAGES_ASSET_VERIFY`返回UNKNOWN，尚未形成Pages deployment；不是套件上线成功。
1.0.10移除官方协议未保证的“上传后、登记前check-missing必须立即为空”门禁，保持
首次列表严格校验、每次远程操作前owner核对、单次上传/登记及部署后全部验证。
此机制已在合成提供方复现，但旧线上分支不能唯一倒推，owner读取失败仍可能产生同码。

新增独立`continue-pages-asset-protocol-deployment`，previous和资源父版本均为准确1.0.9；
仅绑定上述原回执、control SHA/ETag、16对象/15历史正文及全部资源，保七旧入口和十一秘密。
先只读核验、create-only移交证据、再单次非空writer CAS；不重放D1初始化/GitHub配置，
Worker同业务bundle，Pages复用原项目，任何UNKNOWN停写、不盲重放或清锁。
正常发布和一次无写凭据预检后，现场刷新无漂移才提交准确恢复。成功后仍须独立verify
及本地真实双来源TLS消费；旧服务不删除。当前实施状态不预报生产成功。

### EP-SBA-20：准确 1.0.7 失败的新受限恢复入口（历史）

用户已明确批准方案203：1.0.9新增独立`continue-generation-outputs-deployment`，只接受
task `dc-d8fbaaa6c7404904928900e9f6a8c9f1` / run `40358960520` 的准确上下文和
previous 1.0.7。旧六个repair、十一秘密及其他manifest字段保持；新入口不是通用重试。
资源父版本仍是1.0.5，不把未发布的1.0.7候选当作线上资源。

执行前严格重核最新原始回执、全部祖先终态、control字节/ETag、十三对象及十二历史正文、
D1/GitHub/Worker/Pages。原始ZIP下载绑定固定摘要、有界内容及严格envelope/result；
签名下载使用独立无Bearer会话，不允许第二跳，不导出签名URL或异常正文。
任何漂移零写入；先create-only证据，再单次CAS移交始终非空的writer。D1初始化和GitHub
配置只读复核，不重放；Worker保持同业务bundle，Pages复用原项目且最多单次部署。

正常PR/main CI和完整隔离门禁后，只执行一次准确新main的无部署凭据hosted预检。
通过并刷新全部现场后，才经正常SpringBok UI单次恢复；新UNKNOWN立即停写，不自动再次
恢复、清锁或回滚。成功后另行独立verify与真实双来源TLS消费；旧服务不删除、禁用或退役。
当前只完成本地入口及定向验证，尚未提交该恢复，不宣称预检、恢复或正式上线成功。

### EP-SBA-19：单个坏订阅拖掉整批好结果的修复（已发布源码）

在批准 gitlink 的真实进程池中已禁网复现：两个任务里一个抛普通异常，原
`pool.map()` 使整批结果返回空列表，成功任务也丢失。隔离 runtime 现在对准确
`workflow.executewrapper()` 做 hash-bound 最小补丁：只捕获 `execute()` 的普通异常，
返回原 taskid 与空节点列表，不过滤结果位置、不重试、不输出异常或订阅信息。
其他成功任务保持；取消、无效任务结构和父进程池故障不在此容错范围。

TLS 与任务隔离补丁均绑定修改前后完整 SHA256，生成后必须核对恰好两个固定
provenance 项及实际字节。原 upstream/gitlink 不改。全任务失败仍由必需产物、内容、
真实审计及数量门禁拒绝；不补空文件、不放宽发布条件、不移交 writer。
这修复的是已经复现的代码缺陷，不是原生产 UNKNOWN 根因已被唯一定位；原任务仅
141 秒，已排除总预算耗尽，但现有留存证据不含原私有生成日志，不能倒推其实际失败分支。
该修复已由PR24正常发布，main为`7df6b7da5d8b6bb6a928cc50428ac909b68c39e8`；
PR/main CI、禁网Linux1207项/零skip、Go82项/零skip及vet、真实公开采集均通过。
这些是上一轮源码与隔离验证证据，不替代1.0.9门禁或新的生产业务验收。

1.0.7 已通过正常 PR/main CI 发布，并于 00:25:18 经 SpringBok 单次提交任务
`dc-d8fbaaa6c7404904928900e9f6a8c9f1` / run `40358960520`。应用回执为
`EASYPROXY_SBA_GENERATION_OUTPUTS_UNKNOWN`，不是部署成功。00:30 完整只读后验确认
原 control/ETag、十三对象、D1/GitHub、Worker 1.0.5 和 Pages 零 deployment 均保持。
本次尚未进入 writer 移交或 Pages 发布；未重试、清锁、verify、本地消费或删除旧服务。

### EP-SBA-18：仅生成输出的有限诊断（负责人：主 AI）

该历史阶段仅批准补充、隔离验证、正常发布诊断及一次无部署凭据生成预检，不含生产恢复。
1.0.8诊断已发布，唯一hosted run `40374394955`通过，但不是线上版本；当时未新增repair。
输出读取仅允许 `public-clash.artifact`、`public-singbox.artifact`、`public-mixed.artifact`、
`crawledsubs.artifact`，失败原因仅为 `missing`、`not-regular`、`symlink`、`path-alias`、
`stat-error`、`path-error`、`permission`、`read-error`。仅在生成输出失败的只读预检报告中
附加 `outputDiagnostic`；不导出路径、异常原文、日志或产物正文，不再次访问文件以诊断。
生产阶段码仍为 `EASYPROXY_SBA_GENERATION_OUTPUTS_UNKNOWN`，单次生成和原产物字节/大小
上限及后续验证保持；该历史诊断批准不构成再次恢复权限，新授权仅以上述EP-SBA-20为准。

### 1.0.5 Pages 中断现场（历史父资源保持）

五动作入口、实例周期刷新、manifest v3 与 deployment v2 已实现；1.0.5 的准确单次恢复任务
`dc-7affcab05da84f4f938d4430aa9b4799` / run `40323259410` 返回
`EASYPROXY_SBA_PAGES_UNKNOWN`，不是 Actions 成功即应用成功。只读核实 `active=null`，
writer 仍锁定 1.0.5；R2 共十三对象，原九个阶段对象逐摘要保持。Worker 公开版本为 1.0.5，
Pages 配置为 1.0.5，但零 deployment，latest/canonical 均为空；D1 与 GitHub 配置不变。
首次部署和五次独立 repair 均未完成；没有自动重放或清锁，旧服务保持。
应用尚未通过 SpringBok 正式上线，不能把本地测试、构建或平台上线当作应用上线。
当前完整合同及边界见 [生命周期与调度](lifecycle.md)。旧手动部署及工作流保持不动。

### EP-SBA-17：准确 1.0.5 的单次受限恢复（负责人：主 AI）

用户已明确批准独立入口 `continue-pages-diagnostic-deployment` 的实现、验证、正常发布及
单次执行；1.0.7 已执行但在生成输出阶段 UNKNOWN，未进入移交和发布，不是线上版本。入口只接受上述
准确父任务、run、请求/结果摘要及 1.0.5 previous，不改变五个旧 repair 的匹配和秘密范围。
先核全部父/祖先终态、准确 control 摘要和 ETag、十三对象集合及十二历史正文、D1/GitHub、
Worker/Pages 准确 1.0.5。任何漂移零写入；先 create-only 保存新移交证据，再单次 CAS 将
始终非空的 writer 移交。旧 Permit 失效，全部历史证据保留，不重放 D1 初始化/GitHub配置。

随后仅发布相同 Worker 业务 bundle 的新来源元数据、复用 Pages 项目，按新候选构建 hash
查询并只上传缺失资产，不要求缓存为空。Pages 成功才继续原 Aggregator 审计/晋级、受限
MiSub 业务同步及独立读回；该正常同步可能写业务数据，不等于重放数据库初始化。
全部成功才 commit/release writer，之后独立 verify 和准确新版本本地双来源真实 TLS 验收。
任何新 UNKNOWN 立即停写，不自动恢复、重试、清锁、删除、禁用或退役旧服务。

### EP-SBA-16：已发布 Pages 有限子阶段诊断（历史范围）

该阶段用户仅批准补充、隔离验证并发布诊断源码，没有提交生产 repair/deploy/verify、移交
writer、上传资产、部署 Pages 或删除旧服务。1.0.6 已正常发布源码，但不是线上版本。
既有 repair 的 `fromErrorCodes`、秘密清单和准确父任务绑定保持不变，不扩大恢复权限。

诊断覆盖构建、项目读取/创建/配置、归属核对、资产 token/查询/上传/验证/登记、部署提交/
读取/列表/等待/读回、公开版本和最终 writer 核对共二十个固定子阶段。provider HTTP 错误仅
保留固定分类：3xx、400/401/403/404/409/413/429、其他 4xx/5xx/其他状态，以及 SIZE、
DECODE、ENVELOPE、TIMEOUT、NETWORK、REQUEST；不保存正文、headers、token 或异常原文。
例如 `EASYPROXY_SBA_PAGES_ASSET_UPLOAD_HTTP_403_UNKNOWN`。请求顺序、写入归属检查、
无重试、无重定向、成功校验与 UNKNOWN 保锁停写语义不变。未知值拒绝或回退通用安全码。

只读证据 `generation-pages-diag-115.safe.json`、`generation-pages-offline-116.safe.json`
和 `generation-pages-queries-117.safe.json` 未找回原资产请求的 HTTP 细节：当前 token 与
check-missing 解析通过不等于原失败已复现；历史 99 个 hash 也不是 1.0.5 重构产物证据。
audit 仅能看到项目/Worker 等成功请求，不能证明资产 JWT 接口原失败的具体原因。
115 的原对象保持布尔值误用了 control 文件名；116 独立逐摘要核实原九阶段对象保持，
不覆盖原证据。新诊断只能改善未来获批执行的回执，不能补回原失败未保留的信息。

本轮验证与发布结果完成后另行记录，不能预先宣称测试通过或生产恢复成功。

## 以下为分阶段历史实施记录

下面各阶段的“尚未”描述是当时停点；以顶部当前状态和生命周期文档为准。

### EP-SBA-15：发布前生成 UNKNOWN 的只读核对和有限诊断（负责人：主 AI）

1.0.4 任务 `dc-a541706c25ec4b25a446013a86acab17` / run `40311072936` 的真实应用回执为
`EASYPROXY_SBA_GENERATION_UNKNOWN`，不是 Actions 成功即应用成功。发生后已停止云写入，
未独立 verify、未做本地消费验收，也没有删除旧服务。完整只读前置 `assets-readonly-preflight-70.safe.json`
证明 control/ETag、十个原对象、D1、GitHub、1.0.3 Worker 与无部署的 Pages 均保持；
该 generation 在恢复移交和发布之前执行，不能误称 Pages 已发布成功。

准确 main `c1dee04af9b0157551f316586d1a9a881e3f13ac` 在无云写凭据的本地隔离容器生成五份
必需产物并校验通过；同源码的既有无部署凭据 hosted 预检 run `40312826379` 则在 generation
阶段失败。两份证据仅确定存在环境相关的生成失败，尚不能确定采集源、网络或产物的具体根因。
不以本地成功覆盖原 UNKNOWN，不擅自更换来源、降低校验或自动重试。

PR #20 已发布有限诊断改进：生成来源、runtime、配置、采集、读回、产物、转换、校验
使用有限的固定阶段码；只读预检可记录目标源码抛出的有限安全诊断常量，拒绝任意异常正文、
伪装错误类型或不在白名单的字段。旧目标无需新增 diagnostics 依赖，不回退借用 driver 实现。
诊断本身不改变采集、输出验证、调用次数或失败停写语义。

只读 hosted run `40317489160` 将原准确 1.0.4 的失败缩小到 `subs/clash.yaml` 的
YAML/节点结构校验；后续同源码只读 run `40318129093` 全部通过，公开采集结果并非固定输入。
禁网合成试验使用批准 SHA 对应的真实 subconverter，证明纯数字密码会输出 `!<str> 123456`，
现有 PyYAML `SafeLoader` 拒绝该合法转换器标签并复现 `ARTIFACT_FORMAT`。该兼容缺陷已证实，
但旧 hosted 失败没有导出具体标签，不能声称已直接证明它恰好由这一标签触发。

1.0.5 使用独立 `ClashSafeLoader`，仅为准确 `str` 标签构造标量字符串；不全局注册构造器，
不接受其他标签、Python 对象标签或序列/映射型 `str`，保留原产物字节与全部其他校验。
新增操作 `continue-generation-initial-deployment` 仅绑定准确 1.0.4 UNKNOWN 的 task/run/
请求/结果摘要，并另核原 1.0.3 Pages 父任务终态。`previous` 为 1.0.4，而现场 writer、Worker/
Pages 核对和 `PagesResume` 仍严格绑定真实 1.0.3。复用原十对象/所有资源的只读漂移检查，
先 create-only 保存含最新失败与原祖先的移交证据，再单次 CAS 移交非空 writer；不删除、不
重放 D1/GitHub，保留所有 UNKNOWN。任何新 UNKNOWN 均停止云写入，不自动再次恢复。
用户批准后已执行该准确恢复任务，最终在 Pages 返回 UNKNOWN；见顶部最新状态，1.0.5 未完成上线。

### EP-SBA-14：Pages 资产 API 合同与新准确任务恢复（负责人：主 AI）

1.0.3 的单次恢复 `dc-3dad4093eba94806b436848210a958fb` / run `40288989518`
在 Pages 阶段再次 UNKNOWN 后停止。只读查询确定 `check-missing` 成功响应省略 `errors`；
99 个公开资产 hash 均未命中，项目无 deployment，control 和十个原对象保持。
Cloudflare 官方 OpenAPI 另明确 upload/upsert 成功 ACK 不要求 `result`。
1.0.4 仅为三个准确的资产 POST 端点接受省略 `errors`，仅两个写入 ACK 可省略 `result`；
显式错误、缺少成功标志、查询缺少结果、错误 hash 列表和其他 provider 响应仍拒绝。
不改变无重试、无重定向和 UNKNOWN 停写语义。

用户已另行批准新操作 `continue-pages-assets-deployment`，仅绑定上述 1.0.3 父任务、
run、准确请求和结果摘要。恢复前核新 control 字节、十个原对象及全部正文、D1 schema/history/
四表零行、完整 GitHub、1.0.3 Worker 元数据/公开版本/同业务模块摘要、准确 Pages ID/配置/
零部署；任何漂移拒绝写入。先 create-only 移交证据，再单次 CAS 移交非空 writer，保留所有
历史阶段与 UNKNOWN，不重放 D1 或 GitHub，不删除资源。Worker 只更新同 bundle 的来源
元数据；Pages 复用项目，更新版本、首次上传资产和发布；随后完整审计、同步和独立读回。
再次 UNKNOWN 立即停止。该入口尚未正式执行，不能称应用已上线。

资产 API 四文件窄修的隔离 Linux 全量 `assets-native-54.xml` 为 **805 passed、0 skip**。
此前 native52 的单项失败已证实是 Windows bind mount 丢失测试 helper 执行权限；仅在隔离
容器复制、核摘要并 chmod 后复验，不修改产品或宿主二进制。新恢复初轮定向验证
`assets-repair-55.xml` 为 195 passed。1.0.4 完整冻结源码的 `assets-recovery-native-57.xml`
为 **868 passed、0 skip**，CLI Go 82 项、0 skip 与 go vet 通过，并按新源码重建真实 sealer。
native56 因隔离网络下空 Go cache 无法下载依赖而失败；native57 显式复用固定测试环境内的
已有依赖缓存、禁用下载后复验，不改产品合同。SpringBok 实际 parser 接受四个 repair，
冻结源码 Gitleaks 未发现泄漏；`assets-readonly-preflight-58.safe.json` 七项真实只读前置通过。
准确 PR/main CI、正式单次恢复和最终本地业务验收仍待完成。

### EP-SBA-13：Pages 空变量响应与准确部分发布恢复（负责人：主 AI）

1.0.2 的单次正式恢复 `dc-c982bc121b8548d78474848a8065b296` / run `40280908267`
已补齐 GitHub 配置、发布 Worker，并创建 Pages 项目，随后返回 `EASYPROXY_SBA_PAGES_UNKNOWN`。
只读核对证明 Pages 尚无任何 deployment、生产配置正确；provider 将空 preview `env_vars`
返回为 `null`。官方 Cloudflare OpenAPI 将该字段声明为 nullable，真实响应离线回放在同点复现。
窄修只在预期变量表为空时接受 `null`；错误类型、额外绑定和生产必需变量仍严格拒绝。

用户已另行明确批准此准确 Pages UNKNOWN 的一次受限恢复，不复用已消费的 GitHub 修复批准。
独立操作 `continue-pages-initial-deployment` 固定该父 task/run、版本、请求和结果摘要。
写入前核终态、准确 control 字节、七个原对象及全部阶段正文、D1 schema/history/零业务行、
完整 GitHub 环境/策略/变量/secret 名单/registry、Worker 设置/公开版本/真实模块摘要，以及
准确 Pages project ID/配置/无部署。任一漂移即停止；全桶只读清单不接受额外或重复对象。

全部检查通过后先 create-only 保存新移交证据，再单次 CAS 移交仍非空 writer；旧 Permit
失效，两个历史 operation 的阶段和所有 UNKNOWN 回执保留。D1 和 GitHub 仅再核验、不重写。
为统一新版本的来源证明，Worker 只发布一次相同业务 bundle 的新版本元数据，不重设子域；
Pages 复用原项目、单次更新新版本配置、上传资产并首次发布，不重建项目、不删除任何资源。
随后仍须完整候选审计、promotion、受限 MiSub 同步和独立公开读回，成功后才提交版本。
任何 marker/CAS/后续响应不确定均停止，不自动重试、清锁、删除或回滚。

本机定向测试已通过：`pages-repair-38.xml` 117 项、`pages-protocol-41.xml` 53 项；
其中包含真实 HTTP multipart、真实既有 Functions 产物和全部关键拒绝反例，不连接真实云账号。
产品只读前置 `pages-readonly-preflight-39.safe.json` 七项全部通过，未创建 marker、移交 writer
或执行云写入。冻结 index 的全量原生 Linux 回归 `linux-native-pages-recovery-42.xml`
为 **780 passed、0 skip**；CLI Go 82 项、0 skip 与 go vet 通过，真实 sealer、CLI 和迁移
materializer 已重建。真实 SpringBok parser 接受 1.0.3 的三个独立 repairs，scoped Gitleaks
未发现泄漏。准确 CI、正式单次恢复及独立业务验收仍待完成，不能据此声称线上恢复成功。

### EP-SBA-12：GitHub 创建响应与准确中断点定向恢复（负责人：主 AI）

1.0.1 的真实 repair 在 GitHub 阶段返回 `EASYPROXY_SBA_GITHUB_UNKNOWN`。只读核实 D1
已完成两份审核迁移且业务表全空；GitHub 已创建实例 environment、main 策略及 CONFIG
变量，尚无 OWNER、secrets 或刷新索引；Worker/Pages 尚不存在。原 writer 和两个阶段
标记保留。它已不满足 pristine 条件，不重发原任务，也不扩展原 pristine repair。

已联网核 GitHub 官方 REST OpenAPI：变量及 secret 的创建响应 201 为 JSON empty-object。
原适配器只接受零字节正文，在第一个变量实际创建后误报 UNKNOWN；本机真实 HTTP 回归
在同一调用点复现。1.0.2 仅允许该创建响应为严格 `{}` 或兼容原空正文；其他正文仍拒绝，
204 仍只接受无正文，不改变重试、重定向或响应丢失语义。

独立操作 `continue-github-initial-deployment` 仅绑定上述准确 1.0.1 父任务/run、请求和结果
摘要。执行时重核父 run 已结束、仅一次尝试且 executor/title 一致，核原 control 的准确
字节摘要、仅三个既有 R2 对象及两个原阶段正文、D1 完整 schema/history/零业务行、
Worker/Pages 缺失、准确 GitHub environment ID/policy/仅 CONFIG/无 secrets/无索引。
任何漂移或不确定读取都在云写入前停止；不支持其他部分部署或其他 UNKNOWN。

用户已明确批准这一次定向恢复。全部检查通过后先 create-only 记录移交证据，再单次 CAS
将非空 writer 原子移交到新候选，始终不解锁；旧 Permit 立即失效，旧阶段和 UNKNOWN
回执保持。D1 只读复核，不重发 DDL；GitHub 不重建环境/策略/CONFIG，只补 OWNER、
原声明 secrets 与索引，然后继续原发布/审计/同步/独立读回链。只有完整成功才提交版本。
marker 或 CAS 响应丢失、新阶段失败均停止，不自动再次移交、删除、回滚或 retry。
当前冻结 index 的原生 Linux 回归 `linux-native-recovery-24.xml` 为 **701 passed、0 skip**；
CLI Go 82 项、0 skip 与 go vet 通过，真实 sealer、CLI 和迁移 materializer 均重新构建。
先前 native20 的三个失败是 harness 缺 curl，补齐环境后使用相同冻结源码复验；native22
没有完成回执，不计为通过。原失败/中断证据保留，未跳过 TLS 校验或修改产品保护。
真实 SpringBok manifest parser 接受 1.0.2 的两个独立 repairs；上线前只读核对
`recovery-readonly-23.safe.json` 全部通过，无 marker、writer 移交或云写入。
当前仍待精确 CI 和正式执行；后续仍欠独立 verify、本地消费和旧资源退役批准。

### EP-SBA-10：无写凭据 hosted 预检（负责人：主 AI）

原任务只保留通用异常回执，runner 私有日志不能恢复具体原因。新增手动工作流
`SBA Read-only Preflight`，输入准确 40 位源码 SHA，以诊断入口所在提交为 driver，
独立检出目标根与准确 gitlinks、安装其原锁文件及私有 Python，真实下载固定工具并构建
运行时、审计镜像、sealer 和公开采集产物。目标库函数从该准确 checkout 加载，不以
driver 版本替换；两个可选私有种子固定停用。仅保留固定字段/固定阶段错误的公开报告，
不上传私有日志、订阅正文或任何凭据。不调用部署、R2 writer、provider 客户端或原任务。

此入口只排查准备阶段，不验证业务秘密、首次云写入、线上资源、部署成功或独立 verify。
本地固定源码预备和四个官方工具下载已分别通过。GitHub hosted run `40267227117` 对原准确
源码 `1fc843033ba5f39384772d1dccb4ccd90deda03d` 的七项准备检查全部通过，无云写入。
这不能恢复或确定原 UNKNOWN 的具体原因，也不授权重新执行原任务。

### EP-SBA-11：严格 pristine 首次部署修复与脱敏阶段错误（负责人：主 AI）

1.0.1 候选只声明 `pristine-initial-deployment`：绑定原 1.0.0 的准确源码、通用 UNKNOWN
错误码和平台提供的父任务/run/请求与结果摘要，沿用同实例、同资源和原秘密 profile。
修复前只读核验整个 R2 bucket 为空且无控制记录、D1 身份一致且业务 schema 为空、Worker
和 Pages 不存在、GitHub 仓库可访问且具有管理权但实例 environment 不存在。任何既有对象、
部分发布、读失败或不确定响应都停止，不清锁、不接管、不回滚或重放原 writer。

全部前置通过后才在原资源创建新候选，重新执行完整发布及业务检查；只在成功后返回
`repair-completed`、`data-preserved`、`unchanged-resources-verified`、`service-ready`。
外层回执仍为 repair，原任务的 UNKNOWN 回执不变。该动作不处理修复中途再次失败的部分发布。
当前只读核验成立不保证未来仍 pristine，正式 repair 必须现场重新检查。

新入口将异常转换为固定白名单阶段的 `EASYPROXY_SBA_<STAGE>_UNKNOWN`，不输出异常正文、
响应、秘密或配置；未知阶段仍使用通用错误码。错误状态、create-only 回执、持锁和不重试语义
保持不变。此版本仍是待验证/发布的候选，不能据此声称已经修复或上线。

当前候选冻结 index 的原生 Linux 回归为 633 passed、无 skip，含 repair/阶段错误/原回执
不可覆盖和真实 Worker 404 错误码负例；CLI Go 82 项与 go vet 通过，真实 sealer/CLI/迁移
准备完成。SpringBok 的实际 manifest parser 接受该 1.0.1 合同。证据见 `linux-native-repair-10.xml`；
旧 555/247 回归仍只属于历史版本。尚待准确 PR/main CI、控制面发布和正式 repair/verify/本地消费。

### EP-SBA-08：Linux 应用工具链与审计运行时（负责人：主 AI）

SpringBok SUITE-03 已合并并有正式控制面/Linux pin 发布回执。应用已补齐 Ubuntu 24.04/x64
的准确 Node 22.23.3、npm 10.9.7、esbuild 0.28.2、Go 1.27.3，以及 CPython 3.12 的 28 个
带哈希 wheel 和私有 venv bootstrap。真实 ECH、Pages/Functions、CLI 和生产 tags 审计运行时
均已构建；固定 image ID 的真实订阅消费/租约/探测/容器清理在本机完成。
详见 [固定工具链与实测边界](toolchain.md)。这不等于五动作或线上应用验收。

### EP-SBA-09A：首次 D1 与公开配置映射（负责人：主 AI）

`d1_init.py` 在共同许可内拒绝非空业务库，只执行两份摘要固定的迁移，逐条单次写入后核对
完整 schema、迁移记录和零业务行；不借用有迁移/Cron 副作用的 Pages 路由触发初始化。
`suite_config.py` 将严格公开配置映射为同一套件资源摘要、规范化仓库和实例专属名称，拒绝
指向旧手动资源、其他仓库或其他实例的目标。它们尚未由正式五动作入口串接。

### EP-SBA-01：固定源码准备（负责人：主 AI）

sources.py 读取批准根提交中的 .gitmodules 和三个 gitlink，在独立、全新目录检出各自
准确 SHA。拒绝脏根 checkout、浮动引用、仓库来源漂移、未知子模块、URL 凭据及目标目录
复用；可从来源身份匹配的已有本地对象库只读取对象，不复制其未提交文件或修改其 HEAD。
所有部署代码、规则与接入测试由 .sba 单独拥有，不修改独立 upstream 仓库。

materialize_workspace 进一步将批准根与三个固定 gitlink 准备在同一个全新目录，现有根脚本
可通过 upstreams/... 直接读取源码；不移动或复制开发工作树，也不将 dirty cache 带入构建。

已补充 materialize_root：周期调度入口也从批准根对象准备独立 checkout，而不是直接运行
默认分支的新提交。根/子模块缓存即使有不同 HEAD 或未提交文件，也只提供准确 SHA 的
Git 对象。Git 固定 core.autocrlf=false，准备与脏状态检查使用同一套隔离环境。

### EP-SBA-02：共同持久写入栅栏（负责人：主 AI）

coordination.py 实现同一私有 R2 bucket 中的条件写入记录，绑定来源仓库、环境、账号、
bucket、正式/预升级用途与批准资源清单摘要。首次只接管空 bucket，不导入旧部署。
deploy/update/refresh/preview/destroy-preview 共用一个不可过期的 writer；只有动作所需
业务检查全部通过，才能提交版本并释放。没有自动过期、夺锁、finally 解锁或写入重试。
API 响应丢失、旧句柄、重启、版本/资源漂移均失败关闭。详见 [实施边界](integration-notes.md)。

这只是已实现的共同入口，还未接到真实的五个生命周期动作和定时工作流；**旧生产发布
工作流未使用此锁**。不能将底层栅栏测试通过表述为现有生产竞态已消除。

### EP-SBA-03：复用 ECH Worker 的私有 R2 发布出口（负责人：主 AI）

edge.mjs 组合原 ECH Worker 和只读产物路由，不修改原 ECH 发布入口、不增加第四个常驻
服务。新套件通过 /aggregator/ 提供与现有发布合同一致的有限产物路径，R2 本身可保持
私有；控制记录、加密备份和 crawledproxies 不公开。独立版本元数据接口不返回环境秘密。
只读流、HEAD、拒绝写入和 ECH 原鉴权/升级均已在真实本地 workerd 验证，未上线。

```powershell
$env:PYTHONDONTWRITEBYTECODE = '1'
python -m pytest -q .sba/tests -o cache_dir=C:/Users/Public/nas_home/AI/GameEditor/linshi/easyproxy-sba-cache --basetemp C:/Users/Public/nas_home/AI/GameEditor/linshi/easyproxy-sba-tests
node --test .sba/tests/aggregator-assets.test.mjs
```

完整 Python 回归须显式设置 `EASYPROXY_TEST_ESBUILD` 为当前受支持平台、摘要匹配的
x64 esbuild 0.28.2 绝对路径，以及 `EASYPROXY_TEST_PAGES_BUILD` 的真实产物目录；否则
对应真实构建/产物测试会明确 skip，不能报告为完整通过。进程组测试只在真实 Linux 运行。

真实 workerd 回归需要显式指定已安装 Miniflare 的 dist/src/index.js 绝对路径
（通过 EASYPROXY_TEST_MINIFLARE）和 linshi 下的产物目录（EASYPROXY_TEST_ARTIFACT_ROOT），
再运行 node --test .sba/tests/edge.workerd.test.mjs。测试复用该运行时可解析的 esbuild，
未提供路径时会明确 skip，不应统计为已通过。仅接受测试适配器列出的准确 v4/v5 版本，
未知版本失败，不降级；产品本身不依赖 Miniflare。

### EP-SBA-04：受保护的真实迁移阶段（负责人：主 AI，尚非整套动作）

migration.py 已将持有的 update 许可接到真实 easyproxyctl cloud backup / cloud migrate。
先按现有 age 格式备份，创建本次 operation 的私有 R2 对象并完整回读；CLI 在首次 D1 写前
校验该持久备份的 SHA256、源身份和当前完整数据基线，随后只执行两份已审核的固定迁移。
迁移写入使用固定 Cloudflare HTTPS API、不可重发请求正文和单次发送，不调用可能内部
重试的 Wrangler 写命令。既有业务行、用户自定义表及 DDL 均参与保持检查。

这一阶段仅返回 backup-created、data-preserved、migration-verified；**不提交版本、不释放
writer，不产生 suite-published/service-ready 或 SBA 成功回执**。Pages/Worker 发布、Aggregator
任务、verify/preview/destroy-preview 仍待串接。详情与命令见 [迁移合同](migration.md)。

### EP-SBA-05：ECH Worker 真实构建与发布阶段（负责人：主 AI）

ech.py 使用批准根 SHA 的干净源码、固定摘要的真实 esbuild，生成原 ECH 与私有 R2 出口的
组合 ESM；在同一套件许可和阶段级单次尝试标记下，经 Cloudflare multipart API 上传，
回读资源归属、绑定、子域开关和公开版本。首次发布拒绝覆盖已有同名 Worker。

worker_api.py 禁止写入重试、重定向及环境凭据链；响应未知不自动回滚或释放许可。已接
实际发布协议，但当前只进行本机合成 HTTP 与 workerd 验证，没有真实云端发布。它只返回
ECH 阶段证据，不声明套件可用；详情见 [ECH 发布合同](ech.md)。

## 已核实的平台前置

SpringBok 新增显式 cloudflare-suite：Pages、Workers、D1/R2、仓库级 Actions 目标归属，
以及按 owner/仓库/应用绑定的业务秘密保险库。新套件选择 manifest v3、deployment v2 的
Ubuntu 24.04 / PowerShell 7.6.9 / Python 3.12 / Node 22 完整 Linux tuple；原 Windows tuple
仍兼容，但本套件不假设 Windows runner 能运行 Linux Docker。
2026-10-26 前序独立回执确认 SUITE-03 正式代码/Linux pin 为
`4d6a8bcd8c52f0ada791729bafc2788a2cffb972`，受保护 tag 与 workflow ID `396352142`
已建立，原 Windows pin、bindings、运行配置和 Access 保持。证据在
`linshi/springbok-suite03-20261026/`；平台前置不代表 EasyProxy 已经部署。

## 后续必须完成，不能省略

- deploy：创建并发布 MiSub Pages/Functions、ECH Worker，关联 D1、Aggregator R2 和实例
  Actions 环境；提供本地 EasyProxy 的接入信息，不把秘密混入公开配置或回执。
- update：同实例、同资源，先留存可恢复加密备份，再做 expand-only 迁移和保留数据校验。
- verify：固定当前已部署版本的独立业务验证，不申请部署写凭据。
- preview：独立 Pages/Worker/存储/Actions 环境，源数据副本及副作用隔离；必须证明源不变。
- destroy-preview：只处理批准清单中的测试资源，不碰正式数据、共享工作流文件或仓库。
- Aggregator 周期刷新与整套升级分开；两者须有同一资源的单写入者栅栏，不能用创建第二个
  workflow 的方式绕过当前 root 发布权威。默认分支 schedule 必须加载实例批准 SHA。

已有 easyproxyctl 身份核验、age 备份、完整数据保持检查已接到 update 的持有许可；实际
ECH、Pages/Functions、Aggregator 候选/审计/晋级与受限管理同步阶段也已实现，不能把它们
当成整套升级。正式编排、GitHub 实例环境和固定 SHA 调度仍未接通；SBA 路径不接旧脚本
内部的写重试或自动回滚。预升级销毁也尚未实现：栅栏的
retired 标记只是禁止再次使用，并不等于 Pages/Worker/D1/R2/Actions 清理已经完成。

用户已授权最终真实部署与旧服务迁移。本轮只读清点并保护旧资源、复用现有凭据文件；
准确删除清单须在新服务验收与备份完成后列明，并等待用户明确“同意”，不可提前清理。

### EP-SBA-06：MiSub Pages / Functions 实际发布阶段（负责人：主 AI）

pages.py / pages_build.py / pages_api.py 已补充准确 gitlink 源码的隔离 npm/Vite 构建、官方
Wrangler 本地 Functions 编译与 Direct Upload API 发布；不修改独立 upstream，也不调用
Wrangler 远程发布。项目与业务秘密/D1 绑定、阶段级单次标记、API 终态、canonical 归属和
公开版本均参与检查。与 ECH 共用禁止重试、重定向和环境凭据链的 HTTPS 传输。

本轮真实 MiSub 构建生成 99 个静态资产及 Functions bundle，并在本地 workerd 验证原前端、
登录/签名 Cookie、D1 与新增只读版本路由。HTTP 发布只在本机合成 provider 中测试，**没有
真实云端写入**。仅返回 Pages 阶段证据，不提交版本、不解锁、不宣称整套可用。
详见 [Pages 发布合同](pages.md)。Aggregator、独立 verify 和完整 preview/销毁仍未接通，
因此继续不提供可部署 manifest/deployment 或空壳成功入口。

### EP-SBA-07A：Aggregator 本地输出与受保护发布阶段（负责人：主 AI）

新增隔离本地输出适配、与候选摘要/operation/gitlink 绑定的实际审计调用，以及禁止重试
或自动恢复的 R2 candidate/immutable/LKG/stable 发布。原 upstream 不改，私密 raw crawl
不公开，阶段从不自行提交或释放整套许可。详见 [Aggregator 阶段与剩余边界](aggregator.md)。
已真实验证原 PushToLocal 和本机 S3 传输，但尚非完整联网采集/Actions/MiSub 刷新验收。

### EP-SBA-07B：MiSub 受限管理同步（负责人：主 AI）

misub_sync.py 在同一套件 Permit 内使用单次持久阶段标记，将通过真实消费审计的管理节点
和唯一 aggregator-global profile 同步到实例 Pages。新增 app-owned 管理接口复用原签名
Cookie，仅允许数字后缀的两类管理 ID；直接调用原 D1 按 ID adapter，不调用会重探测全部
订阅、通知或刷新缓存的原保存 handler，也不访问有迁移/Cron 副作用的 /api/data。

有界流式请求、准确 sourceSha、同 Origin、完整未管理行和 profile 保持均参与验证。多行
D1 写入不是原子 CAS；途中失败报告 UNKNOWN 并保留 writer，不重放、不回滚。实际批准
gitlink 的四个适配文件隔离构建已在 workerd 验证两次同步、鉴权/作用域负例和无外部请求；
这不是线上同步或整套部署验收。详见 [Aggregator 阶段与剩余边界](aggregator.md)。
