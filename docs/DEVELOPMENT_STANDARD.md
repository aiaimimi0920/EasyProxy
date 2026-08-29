# EasyProxy 增量开发规范

生效日期：2026-08-28

## 1. 文档地位与适用范围

本文是 EasyProxy 仓库的通用开发基线，适用于仓库自行维护或携带的以下区域：

- `service/base`：EasyProxy 主运行时、管理 API 和 WebUI；
- `upstreams/misub`：共享来源注册中心与 manifest 服务；
- `upstreams/aggregator`：fallback 订阅产物生成器；
- `upstreams/ech-workers`：本地 ECH connector helper；
- `workers/ech-workers-cloudflare`：Cloudflare ECH Worker；
- `deploy`、`scripts`、`tests`、`.github/workflows` 和根级运维入口；
- EasyProxy 自己维护的文档、配置模板、发布合同和生成流程。

EasyProxy 的第一方集成与部署代码位于根仓库；三个 `upstreams/*` 模块是固定到
维护者公开 Fork commit 的根级 submodule。上游源码变更必须先进入对应 Fork PR，
再由独立根 PR 更新已验证指针。修改时必须明确区分上游同步、EasyProxy 携带
补丁和纯文档/测试调整，并始终使用递归子模块检出。

本文采用 **增量治理**：所有新增代码和被实质扩展的职责必须符合本规范，但
正常功能任务不需要先清偿全仓历史大文件债务。若更具体的架构、发布、安全或
部署文档规定了更严格的规则，以更严格的本地规则为准。

相关合同文档：

- [架构说明](architecture.md)
- [统一来源架构](unified-source-architecture.md)
- [开发工作流](development-workflow.md)
- [发布合同](release-contract.md)
- [发布检查表](release-checklist.md)
- [根宿主部署标准](root-host-deploy-standard.md)
- [Local Server](local-server.md)
- [透明网关](transparent-gateway.md)

## 2. 目标

这套规则用于同时改善：

1. **可维护性**：让文件和目录围绕明确 owner 与单一职责组织；
2. **可验证性**：让 Go、React、MiSub、Python 运维脚本和 Worker 能分别验证；
3. **运行安全**：保护代理凭据、管理凭据、订阅 URL、token 和配置分发材料；
4. **资源正确性**：明确连接、监听器、探针、goroutine、子进程和临时产物的生命周期；
5. **并发正确性**：保护 reload generation、CAS、持久状态和运行时状态的提交边界；
6. **性能**：控制探测、来源刷新、批处理、缓存、并发和 WebUI 热路径成本；
7. **可发布性**：保证源代码、嵌入式前端、Docker 镜像和空白宿主部署结果一致；
8. **持续演进**：通过新增代码约束逐步降低旧债，而不是发动不可控的全仓重写。

行数是设计报警器，不是机械 KPI。短文件也可能职责混乱；高度凝聚的状态机
可以申请软上限例外，但必须给出真实理由和保护证据。

## 3. 仓库 owner 与变更边界

### 3.1 模块 owner

- `service/base` 拥有本地执行面：代理运行时、来源合并、connector 执行、节点
  健康、路由、TUN、管理 API、持久化和 WebUI 后端合同；
- `service/base/frontend` 拥有管理 WebUI 的 React/TypeScript 源码；
- `upstreams/misub` 拥有共享来源注册、profile、manifest 和 Cloudflare/Docker
  兼容存储行为，不拥有客户端本地代理执行；
- `upstreams/aggregator` 只拥有 fallback 订阅产物生成，不应扩张为通用来源注册中心；
- `upstreams/ech-workers` 拥有本地 helper 行为，不拥有 EasyProxy 主生命周期；
- `workers/ech-workers-cloudflare` 拥有 Cloudflare 侧入口，不拥有本地 connector
  物化和节点健康；
- `deploy` 与 `scripts` 拥有部署、发布、派生配置和验证编排，不应复制业务规则；
- `docs` 拥有公开合同和操作说明，不得成为与代码相冲突的第二套行为实现。

