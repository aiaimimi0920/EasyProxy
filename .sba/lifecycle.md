# EasyProxy 标准套件生命周期与调度

## 当前状态（2026-10-27）

1.0.10 已完成正常 SpringBok 恢复、独立 verify 和本地双来源真实 TLS 业务验收：
repair `dc-9e968056fd18416a915a2f220b1bf81e`，verify
`dc-9f85e79bf52a4570b0cdc313ba93159d`。旧失败回执仍保留；历史 UNKNOWN 不改写。

1.0.11 已发布，旧服务已按批准清单备份、冻结并清理；SpringBok 全新 deploy 任务
`dc-92bbbbc744784ec6adf13a37ddd68f97` 在生成输出阶段 UNKNOWN，尚未上线。
新 R2 为空、无 control/writer，Worker/Pages/GitHub environment 尚不存在。
1.0.12 候选只修生成职责：候选阶段明确跳过旧 Clash 连通性预筛，保留格式与 TLS 检查；
真正可发布性仍由准确 EasyProxy 镜像的消费审计、有效节点数量和全部晋级门禁决定。
生成修复本身不授权重放生产任务、清锁或通用接管。只有整条流程和真实代理测试通过才删除本任务
备份副本；源码、凭据原件和审计保留。

新建的自定义域名必须归属同一活跃 zone/account，DNS 与 Worker/Pages association 均为空；
部署单次创建绑定和必要 DNS，成功读回 domain/zone/DNS ID 并保存证据；未知回执不重放。
update 仅核既有绑定，不修改或接管域名。传播等待有界且只发 GET。
公开地址可自定义，但自动管理密码、Cookie 和 manifest token 始终只发往准确项目的
`pages.dev`；升级备份同样固定管理地址，不因公开域名漂移向其他接收端发送秘密。
隔离 preview 继续使用 provider origins，可从自定义公开地址的正式源复制；自定义 preview
在云操作之前拒绝，destroy-preview 不用于正式套件退役或删除自定义域名。

云端是一套部署：MiSub Pages/Functions、ECH Worker 与 GitHub Actions Aggregator；
D1 和私有 R2 是该套件的数据资源。`/aggregator/` 复用 ECH Worker，不增加常驻服务。
多个本地 EasyProxy 可以连接同一套云端。

## 正式入口与资源合同

SpringBok 调用 `springbok.ps1 -RequestPath ... -ResultPath ...`。stdlib bootstrap 从批准
根 SHA 和三个准确 gitlink 创建源码外独立工作区，再安装带哈希的 Python wheel。
Node 22.23.3、npm 10.9.7、esbuild 0.28.2、Go 1.27.3 与审计镜像来源均有固定校验。
runtime 为 Ubuntu 24.04 / PowerShell 7.6.9 / Python 3.12 / Node 22。

公开配置恰有 11 键：accountId、repository、pagesName、workerName、workersSubdomain、
databaseId、databaseName、bucketName、refreshTarget、pagesOrigin、workerOrigin。
名称由 `ep-{instance}-...` 模板产生；不允许改成旧手动资源名或其他实例资源。
平台清单含 D1、R2、Worker、Pages、Actions environment 和两个公开 origin，共七项。
应用内部 provider-only inventory 仍为原五项；自定义配置才追加对应服务的 domain 行，
因此旧配置的 digest 不变，新配置的 digest 绑定准确公开 origin。
秘密只引用 manifest 的 11 个名称；不得放进公开配置、日志或回执。

所有动作业务预算为 3300 秒，父进程另留 120 秒子进程清理，平台上限 3600 秒。
结果是 create-only、UTF-8 无 BOM 的协议文件，不以 stdout 或进程退出 0 代替业务回执。
发生写入结果不明时报告 UNKNOWN，不自动重放、回滚或清锁。
新版本只记录固定白名单阶段错误码，不把内部异常或 provider 响应正文放进回执。
PR #20 将 generation 进一步分为来源、runtime、配置、采集、读回、产物、转换和校验；
诊断不是对原失败根因或恢复成功的证明。只读预检仅允许目标代码中
准确错误类型与有限常量共同匹配的诊断码，旧目标不需要加载 driver 的新 diagnostics 模块。

