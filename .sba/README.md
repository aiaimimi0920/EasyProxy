# EasyProxy 云套件 SBA 接入

## 当前状态（2026-10-26）

五动作入口、实例周期刷新、manifest v3 与 deployment v2 已实现，固定发布源码已通过本地验证。
首次 SpringBok 部署已单次提交，但应用回执为 `unknown`；没有重放或清锁，旧服务保持。
应用尚未通过 SpringBok 正式上线，不能把本地测试、构建或平台上线当作应用上线。
当前完整合同及边界见 [生命周期与调度](lifecycle.md)。旧手动部署及工作流保持不动。

### EP-SBA-10：无写凭据 hosted 预检（负责人：主 AI）

原任务只保留通用异常回执，runner 私有日志不能恢复具体原因。新增手动工作流
`SBA Read-only Preflight`，输入准确 40 位源码 SHA，以诊断入口所在提交为 driver，
独立检出目标根与准确 gitlinks、安装其原锁文件及私有 Python，真实下载固定工具并构建
运行时、审计镜像、sealer 和公开采集产物。目标库函数从该准确 checkout 加载，不以
driver 版本替换；两个可选私有种子固定停用。仅保留固定字段/固定阶段错误的公开报告，
不上传私有日志、订阅正文或任何凭据。不调用部署、R2 writer、provider 客户端或原任务。

此入口只排查准备阶段，不验证业务秘密、首次云写入、线上资源、部署成功或独立 verify。
本地固定源码预备和四个官方工具下载已分别通过；hosted 复现仍待真实执行，未确定线上根因。

## 以下为分阶段历史实施记录

下面各阶段的“尚未”描述是当时停点；以顶部当前状态和生命周期文档为准。

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
