# EasyProxy 标准套件生命周期与调度

## 当前状态（2026-10-26）

五动作应用入口、实例周期刷新、manifest v3 与 deployment v2 已实现；1.0.1 包含受限 repair
候选。首次任务返回 UNKNOWN，未重放、未清锁。应用尚未通过
SpringBok 正式部署，不能把本地合同测试、构建或平台 SUITE-03 上线当作应用上线。
旧手动部署和旧工作流仍保留，不被本套件隐式接管。

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
平台清单含 D1、R2、Worker、Pages、Actions environment 和两个 provider origin，共七项。
秘密只引用 manifest 的 11 个名称；不得放进公开配置、日志或回执。

所有动作业务预算为 3300 秒，父进程另留 120 秒子进程清理，平台上限 3600 秒。
结果是 create-only、UTF-8 无 BOM 的协议文件，不以 stdout 或进程退出 0 代替业务回执。
发生写入结果不明时报告 UNKNOWN，不自动重放、回滚或清锁。
新版本只记录固定白名单阶段错误码，不把内部异常或 provider 响应正文放进回执。

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