1.0.5 增加仅支持批准转换器 `!<str>` 标量的独立安全 YAML loader；不全局修改 PyYAML、
不接受任意标签或非标量 `str`，不改变产物。禁网真实转换器的纯数字密码已复现旧校验失败；
旧 hosted 失败只直接定位到 Clash YAML，而另一同源码采集已通过，不能扩大根因结论。
新准确操作 `continue-generation-initial-deployment` 绑定 1.0.4 task
`dc-a541706c25ec4b25a446013a86acab17` 的原请求/结果摘要与终态，并另核原 Pages 父任务。
该准确恢复执行时先核原 1.0.3 资源和完整十对象，再单次 marker 后 CAS 移交，不重放
D1/GitHub、不清锁、不删除，保留 UNKNOWN 与阶段证据；最终 Pages 再次 UNKNOWN 后停写。
用户对该次受限恢复的批准已消费，不得用它重试或移交当前 1.0.5 writer。

1.0.6 仅补充二十个固定 Pages 子阶段及有限 HTTP/响应分类，回执不含正文、headers、
token 或异常原文。请求顺序、次数、归属检查、成功判断及无重试语义保持，原 repair 的
错误码匹配与准确父任务绑定不扩大。该阶段授权仅为诊断源码发布和隔离验证，不执行生产
repair/deploy/verify、writer 移交、资产上传、Pages 部署或旧服务删除；不是新的恢复许可。
当前只读查询和 audit 未保存原失败资产请求的 HTTP 细节，根因仍未证实；诊断不能补回
历史缺失信息，也不能把本地故障注入的分类成功当成线上修复成功。

1.0.7 新增单独绑定准确 1.0.5 task/run/requestDigest/resultDigest 的
`continue-pages-diagnostic-deployment`；用户已另行明确批准实现、发布及单次执行，批准
不用于循环恢复。旧五入口和十一 secret 保持；准确父版本白名单只新增审核的 1.0.5。
先只读核全部祖先终态、control SHA/ETag、十三对象及十二历史正文和所有已有资源，
再 create-only 移交证据、单次非空 writer CAS；D1 初始化和 GitHub 配置不得重放。
Worker 同业务 bundle 仅更新版本来源、无子域重设；Pages 复用项目，以新候选 hash 查询
并仅上传缺失资产。成功后才继续原审计/晋级/受限业务同步和读回；同步可能写业务数据。
新 UNKNOWN 停写且批准消费，不重试、不清锁、不自动恢复。全部成功后另行独立 verify
与本地真实双来源 TLS 验收；旧服务/工作流不删除或禁用，退役须另列准确清单获批准。
该1.0.7批准已消费，准确失败如顶部所述；不能复用旧批准或声称Pages发布成功。

1.0.8已发布有限生成输出诊断和hash-bound单订阅任务异常隔离补丁；前者唯一无部署凭据
hosted预检通过，后者已禁网复现并修复真实进程池整批丢结果机制。不能据此唯一倒推历史
生产失败根因。1.0.9的新入口`continue-generation-outputs-deployment`已另获方案203
明确批准，严格绑定最新1.0.7原始UNKNOWN回执；previous1.0.7与资源父版本1.0.5分离。
旧六入口和十一秘密保持。实现还核固定原ZIP摘要和严格envelope/result，签名下载无Bearer、
不重定向；全部原始回执/祖先/资源/control/十三对象一致后才先证据、单次非空writer CAS。
不重放D1初始化/GitHub配置，后续发布仍遵守同bundle、原Pages项目、最多单次deployment。
完整冻结和正常PR/main CI后仅一次无部署凭据hosted预检；成功且现场重新核对无漂移后，
才正常UI单次生产恢复。任何新UNKNOWN即消费本次批准并停写，不自动重试或再次恢复。
成功后独立verify和本地真实消费；旧服务/工作流/密钥保留，不删除、禁用或退役。
当前为1.0.9源码实施阶段，尚未发布或提交恢复，不预报上线成功。