修改跨模块合同前，先确认真正 owner。跨模块变更必须在所有受影响层分别验证，
但不要为了方便复制实现或让调用方接管下游 owner 的职责。

### 3.2 旧仓库边界

旧 `ProxyService` 和其他历史仓库只用于行为查询、迁移审计和缺失逻辑对照，不是
前向开发写入目标。所有新实现和修复必须落在 EasyProxy 当前仓库，除非任务明确
授权维护另一个独立仓库。

### 3.3 `upstreams/*` 规则

修改 `upstreams/misub`、`upstreams/aggregator` 或 `upstreams/ech-workers` 时，
源码提交必须发生在对应维护 Fork；根仓库只更新 gitlink。完成报告和 PR 摘要
必须标明以下一种：

1. 上游同步导入；
2. EasyProxy 本地携带补丁；
3. 文档或测试调整。

本地补丁应保持窄小、可识别，并避免无必要地重排大量上游代码。同步上游前先
盘点本地补丁，避免静默覆盖已交付行为。

## 4. 有效代码行定义

### 4.1 计数口径

有效代码行（effective code lines）按以下口径计算：

- 空行不计；
- 仅包含注释的行不计；
- 纯注释的多行区域不计；
- 同时包含代码和内联注释的行计为代码；
- 字符串、模板、宏或内嵌脚本中的可执行实现不能因为表现为文本就自动豁免；
- 仓库已有语言感知 checker 时，以 checker 输出为准；
- 没有 checker 时可以人工复核或使用临时工具测量，但物理行数只能作为粗略上界。

### 4.2 默认纳入范围

以下手写内容默认纳入：

- Go、TypeScript、JavaScript、Vue、Python、PowerShell、Shell 和 Worker 生产代码；
- 测试代码、测试夹具和集成 harness；
- 构建、发布、迁移、运维、配置渲染和验证脚本；
- 手写样式和运行时配置代码；
- Docker entrypoint、Compose 逻辑和 GitHub Actions 中的实质脚本实现。

Markdown 等纯文档不执行源代码行数门禁，但应按主题拆分、保持可导航，并避免
在一个文档中混入多个互不相关的合同。

### 4.3 允许排除的内容

只有以下内容可以由仓库策略显式排除：

- 由确定性工具生成且禁止手工修改的文件；
- 原样 vendored、不可修改的第三方源码；
- 语言、框架或发布合同必须保留的机器产物。

`service/base/internal/monitor/assets` 中的嵌入式 WebUI 资源属于构建产物，权威
源位于 `service/base/frontend`。不得直接手工修补哈希资产来绕开前端构建。

业务代码不能通过复制到 `generated`、`vendor`、JSON、模板、字符串或其他
扩展名获得豁免。任何排除都必须有可审查的路径规则和理由。

## 5. 文件大小分级

| 有效代码行 | 状态 | 要求 |
| --- | --- | --- |
| 约 150 | 最优目标 | 优先形成容易理解和测试的职责单元 |
| 100-250 | 推荐区间 | 通常无需额外说明 |
| 251-500 | 可接受 | 必须保持一个清晰职责和合理内部结构 |
| 501-700 | 软上限例外 | 必须说明凝聚性理由、风险和保护测试 |
| 701-1500 | 不可作为新增完成状态 | 继续按真实职责拆分 |
| >1500 | 硬上限违规 | 无条件二次拆分，不允许豁免 |

`150` 是设计目标，不是要求每个文件达到相同长度。拆出大量仅转发调用、没有
独立 owner 的薄文件，同样违反本规范。

## 6. 新增代码与历史旧债

### 6.1 什么属于新增代码

以下任一情况均受本规范约束：

1. 新建源文件；
2. 新建模块、组件、服务、路由、命令、测试套件或脚本；
3. 在既有文件中加入新的业务职责、状态所有权或协议分支；
4. 新增类型、函数或对既有函数进行实质性扩展；
5. 从旧文件提取、移动、复制或改名后形成的新结果。

移动后的完整文件仍需测量。不能把旧大文件原样移动后声明为非新增代码。

### 6.2 历史旧债

规范生效前已经存在的超标文件属于 grandfathered legacy debt：

- 无关任务不需要清理它们；
- 不因其他超标文件存在而判定本次功能失败；
- 小缺陷修复不需要顺手改写整个旧模块；
- 旧债豁免只保护既有内容，不保护继续向同一文件堆叠新职责。

### 6.3 在既有大文件中开发

优先决策顺序：

1. 判断新增行为的真正 owner；
2. 把新职责放入不超过 500 行的凝聚模块；
3. 在旧文件中只做导入、注册、构造或委托等最小接线；
4. 用测试证明边界和原行为没有被破坏。

局部缺陷、安全问题、协议兼容问题或无法安全外提的极小接线可以原位修改。
应尽量使旧大文件有效行数不增长；确需增长时，在完成报告中记录原因和不立即
提取的风险判断。

### 6.4 主动拆分任务

当任务明确选择大文件进行结构重构时：

- 先用行为测试固定 API、序列化、状态转换、事件和副作用；
- 按职责和依赖方向提取，不按任意行号切块；
- 新产生或完成迁移的文件不得超过 700 行；
- 正常接受上限仍为 500 行，目标仍为约 150 行；
- 未触及的旧债必须如实保留，不得宣称全仓治理完成。

## 7. 501-700 行软上限例外

软上限例外至少应记录：

- 文件路径和有效行数；
- 文件的唯一职责；
- 为什么进一步拆分会破坏强凝聚状态机、制造循环依赖、降低可测试性或增加
  生命周期风险；
- 保护该边界的测试；
- 复核任务或评审记录；
- 后续复核日期（若例外带有效期）。

不得虚构审批人、评审结论、测试结果或源码 hash。以下理由不足以申请例外：

- 没有时间；
- 文件以前就很大；
- 拆分麻烦；
- 框架习惯集中实现；
- 通过压缩格式可以降低物理行数。

## 8. EasyProxy 架构不变量

### 8.1 统一来源合同

`service/base` 的运行时来源优先级必须保持：

1. 本地来源；
2. MiSub manifest；
3. aggregator fallback。

去重基于规范化后的 `(kind, input)`。远端 manifest 和 fallback 节点是运行时
来源，不得覆盖本地持久节点存储；manifest 恢复后 fallback 必须自动退出。

`kind` 是来源分类权威。探测结果只能作为建议元数据，不得静默改写用户保存的
`subscription`、`proxy_uri` 或 `connector` 分类。

支持的 connector 在本地 `service/base` 物化和执行。MiSub 负责描述 connector，
不应代替客户端执行最终代理出口。

### 8.2 配置权威与本地状态

`topology.yaml` 是非敏感部署拓扑权威，只能保存 Secret 的环境变量引用；
`deploy/service/base/config.yaml` 是本地运行时和 WebUI 持久化权威。拓扑更新与普通
部署不得覆盖已经存在的运行时文件。

`upstreams/misub/.env`、`workers/ech-workers-cloudflare/.dev.vars`、bootstrap 和
部署 manifest 属于本地或平台状态，不得提交真实秘密。旧根 `config.yaml`、派生
renderer 和自动同步 GitHub 配置的入口已经删除，不得重新引入第二套配置决策逻辑。

运行时恢复来源优先级必须清晰：已有配置文件优先，其次显式 bootstrap，再其次是
import code 创建 bootstrap；启用远端后台同步时，应明确远端是否拥有后续更新权。
不得让多个权威来源无声明地相互覆盖。

### 8.3 Reload、CAS 与状态发布

涉及配置、节点、Profile、规则或运行时来源的修改必须保持事务边界：

- 候选配置验证成功后才能发布；
- generation 变化后旧探针和旧异步任务不得覆盖新状态；
- 稳定节点可以按明确身份继承健康证据，节点身份变化时必须重置；
- 持久公开规则与运行时本地文件层保持分离；
- CAS 冲突必须显式返回，不得通过 last-write-wins 静默覆盖；
- reload 失败应保留或恢复最后一个可用实例，不能先销毁稳定实例再验证候选；
- 无变化刷新应避免无意义 reload 和健康状态抖动。