## 五个生命周期动作

- **deploy**：只接受独立新资源；在同一持久 Permit 下初始化空 D1、配置 GitHub 实例
  environment、发布 ECH/Pages，生成 Aggregator candidate，经真实 EasyProxy 消费审计后
  晋级并受限同步 MiSub；独立公开读回全部通过才提交版本。
- **update**：复用同实例资源，先生成 age 备份，存私有 R2 并完整回读；真实 CLI 绑定
  备份摘要执行审核迁移，核完整既有数据保持，再运行发布链。普通更新不轮换业务秘密。
- **verify**：先返回只读路径，不读取占位秘密，不创建 writer，不构建部署工具；验证公开
  版本、不可变/稳定产物正文摘要、订阅结构和前端。它不证明外网代理出口端到端可用。
- **preview**：复制源 D1 的真实数据到独立资源，演练审核迁移；创建前后及完成前比较源
  数据摘要、版本、身份。源不冻结，正常并发写入可能使该次演练无法证明 source-unchanged。
- **destroy-preview**：核私有 provenance 与批准七项清单；全部删除前置检查先完成，再按
  入口优先、数据随后顺序单次删除并读回。只删该 environment，不删共享 workflow/仓库。

### 可选 repair：只处理原 1.0.0 的全资源 pristine 首次部署

`pristine-initial-deployment` 仅接受原准确 SHA
`1fc843033ba5f39384772d1dccb4ccd90deda03d`、版本 1.0.0 和
`EASYPROXY_SBA_FAILED_OR_UNKNOWN`，并校验平台绑定的父任务、run 和请求/结果摘要。
它不是 retry/update：只读确认 R2 全空且无控制记录、D1 身份及空 schema、Worker/Pages
不存在、GitHub 仓库管理权及不存在的实例 environment 后，才在相同资源创建新候选。
任何部分写入、不可访问或格式不确定均拒绝；不删除、解锁或接管旧 writer。

完整部署成功才返回 `repair-completed`、`data-preserved`、`unchanged-resources-verified`、
`service-ready`；外层 action 为 repair，原 UNKNOWN 回执不变。全部 provider 发布仍有自身
归属/单次写入检查；外部竞态或新故障仍可能 UNKNOWN，不能再次自动 repair 或换环境绕过。
修复完成后仍须固定修复 SHA/version/config 的独立无写凭据 verify，再做本地真实消费验收。

### 独立 repair：准确 1.0.11 新实例空现场继续

`continue-fresh-pristine-deployment` 只接受 `dc-92bbbbc744784ec6adf13a37ddd68f97` /
run `40468362650` 的准确 deploy UNKNOWN；previous 为准确 1.0.11，原结果 checks 为空。
完整公开配置摘要绑定 `easyproxy-main`、同一预创建 D1/R2 及批准的两个 `yamiyu.com`
origin。八个历史入口不变，不能把其他生成失败或另一实例路由到本入口。

正常源码发布及无部署凭据预检后，经正常 UI 单次继续；构建前与首次写入前均只读核原
executor 终态、固定 ZIP 摘要和严格回执，D1 空 schema、R2 全空/无 control，Worker、Pages、
environment 不存在，registry 为空，workflow active，zone 活跃且 DNS/association 空闲。
任一漂移拒绝。没有非空 writer，不执行移交、清锁、CAS 或资源删除/重建；随后仅首次
初始化与完整发布/真实审计。原 UNKNOWN 保留；再次 UNKNOWN 停写，不自动再次继续。
成功后仍须独立 verify 与真实双来源 TLS 消费，不能以 executor 成功代替业务通过。

### 独立 repair：准确 GitHub 创建响应中断点