### 8.4 Local Server、Smart Routing 与 TUN

- Local Server 只支持 `mode: pool`、`listener.protocol: mixed`、一个 canonical
  凭据和可信 LAN 边界；
- Local Server 不得与 legacy multi-port、hybrid 或额外 listener 拓扑混用；
- Smart Routing route A 复用标准 listener；route B 使用额外端口时，Docker
  发布必须和 YAML 配置同步；
- 原生 TUN、透明网关和规则层变更必须验证路由所有权、回滚、DNS、IPv4/IPv6、
  权限和宿主网络影响；
- 不得用开发机已有系统代理、TUN 或宿主透明路由掩盖 EasyProxy 自身失败。

## 9. 模块设计与注释

### 9.1 按职责拆分

优先使用以下边界寻找拆分点：

- 领域类型、API schema 和序列化合同；
- 输入解析、协议识别和验证；
- 来源合并、路由决策、健康评分和状态转换；
- 网络、数据库、文件系统、Cloudflare 和 GitHub 适配；
- sing-box、connector、listener、TUN 和子进程生命周期；
- React/Vue 展示、状态管理、数据获取和副作用；
- 配置渲染、部署编排与发布验证；
- 测试夹具、断言 helper 和集成 harness。

不要把互不相关的逻辑继续集中到 `common`、`utils`、`helpers`、`misc` 或巨型
`index` 文件。目录和文件名应表达 owner，而不是隐藏 owner。

### 9.2 注释要求

新增或拆分后的文件只添加能降低理解成本的注释，重点解释：

- 模块目的和责任边界；
- 不显然的不变量；
- 权限、凭据和信任边界；
- 并发、取消、重试、reload 与清理约束；
- 缓存、探针、批处理和性能取舍；
- 软上限例外的凝聚性理由。

不要逐行复述代码，不要用注释填充降低有效行数，也不要保留已经失真的历史
解释。行为变化时，代码、测试和合同文档必须在同一轮更新。

## 10. 逐文件质量审查

每个新增文件和每个被实质修改的文件都要按职责完成一次有范围的审查。

### 10.1 安全与秘密

检查：

- 外部输入是否有类型、长度、范围和格式边界；
- 管理 API、Profile、设备身份和 owner 过滤是否在可信边界执行；
- shell、路径、URL、HTML、模板、YAML 和命令参数是否存在注入或穿越；
- 密码、订阅 URL、manifest token、cookie、API key、R2 凭据和 import code
  是否会进入日志、错误、测试快照、Git diff 或发布证据；
- 网络响应、上传、订阅和配置分发产物是否有大小和超时限制；
- 失败是否 fail closed，是否会静默降级到未认证或非预期代理路径。

所有真实秘密必须位于 ignored 配置、环境变量、平台 secret store、import code
或显式私有输入中。不得把真实秘密提交到示例、测试、README 或生成资产。

### 10.2 资源、并发与生命周期

检查：

- cache、历史记录、队列、连接和探测结果是否有界；
- socket、listener、数据库、文件、ticker、goroutine 和子进程是否在所有路径清理；
- context 取消后旧任务是否仍会提交结果；
- 锁顺序、锁范围和 reload barrier 是否安全；
- connector 失败、候选 box 失败和 rollback 是否泄漏端口、进程或临时文件；
- 异步 drain 是否有 owner，容器停止时是否能够确定性退出。

### 10.3 性能

检查：

- 热路径是否引入重复解析、序列化、复制或分配；
- 来源刷新是否绕过缓存、失败冷却或条件复用；
- 节点探测是否存在无界并发、重复握手、排队饥饿或错误预算；
- WebUI 是否在渲染或事件循环中执行阻塞工作；
- 数据库和网络访问是否出现 N+1、无界分页或无背压批处理；
- 缓存失效和批处理结果是否与 generation、来源 revision 和配置 revision 对齐。

确认的问题应在同一 owner 边界内修复并补回归证据。没有证据的问题不得用大
范围“顺手优化”替代当前任务。

## 11. 开发与运行时安全边界

### 11.1 本地迭代

正常开发采用：编辑 owning module → 聚焦测试 → 模块完整验证 → 隔离 Docker
运行验证。GitHub Actions 不作为快速本地循环的主要测试工具。

临时镜像上下文、Compose、env、日志和导出 tar 应放在仓库外的临时工作区，或
仓库已忽略的明确临时目录中。不得把短期验证产物散落到源码目录。

### 11.2 Live 容器

现有生产容器、挂载数据、端口、网络和配置在未经明确授权时是不可扰动边界。
验证 live-adjacent 改动时：

- 先检查真实 image、bind mounts、network、capabilities、restart policy 和配置路径；
- 优先使用独立容器名、端口、网络别名、数据目录和 validation ID；
- 不共享可写 SQLite、运行时数据目录或 TUN owner；
- 任何替换必须先建立配置、数据和旧镜像回滚路径；
- 不得把“容器 running”当作 API、代理链路或语义成功；
- 必须证明流量确实经过 EasyProxy，而不是开发机其他代理或 TUN。

### 11.3 部署边界

- GitHub Actions 用于验证、Cloudflare 部署、aggregator 发布、GHCR/R2/config/
  release 发布；
- 客户端本地运行时部署保持 script-driven，以根 `deploy-host.ps1` 为首选入口；
- `deploy-host.ps1` 必须保持单文件可下载、空白宿主可 bootstrap、可无副作用自检；
- 发布镜像不得依赖 bind mount 当前源码树；
- 正式 release-grade 验证应从 GHCR 拉取目标镜像，在接近空白宿主的环境验证，
  而不是依赖开发机已有本地镜像或源码。

## 12. 验证门禁

### 12.1 每个代码任务的基础门禁

至少执行：

1. 修改前后的有效行数测量；
2. 新增行为的聚焦测试和回归测试；
3. 直接依赖模块的编译、类型检查或静态检查；
4. 仓库官方 formatter 或其检查模式；
5. `python scripts/check-effective-lines.py`；
6. `git diff --check`；
7. scoped `git status`、`git diff` 和未跟踪产物复核。

仓库已由 `scripts/check-effective-lines.py` 和 `effective-lines-baseline.json` 实施
baseline/ratchet 门禁，并在 `.github/workflows/reusable-validate.yml` 中执行。门禁
阻止新增硬超限、旧债增长和已经下降但未移除的过期例外；软超限仍需在评审中说明。
当前 legacy baseline 为空，后续不得用新增例外替代职责拆分。

### 12.2 根脚本与发布合同

```powershell
python -m unittest discover -s "tests" -p "test_*.py" -v
python -m unittest discover -s "upstreams/aggregator/tests" -p "test_*.py" -v
python scripts/validate-release-contract.py
git diff --check
```

### 12.3 `service/base`

```powershell
Set-Location service/base
python ../../scripts/check-go-format.py cmd internal
go test -count=1 -timeout=300s ./...
go vet ./...
go build -tags "with_utls with_quic with_grpc with_wireguard with_gvisor" `
  -o easy-proxy ./cmd/easy_proxies