`continue-github-initial-deployment` 与 pristine 操作分离，只接受已核实的准确 1.0.1
父任务/run/请求与结果摘要及 `EASYPROXY_SBA_GITHUB_UNKNOWN`，不是通用 retry/接管。
原 run 必须 completed、首次尝试且准确 executor/title 一致。原 control 必须逐字节命中
固定摘要，R2 只能包含原 control 和两个正文一致的初始化/环境阶段标记；D1 schema、
迁移历史和四表零行必须一致；Worker/Pages 不存在；GitHub 必须是准确环境 ID、main
策略、仅准确 CONFIG、无 secret 和刷新索引。外部管理写入必须排除；任一漂移即停止。

只在明确批准该恢复后使用。先 create-only 保存新操作的移交证据，再以原 ETag 单次 CAS
将始终非空的 writer 移交至新候选，保留原任务 UNKNOWN 和旧阶段；旧 Permit 失效。
不重放 D1 初始化、GitHub 环境/策略/CONFIG 写入，只补缺失 OWNER、原声明 secrets 和
索引，再走完整发布/审计/同步/读回。marker/CAS/后续阶段 UNKNOWN 一律保留现场，
无自动重试、过期、解锁、删除或回滚。该候选尚未执行成功，不能称应用已部署。

### 预升级的明确边界

SQLite 副本支持 MiSub 当前 schema、普通表/index/view/trigger、rowid、sqlite_sequence、
Unicode/NUL/blob/浮点值；有界分页，拒绝 foreign key、generated column、virtual table 等
未支持的自定义 schema，在目标写入前失败关闭。不宣称支持任意 SQLite 数据库。

候选 settings 在发布前关闭通知、Aggregator Cron 与 WebDAV。`SBA_PURPOSE=preview` 的
ECH 只开放版本和有限产物读取，禁止代理出口；Pages 只开放静态登录页、受鉴权的指定读取、
登录/export/受限 managed-sync，禁止普通数据、Cron、restore 和其他写入。
因此 preview 是受限升级演练环境，不是完整开放的交互式 MiSub 测试站。

R2 的控制记录与待删 bucket 同址，清理使用专门终态，不先 commit/解锁再删除。
持锁删除其他对象、核源、fsync 本地终结意图后，最后删 control 与 bucket 并确认 404。
control 删除后已无远端 writer，特殊终态的 UNKNOWN 依赖平台原任务/资源锁继续禁止接管；
本地意图不是远端锁，也不是提前成功回执。响应丢失不重发，不用后续负读取假定原操作失败。

## 周期刷新与升级的共同写入者

`sba-aggregator-refresh.yml` 从公开仓库变量 `EASYPROXY_SBA_REFRESH_REGISTRY` 读取最多
32 个 production environment；索引没有秘密或批准版本。default branch 只准备 bootstrap，
实际源码取各实例 R2 的 active SHA，核 manifest/workflow，再与升级共用持久 writer。
实例刷新只有七个必需 secrets，不取得 GitHub 管理 token、Cloudflare token、Cookie secret
或备份口令；preview marker 在读取任何 secrets 前拒绝。

GitHub environment PUT 是 upsert，repository variables 也没有 CAS。SpringBok 仓库锁与
R2 writer 不会锁住外部管理员；部署期间必须排除 out-of-band 修改。多实例索引合并后读回，
更新遇到索引缺失会拒绝，不擅自重新启用。旧生产 writers 尚未使用本锁，不能宣称其竞态已消除。

两个历史私有种子字段可使用准确值 `sba-disabled-source-v1` 显式停用。此值不是密码或 URL，
会在调用 materializer 前移除，使对应缺失私有种子禁用；公开种子和公开 crawl 仍运行。
这样新套件可不依赖待退役的旧 seed relay。没有可恢复明文的旧加密 secret 不伪造为已提取，
也不为维持它另建长期服务。产物仍须至少有真实可消费节点并通过相同审计，停用不绕过验收。

## 验证与证据

### 准确 Pages 资产响应恢复（EP-SBA-14）

1.0.3 任务 `dc-3dad4093eba94806b436848210a958fb` / run `40288989518` 再次 UNKNOWN。
真实只读资产查询省略 `errors`；官方 OpenAPI 的 upload/upsert ACK 又不要求 `result`。
兼容仅限三个准确资产 POST 端点；查询仍必须返回合法 hash 列表，普通项目/Worker 合同不变。

另行批准的新 repair `continue-pages-assets-deployment` 固定准确 1.0.3 context/release、
control `aa8c2bf3295c755bba5f2a2018a6c9eff8c93dc1600ac8fdc7750617f6ff80b3`、
writer `0321f9e60e7044b1a73ae1134ab332d4` 和十个原对象。重复核 control/对象及全部
资源前置后才保存新移交证据、单次 CAS；writer 从不清空。D1/GitHub 只读，Worker/Pages
严格先验证现有 1.0.3 而不是旧 1.0.2，再发布一致的新版本来源证明，不新建或删除资源。
原 UNKNOWN 和全部阶段保持；任何响应不确定立即停止，不自动重复恢复。

资产 API 窄修隔离 Linux 全量 `assets-native-54.xml`：805 passed、0 skip；新恢复定向
`assets-repair-55.xml`：195 passed。1.0.4 完整冻结源码 `assets-recovery-native-57.xml` 为
868 passed、0 skip，CLI Go 82 项与 go vet 通过、真实 sealer 重建；SpringBok parser、
Gitleaks 与真实只读七项前置也通过。这些不是正式部署成功证据。
仍须准确源码发布、正式单次恢复、独立无写凭据 verify 和最终版本的双来源真实代理验收。

### 准确 Pages 中断恢复（EP-SBA-13）

1.0.2 的准确 GitHub 恢复任务在创建 Pages 项目后返回 `EASYPROXY_SBA_PAGES_UNKNOWN`。
只读和真实响应离线重放确定：空 preview `env_vars=null` 被过严校验误拒；production 配置正确，
Pages 尚无部署。1.0.3 仅为预期空变量表接受 provider 的 nullable 响应，不放宽必需项。

新的独立 repair 绑定该准确父 task/run/request/result 摘要。用户已另行批准；原批准不复用。
前置逐项核原 control 字节和七个对象、D1 初始化/零行、完整 GitHub、Worker 设置/公开版本/
模块摘要及准确 Pages 项目/配置/零部署，随后再核 control 和对象集合。任一漂移即拒绝写入。
通过后只 create-only 新移交证据及一次 CAS，writer 始终非空；旧 Permit 失效，原证据不动。
不重做 D1/GitHub；相同 Worker 业务 bundle 只更新到新版本、子域不重设；Pages 复用现有项目
完成新版本配置和首次部署。新 UNKNOWN 后停止，不重放、清锁、自动回滚或删除。

`pages-before-33.xml` 两项预期红测保留；窄修后 `pages-full-35.xml` 50 项通过。
恢复编排/持锁/HTTP 合同 `pages-repair-38.xml` 117 项、provider 拒绝反例
`pages-protocol-41.xml` 53 项通过。`pages-readonly-preflight-39.safe.json` 七项真实只读前置
通过，control 和全部原对象保持，没有 marker 或 writer 移交。这些不代表线上恢复成功。
冻结 index 的原生 Linux 全量验证 `linux-native-pages-recovery-42.xml`：780 passed、0 skip；
CLI Go 82 项、0 skip 和 go vet 通过，真实 sealer、CLI、迁移 materializer 已重建。
SpringBok 实际 parser 接受 1.0.3 的三个独立 repair，scoped secrets scan 未发现泄漏。
后续仍须准确 CI、正式单次恢复、独立 verify 及最终源码的本地消费/代理出口验收。

测试临时目录、日志、XML 和构建产物在 `linshi/easyproxy-sba-linux-20261026/`。
最近真实 Linux 构建生成 99 个 Pages 资产及当前 ECH/Functions bundle；production/preview
双 workerd、认证、D1 保持、受限同步和隔离共 34 项通过，无 skip（`workerd-14.xml`）。
销毁编排与 R2 特殊终态 27 项通过（`windows-destroy-03.xml`）。这些均不连接真实云账号。
冻结 Git index 的 Linux 回归 `linux-native-22.xml` 为 **555 passed，无 skip**；
CLI Go 全包 **82 项通过，无 skip**，`go vet` 通过；实际编译的 CLI 成功生成迁移
topology、manifest 及固定 Wrangler 工作目录。根目录 Windows 全量为 **247 passed，
59 subtests passed**（`windows-root-23.xml`），不把 Windows PowerShell 测试的
Linux 环境缺命令错误伪装成已在 Linux 全量通过。