```

Go 变更至少需要一次编译导向验证。涉及并发、reload、TUN、数据库、探针、路由
或持久化时，应增加针对边界条件和取消/失败路径的聚焦测试。

### 12.4 `service/base/frontend`

```powershell
Set-Location service/base/frontend
npm ci
npm run test
npm run lint
npm run build
```

WebUI 变化必须验证当前源码生成的嵌入式资产，并检查 `git diff` 中的哈希资产
替换是否与源码变化匹配。用户可见布局、登录、设备/Profile、路由或监控语义变化
还必须在最终镜像中进行真实桌面/移动浏览器检查。

### 12.5 `upstreams/misub`

```powershell
Set-Location upstreams/misub
npm ci
npm run test:run
npm run build
```

修改 Cloudflare Pages、D1、KV、SQLite 或 manifest 合同时，还需验证对应生产路径
和兼容路径；不能用其中一个存储后端的成功代表所有后端成功。

### 12.6 Docker、Local Server 与网关

涉及运行时、嵌入式前端、认证或部署拓扑时，使用正式 Dockerfile 构建新镜像。
Local Server 变化按 [发布检查表](release-checklist.md) 运行隔离验证脚本；透明网关
和 TUN 变化运行 `scripts/validate-transparent-gateway.ps1` 及对应场景验证。

验证资源必须是 disposable、label-scoped，并且不得启动、停止、替换或连接未授权
的 legacy/live 容器。

### 12.7 文档任务

纯文档或治理规则变更不需要伪造二进制发布。至少验证：

- 路径和相对链接存在；
- 命令与当前 package/workflow 脚本一致；
- UTF-8 无 BOM、无意外秘密；
- `git diff --check` 和 scoped status/diff 正常。

## 13. 发布完成定义

源代码和单元测试通过不等于产品发布完成。涉及产品行为或部署的任务，根据范围
至少需要证明：

- 正式前端产物已嵌入 Go 服务；
- 正式 Dockerfile 构建成功；
- release contract checker 通过；
- 最终镜像的启动、管理 API 和真实代理请求成功；
- 配置、数据、端口、网络、capabilities 和 executable path 符合目标合同；
- 发布路径需要 GHCR 时，目标镜像已从 GHCR 拉取并在目标场景验证；
- config/R2/import-code 变化产生并验证了合同规定的 artifacts；
- 失败时回滚步骤和保留资产明确。

不能只用 HTTP 200 代表异步任务完成，也不能只用 `running` 代表代理链路正常。
必须检查语义结果、错误状态、日志和目标数据面。

## 14. 反规避规则

以下行为均视为违反本规范：

- 用注释、空行、minify、合并语句或破坏格式规避行数；
- 把可执行代码藏入字符串、模板、JSON 或生成目录；
- 把新职责转移到巨型 `common/utils/helpers/index`；
- 拆出大量没有独立 owner 的一行代理文件；
- 删除测试、排除路径或修改 checker 来让违规消失；
- 直接手改嵌入式前端产物而不修改权威源码；
- 把真实 secret 写入示例、测试、日志、截图或发布说明；
- 把远端来源持久化为本地 owner 数据，或让 fallback 覆盖 manifest/local 权威；
- 用本地源码 bind mount 或开发机隐藏网络状态伪造 release 验证；
- 未标记地把 EasyProxy 本地补丁混入 upstream sync；
- 为完成单模块任务顺手修改无 owner 关系的其他模块。

## 15. 完成报告要求

完成开发时，应明确报告：

- 变更 owner、模块范围和用户可见结果；
- 新增/实质修改文件的有效行数和阈值状态；
- 遗留超标文件是否只是未触及旧债；
- 任何 501-700 行软例外及其理由和保护测试；
- 对既有大文件的必要增长及原因；
- `upstreams/*` 变更是同步、携带补丁还是文档/测试；
- 已运行的测试、编译、格式、lint、checker、镜像和 release 验证；
- 真实运行时证据与回滚边界；
- 尚未解决的安全、生命周期、性能或部署风险；
- 工作区是否仍有用户或并行任务留下的未提交改动。

“仓库里仍有历史大文件”本身不代表本次任务未完成；“本次新增代码制造新的
超标文件、继续向旧大文件堆新职责、跳过必要发布验证或越过 owner 边界”则属于
未完成。

## 16. 本地落地原则

- 本规范首先作为人工与评审基线执行；
- 新增 checker 时先建立可审查 baseline 和明确排除项；
- CI 只 ratchet 新违规和旧债恶化，不无差别阻断全部历史文件；
- README、贡献指南、发布检查表和模块文档不应复制出互相冲突的规则；
- 合同变化时更新最接近 owner 的文档，并从其他文档链接到权威位置；
- 后续若增加仓库根 `AGENTS.md`，必须包含本规范的自包含摘要和链接，且可以增加
  更严格规则，但不得降低本规范要求。