冻结快照回归发现并修复了真实部署阻断：锁文件原哈希来自 Windows CRLF，但 Git
保存 LF。现以 `.gitattributes` 固定锁文件 LF，哈希绑定正式 Git 字节，并加字节回归。
原失败 `linux-native-18.xml` 保留；Windows 首轮缺少既有 Git `usr/bin/cygpath` 的
失败也保留，只给复测进程补 PATH，没有改系统配置或放宽测试。

`aggregator-generation-20/generation.safe.json` 记录无旧 relay、无云写凭据的真实公开
采集：85 个候选节点、6 个发现来源。其实际订阅经本机 EasyProxy 消费审计，48 个
稳定节点、兼容探测通过，审计容器清理并独立确认不存在，见
`public-candidate-audit-24/audit.safe.json`。该次复用先前的审计 fixture 镜像，明确
**不是最终发布 SHA 的正式云审计**；正式任务仍须从批准 SHA 重建并独立验收。
线上部署/业务消费验收仍须独立记录，不能沿用旧 bundle 或本机审计代证。

原准确版本的 hosted 只读预检 run `40267227117` 七项通过（exact-source、tools、runtime、
image、sealer、generation、workflow），`cloudWrites=false`。`pristine-readonly-07.safe.json`
记录当次全资源 pristine；它们都不证明历史未曾尝试写入、不确定原异常根因，也不替代
新候选测试或正式 repair 的现场前置检查。上面的 555/247 是历史快照，不算作 1.0.1 回归。

1.0.1 候选的冻结 index 原生 Linux 回归 `linux-native-repair-10.xml` 为 633 passed，无 skip；
`go-repair-10.safe.json` 为 CLI 82 项、0 skip、go vet 通过，真实 sealer/CLI 和迁移 materializer
重新构建。使用当前全部 SBA 字节及准确基线 checkout，未借旧测试结果代证。无云写入。

1.0.2 的冻结 index 回归 `linux-native-recovery-24.xml` 为 **701 passed、0 skip**；
`go-recovery-24.safe.json` 为 CLI 82 项、0 skip、go vet 通过，真实 sealer、CLI 和迁移准备
已重建。native20 缺 curl 的三个真实 TLS 环境失败与 native22 未完成记录均保留，未放宽产品
保护或跳过测试。部署前 `recovery-readonly-23.safe.json` 的产品只读前置全部通过，原 control、
阶段标记和部分配置保持；该核对不是 writer 移交，也不是部署成功，正式恢复仍会重新现场核验。

Python 全量须提供摘要匹配的 `EASYPROXY_TEST_ESBUILD`、真实产物
`EASYPROXY_TEST_PAGES_BUILD`、真实 sealed-box helper `EASYPROXY_TEST_SEALER`；否则相关用例
会明确 skip。workerd 须提供 `EASYPROXY_TEST_MINIFLARE` 与源码外
`EASYPROXY_TEST_ARTIFACT_ROOT`。测试依赖不进入产品。

SpringBok SUITE-03 正式/Linux executor pin 已部署：
`4d6a8bcd8c52f0ada791729bafc2788a2cffb972`，workflow ID `396352142`；这只是平台前置。
应用必须固定发布 SHA，经 plan、单次 submit、真实 receipt、独立 verify 与本地消费验收。

旧手动资源只有在新套件验收、密钥复用与数据备份完成后才列准确删除清单，等待用户明确
回复“同意”；本轮没有执行真实删除。相关阶段合同见
[实施记录](integration-notes.md)、[工具链](toolchain.md)、[迁移](migration.md)、
[ECH](ech.md)、[Pages](pages.md)、[Aggregator](aggregator.md)。
