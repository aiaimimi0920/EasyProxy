# EasyProxy 全面优化开发计划

> 状态：执行中；Phase 0 至 Phase 5 已完成；Phase 6 代码与发布契约已完成，真实跨平台/NAS 发布验收保留到 Phase 8；下一步执行 Phase 7。
>
> 优化前基线标签：优化前的稳定版本
>
> 基线提交：1b555e682a25d5b7bbcfd3c6a384cecb737019aa
>
> 计划原则：允许破坏历史兼容，但禁止无计划地丢失生产数据、密钥、资源身份和可回退能力。

## 1. 文档目的

本计划把 EasyProxy 从“多个功能已经可运行的代码集合”优化为一套：

- 上游边界明确；
- 根仓库职责清晰；
- 可由 Fork 用户自行部署；
- 可安全、重复地更新；
- 可在 Linux、Windows 和 NAS 上快速安装；
- 可验证、可备份、可回滚；
- 自研代码复杂度受控；
- 云端控制平面与局域网执行平面可一键串联；

的完整网络代理拓扑产品。

本计划是后续实施的唯一主计划。阶段实施文档、Issue、PR 和验收报告必须引用本计划中的阶段编号和工作项编号。

## 2. 已冻结的优化前基线

### 2.1 基线状态

优化前代码已经完成以下冻结动作：

- main 已推送到 origin；
- 标签“优化前的稳定版本”已创建并推送；
- 标签指向提交 1b555e682a25d5b7bbcfd3c6a384cecb737019aa；
- 基线包含开发规范 docs/DEVELOPMENT_STANDARD.md；
- 基线包含代理路由失败只硬冷却实际失败节点的修复和回归测试。

### 2.2 基线用途

该标签用于：

- 对照优化前行为；
- 临时构建旧版本；
- 检查重构是否遗漏能力；
- 在优化版本尚未迁移生产数据时回退代码；
- 生成行为差异和发布说明。

### 2.3 标签不能替代数据备份

Git 标签只能恢复源代码和当时跟踪的文件，不能自动恢复：

- Cloudflare D1 数据；
- Cloudflare Worker Secret；
- Pages 环境变量；
- R2 对象；
- EasyProxy SQLite 数据；
- 本地用户配置；
- WebUI 保存的运行时状态；
- 外部服务中的订阅和 Profile。

任何涉及数据结构或资源绑定的阶段，都必须先建立独立的数据备份和恢复流程。

## 3. 优化目标

### 3.1 用户目标

普通用户的最终流程应当是：

1. Fork EasyProxy 根仓库；
2. 启用 GitHub Actions；
3. 添加最少量的 Cloudflare 和产品密钥；
4. 填写一份不包含密钥的拓扑配置；
5. 点击 Bootstrap Cloud Topology；
6. 得到自己的 Aggregator、MiSub 和 ECH Worker；
7. 在 Linux、Windows 或 NAS 上执行一个安装入口；
8. EasyProxy 自动接入云端拓扑并开放局域网代理服务；
9. 后续通过 Update 安全更新，通过 Verify 随时检查，通过 Backup 和 Restore 保护数据。

普通用户不需要：

- 理解各上游仓库的内部结构；
- 手工进入子模块执行部署；
- 手工创建 D1 表；
- 手工拼接 MiSub manifest；
- 手工把 ECH Worker 配置复制到多个位置；
- 依赖开发源码目录运行生产服务；
- 在每次更新时重新创建数据库或重新配置所有客户端。

### 3.2 维护者目标

维护者应当能够：

- 明确区分上游继承代码与自研代码；
- 定期同步上游而不覆盖本地补丁；
- 查看每个 Fork 相对上游的差异；
- 在根仓库固定经过测试的子模块 commit；
- 用统一工作流验证跨模块契约；
- 用统一发布清单重现任意正式版本；
- 对自研代码执行行数和复杂度门禁；
- 删除已经失效的兼容层、旧字段、旧脚本和重复入口。

### 3.3 产品目标

最终产品由两个平面组成：

#### 云端源与控制平面

- Aggregator：公开订阅发现、清洗、验证、转换和稳定产物发布；
- MiSub：全局来源注册、Profile、订阅输出和 machine manifest；
- ECH Worker：Cloudflare 上的远端 WebSocket/TCP 隧道服务；
- GitHub Actions：部署、更新、验证、备份和发布控制面。

#### 局域网执行平面

- EasyProxy：本地订阅展开、连接器执行、节点健康、路由和最终代理出口；
- HTTP、HTTPS CONNECT、SOCKS5 数据面；
- Web Console 和管理 API；
- Linux、Windows、NAS/Docker 部署与更新。

## 4. 明确的非目标与破坏性变更政策

### 4.1 不保留历史兼容

本轮优化不要求继续兼容以下历史形态：

- 旧目录结构；
- 旧的 copy-style 上游同步方式；
- 已废弃的部署脚本入口；
- 重复的 Dockerfile 和 Compose 契约；
- 旧配置字段和别名；
- 已明确废弃的 API 路径；
- legacy multi-port、hybrid 和 extra-listener 等不再属于目标产品的组合；
- 仅为旧实现保留的兼容 DTO、分支和适配层；
- 旧版本未公开承诺的内部函数和文件布局。

第一方调用方必须在同一阶段迁移到新契约，随后直接删除旧实现，不保留长期双轨。

### 4.2 必须保留的内容

“不兼容旧代码”不等于“可以丢数据”。以下内容必须迁移或明确备份：

- MiSub subscriptions、proxy URI、connectors、Profiles 和 settings；
- MiSub Cron 配置和运行所需认证信息；
- Cloudflare Pages、D1、Worker、R2 的稳定资源身份；
- ECH Worker URL、路由和认证连续性；
- EasyProxy 本地配置、节点、规则、设备映射、Profile 和 SQLite 数据；
- 用户自行添加的来源和连接器；
- 生产环境中仍被用户使用的公开端点。

如确实需要更换端点或密钥，必须执行显式迁移，而不是在普通 Update 中静默替换。

### 4.3 不在本轮做的事情

- 不把所有模块改写为同一种语言；
- 不为上游继承代码做纯粹追求行数的重写；
- 不建立多个并行的部署控制面；
- 不在普通更新流程中提供自动销毁；
- 不把 Secret 写入跟踪文件、构建产物或普通日志；
- 不在没有端到端证据时宣称支持某个平台。

## 5. 当前状态与主要问题

### 5.1 上游 Fork 与子模块迁移已完成

三个上游路径已经从历史复制目录转换为公开子模块，根仓库只固定经过
验证的 Fork commit：

| 当前目录 | 正式上游审计基线 | 根仓库固定的 Fork commit |
| --- | --- | --- |
| upstreams/aggregator | wzdnzd/aggregator@27daeb847cfdcf8f7675d701b419cc420739db74 | aiaimimi0920/aggregator@a38278046b9401fed5bf6205ed41d3ec588cfac4 |
| upstreams/misub | imzyb/MiSub@8f18021 | aiaimimi0920/MiSub@90c2a9ba752ba662290e6838a903603f0d304065 |
| upstreams/ech-workers | hhsw2015/ech-workers@46c6c71 | aiaimimi0920/ech-workers@1244581385d50ca600524e89af0b3fdde67918e6 |

`workers/ech-workers-cloudflare` 仍为根仓库第一方代码。Aggregator 的嵌套
`manager` 也通过公开 URL 固定。历史 copy-sync 脚本已经删除，后续同步
使用 Fork PR + 根子模块指针 PR 两阶段流程。

### 5.2 部署入口已存在但重复度高

当前已有六个根工作流：

- .github/workflows/validate.yml
- .github/workflows/deploy-aggregator.yml
- .github/workflows/deploy-cloudflare.yml
- .github/workflows/publish-ghcr-images.yml
- .github/workflows/publish-github-release.yml
- .github/workflows/publish-service-base-config.yml

现有基础能力包括：

- Aggregator 定时和手动发布；
- MiSub Pages/D1 资源解析和部署；
- ECH Worker Wrangler 部署；
- GHCR 多架构镜像；
- R2 配置分发；
- 部署后验证。
- 所有 Actions checkout 均使用递归子模块检出。

主要问题：

- 多个工作流重复安装依赖和执行同一组 preflight；
- Secret 和 Variable 数量多，Fork 用户缺少最短配置路径；
- bootstrap 和 update 有输入名称，但尚未形成完整生命周期合同；
- 缺少统一 backup、restore、rollback 和 secret rotation；
- deploy-subproject.ps1 同时调度过多产品和发布行为；
- 根脚本与 deploy/service/base 下的运行时部署参数重复；
- GitHub Release 只发布说明，没有正式的 Windows/Linux 原生安装包；

### 5.3 MiSub 数据安全基础不足

当前 D1 模型包含 subscriptions、profiles 和 settings 三张表，但：

- 没有正式的 migrations 目录和版本链；
- 主要依赖一次性 schema.sql；
- Wrangler 配置物化时如果 database ID 错误，会绑定到另一数据库；
- Pages 部署前没有强制备份和数据库身份校验；
- KV 到 D1 迁移没有事务、完整校验和正式回滚；
- 前端备份不包含完整 settings；
- 服务端导出 settings 使用的键与实际常量不一致；
- D1-only 模式的 Cron 历史持久化不完整；
- 普通导入具有覆盖当前数据的边界。

### 5.4 ECH 生命周期尚未形成原子更新

当前能力：

- Cloudflare Worker 要求 ECH_TOKEN；
- Wrangler 可以部署 Worker 并同步 Secret；
- 验证脚本可以检查 HTTP Banner 和 WebSocket 子协议；
- MiSub 同步脚本可以写入 connector 并验证 manifest；
- 本地 Go ech-workers 可以打包进 EasyProxy 镜像。

主要问题：

- 普通部署可能同步新 Token，但没有明确区分更新和轮换；
- Worker Secret 与 MiSub connector 更新不是原子操作；
- 当前只支持单 Token，安全轮换会产生短暂不一致；
- managed connector 替换发生在新端点完成验证之前；
- server IP 默认可能在同步时丢失；
- 独立 Go helper 镜像只做帮助输出 Smoke，没有真实隧道 E2E。

### 5.5 本地发布和更新不完整

当前主要生产路径是 Linux 容器，但：

- service/base 与 deploy/service/base 存在两套 Docker 定义；
- 两套镜像的数据目录分别偏向 /etc/easy-proxy/data 和 /var/lib/easy-proxy；
- Compose 和入口脚本对配置可写性假设不一致；
- RESET_STORE_ON_BOOT 可以删除 SQLite；
- 没有 Windows 原生安装器、服务管理和回滚合同；
- GitHub Release 没有上传原生二进制和校验文件；
- Docker 更新没有正式备份、健康切换和镜像回滚流程；
- 当前 release-contract.json 未声明本地二进制和回滚产物。

### 5.6 第一方代码存在明显复杂度热点

当前第一方主要大文件包括：

| 文件 | 物理行数 | 主要风险 |
| --- | ---: | --- |
| service/base/internal/monitor/server_test.go | 4060 | 测试主题混杂 |
| service/base/internal/monitor/server.go | 3390 | API、认证、资产和状态混杂 |
| service/base/internal/monitor/proxy_service_compat.go | 3002 | 兼容合同过大 |
| service/base/internal/boxmgr/manager_test.go | 2739 | 编排测试集中 |
| service/base/internal/config/config.go | 2615 | 解析、验证、路径和远端逻辑混杂 |
| service/base/internal/monitor/manager.go | 2484 | 节点状态与探针职责混杂 |
| service/base/internal/boxmgr/manager.go | 2387 | 跨层编排耦合 |
| service/base/internal/builder/builder.go | 1738 | 构造职责过大 |
| service/base/internal/store/sqlite.go | 1359 | 持久化边界过大 |
| service/base/internal/subscription/manager.go | 1320 | 来源生命周期混杂 |
| service/base/internal/outbound/pool/pool.go | 1319 | 池策略和运行时混杂 |
| service/base/internal/monitor/local_server.go | 1251 | LAN 管理职责集中 |
| service/base/internal/subscription/connectors.go | 1239 | 多连接器实现集中 |
| service/base/frontend/src/components/ManagePanel.tsx | 1154 | UI、状态和副作用混杂 |
| service/base/frontend/src/components/SettingsPanel.tsx | 1122 | 表单、持久化和呈现混杂 |

这些文件超过当前开发规范阈值，必须通过行为保护后的职责提取逐步消除。

## 6. 目标仓库结构

目标结构如下：

~~~text
EasyProxy/
├─ .github/
│  └─ workflows/                  # 用户入口与可复用工作流
├─ upstreams/
│  ├─ aggregator/                 # submodule -> 我们的 aggregator Fork
│  ├─ misub/                      # submodule -> 我们的 MiSub Fork
│  └─ ech-workers/                # submodule -> 我们的 ech-workers Fork
├─ service/
│  └─ base/                       # 第一方 EasyProxy
├─ workers/
│  └─ ech-workers-cloudflare/     # 第一方 Cloudflare Worker
├─ deploy/
│  ├─ cloud/                      # 根仓库云端生命周期包装
│  └─ local/                      # 根仓库本地部署资产
├─ tools/
│  └─ easyproxyctl/               # 第一方跨平台生命周期 CLI
├─ scripts/                       # 仅保留薄包装和维护脚本
├─ docs/
├─ topology.example.yaml          # 非敏感拓扑模板
├─ release-contract.json
└─ OPTIMIZATION_DEVELOPMENT_PLAN.md
~~~

路径可以在实施 ADR 中微调，但所有权边界不得倒退：

- 上游产品源码只存在于我们的 Fork；
- 根仓库只固定子模块 commit；
- 根仓库拥有所有跨模块集成、部署和产品契约；
- 普通用户只操作根仓库。

## 7. 上游 Fork 与子模块治理

### 7.1 子模块规则

每个上游子模块必须：

- 指向我们控制的公开 Fork；
- 固定到完整 commit；
- 保持原路径，降低构建和部署迁移成本；
- 在 Fork 中配置 upstream remote；
- 提供 UPSTREAM.md；
- 记录上游 URL、基线 commit、本地补丁和同步步骤；
- 通过根仓库集成测试后才允许更新指针。

### 7.2 代码行数规则

根仓库复杂度门禁按以下规则计算：

#### 不纳入根仓库行数门禁

- 子模块中从上游继承且未实质重写的代码；
- 上游生成资产；
- 第三方 vendored 内容；
- 数据库生成产物和构建产物。

#### 必须遵循开发规范

- service/base；
- workers/ech-workers-cloudflare；
- deploy、scripts、tools 和根工作流；
- 我们在 Fork 中新增的文件；
- 我们在 Fork 中实质重写的文件；
- 第一方测试和文档工具。

禁止通过把第一方代码放入 Fork 来规避门禁。

### 7.3 上游同步流程

目标同步过程：

1. Fork 内抓取 upstream；
2. 创建 upstream-sync PR，不直接强推生产分支；
3. 运行 Fork 自身测试；
4. 生成本地补丁冲突报告；
5. 根仓库创建子模块指针更新 PR；
6. 运行跨模块契约测试；
7. 合并后发布新根版本。

普通 Fork 用户不需要 Fork 子模块。只有要修改上游模块的高级用户才覆盖 .gitmodules URL。

### 7.4 子模块迁移顺序

按耦合风险排序：

1. upstreams/ech-workers；
2. upstreams/aggregator；
3. upstreams/misub。

每次只迁移一个目录，保留相同路径，完成全仓验证后再进入下一项。

Aggregator 内部还带有自己的 .gitmodules，根检出必须使用递归子模块并验证嵌套内容存在。

## 8. 统一拓扑配置与部署状态

### 8.1 配置分层

优化后只保留四类配置：

| 层 | 内容 | 是否跟踪 |
| --- | --- | --- |
| 产品默认值 | 稳定默认行为 | 是 |
| topology.yaml | 组件开关、资源名、公开地址、非敏感部署参数 | 用户 Fork 中可跟踪 |
| GitHub Environment Secrets | Cloudflare Token、管理员密码、ECH Token | 否 |
| 运行时状态 | D1 ID、部署版本、镜像 digest、迁移版本 | 平台查询或私有状态清单 |

当前把根 config、派生 config、WebUI 修改混在同一权威链中的方式必须结束。

### 8.2 拓扑配置最小字段

topology.yaml 只需要表达：

- deployment_name；
- 启用的组件；
- Cloudflare account 和可选 zone；
- Pages 项目名；
- D1 数据库名；
- Worker 名；
- R2 bucket 和公开基址；
- 是否使用默认 pages.dev、workers.dev；
- Aggregator 调度频率；
- MiSub 默认 Profile；
- 本地 EasyProxy 接入方式；
- 发布通道 stable 或 candidate。

Secret 只能以环境变量名引用，不能写入 topology.yaml。

### 8.3 资源身份发现

Bootstrap 和 Update 必须使用确定性名称查询资源：

- 存在则复用；
- Bootstrap 中不存在才创建；
- Update 中预期存在但找不到则失败；
- 禁止 Update 自动创建替代数据库；
- 每次运行输出不含 Secret 的 deployment-manifest.json；
- manifest 记录资源 ID、版本、schema、URL、镜像 digest 和子模块 commit。

Cloudflare API 查询结果和 topology.yaml 中的确定性名称是资源身份权威。
deployment-manifest 是可审计的版本记录，不是唯一真相：

- 主副本写入私有 R2 的 state/<deployment_name>/ 路径；
- GitHub Artifact 和 Release manifest 只作为发布证据副本；
- manifest 带内容校验和，并记录生成 workflow run；
- manifest 缺失时可以从 Cloudflare API 重建；
- 重建结果与 topology 不一致时，Update 必须停止并要求人工确认；
- 任何 Secret 都不能写入 manifest。

## 9. 统一生命周期控制面

### 9.1 easyproxyctl

新增一个第一方、跨平台的生命周期 CLI，作为 Actions 和本地安装器共享的合同执行器。

建议命令面：

~~~text
easyproxyctl topology validate
easyproxyctl cloud bootstrap
easyproxyctl cloud update
easyproxyctl cloud verify
easyproxyctl cloud backup
easyproxyctl cloud restore
easyproxyctl cloud rotate-ech-token
easyproxyctl local install
easyproxyctl local update
easyproxyctl local verify
easyproxyctl local rollback
~~~

建议使用 Go 实现并放在 tools/easyproxyctl，原因是：

- Windows 和 Linux 可发布单文件；
- 不要求本地预装 Python；
- 可以复用结构化配置和校验；
- 可以减少 PowerShell、Bash、Python 三套业务规则；
- Shell 和 PowerShell 只保留下载和启动薄包装。

Wrangler、Docker、GitHub API 等平台命令仍可由 CLI 调用，但资源决策和安全门必须只实现一次。

### 9.2 用户可见工作流

目标用户入口：

- Validate Configuration
- Bootstrap Cloud Topology
- Update Cloud Topology
- Verify Cloud Topology
- Backup MiSub
- Restore MiSub
- Rotate ECH Token
- Publish EasyProxy Release
- Check Upstream Updates

### 9.3 可复用内部工作流

将重复逻辑收敛为 workflow_call 工作流：

- checkout-recursive；
- validate-root；
- validate-aggregator；
- validate-misub；
- validate-service-base；
- cloudflare-auth-preflight；
- resolve-cloudflare-resources；
- deploy-misub；
- deploy-ech-worker；
- publish-aggregator；
- verify-topology；
- build-release-artifacts。

旧六个工作流在新入口完成验证后直接删除或替换，不保留同义入口。

### 9.4 Actions 安全要求

- Cloudflare 使用最小权限 API Token，不使用 Global API Key 作为推荐路径；
- DNS 权限与部署权限分离；
- PR 工作流不能读取生产 Secrets；
- 生产部署使用 GitHub Environment；
- Restore 和 Secret Rotation 要求人工审批；
- 每个 deployment_name 使用 concurrency 锁；
- 第三方 Actions 固定到明确版本，安全关键入口优先固定完整 SHA；
- 日志不得输出 Token、密码、Cookie、导入码和完整私有配置；
- Actions Summary 只输出非敏感 URL、版本和验证结论。

Fork 用户首版目标配置为：

| 名称 | 存储位置 | 是否必需 | 用途 |
| --- | --- | --- | --- |
| CLOUDFLARE_API_TOKEN | GitHub Environment Secret | 是 | Pages、D1、Worker 和可选 R2 部署 |
| CLOUDFLARE_ACCOUNT_ID | Repository Variable | 是 | 资源所属账户 |
| MISUB_ADMIN_PASSWORD | GitHub Environment Secret | 是 | MiSub 管理认证 |
| MISUB_MANIFEST_TOKEN | GitHub Environment Secret | 是 | machine manifest 认证 |
| MISUB_CRON_SECRET | GitHub Environment Secret | 是 | 外部 Cron 认证 |
| EASYPROXY_BACKUP_PASSPHRASE | GitHub Environment Secret | 是 | MiSub 加密备份与恢复 |
| ECH_TOKEN | GitHub Environment Secret | 是 | ECH Worker 认证 |
| CLOUDFLARE_ZONE_ID | Repository Variable | 否 | 自定义域名 |
| CLOUDFLARE_DNS_TOKEN | GitHub Environment Secret | 否 | 自定义 DNS 自动化 |
| R2 专用凭据 | GitHub Environment Secret | 视存储方案 | Aggregator 产物和私有备份 |

GitHub 不会把原仓库 Secret 复制给 Fork，工作流也不能凭空获得用户的
Cloudflare 权限。因此首次配置必须由用户添加这些 Secret；项目负责提供
最小权限模板、配置检查和不泄密的错误提示。

## 10. Cloudflare 生命周期设计

### 10.1 Bootstrap 合同

Bootstrap 必须：

1. 校验 topology.yaml；
2. 校验 Token 权限；
3. 查询资源；
4. 仅创建缺失资源；
5. 创建或复用 D1；
6. 初始化 migration 表；
7. 设置 Pages 绑定和 Secrets；
8. 部署 MiSub；
9. 创建或复用 ECH Worker；
10. 仅在 Secret 缺失时初始化 ECH Token；
11. 部署 ECH Worker；
12. 配置 Aggregator；
13. 将 Aggregator 和 ECH connector 写入 MiSub；
14. 验证 manifest；
15. 输出本地 EasyProxy Bootstrap 信息。

重复执行 Bootstrap 不得：

- 创建第二个 D1；
- 清空 MiSub；
- 轮换 ECH Token；
- 改变公开资源名；
- 覆盖用户添加的来源；
- 删除非托管 Profile。

### 10.2 Update 合同

Update 必须：

1. 读取 deployment-manifest，缺失时从 Cloudflare API 重建候选；
2. 查询并核对真实资源身份、确定性名称和绑定；
3. 检查当前版本和目标版本；
4. 创建并验证备份；
5. 执行兼容的数据迁移；
6. 部署候选版本；
7. 运行语义健康检查；
8. 成功后提升为生产版本；
9. 失败时保留旧代码或执行明确回滚；
10. 不自动销毁资源。

### 10.3 Verify 合同

Verify 完全只读，至少检查：

- Cloudflare 资源名称和 ID；
- D1 binding 为 MISUB_DB；
- MiSub 页面、登录、数据 API、manifest 和 Cron 状态；
- Aggregator discovery 和 stable artifact；
- ECH Worker HTTP Banner；
- ECH WebSocket Token 握手；
- MiSub ECH connector；
- ZenProxy connector 元数据；
- EasyProxy 获取 manifest；
- Secret 未出现在公开响应；
- 组件版本与 deployment-manifest 一致。

## 11. MiSub 数据保护与迁移

### 11.1 正式迁移体系

新增：

- migrations 目录；
- schema_migrations 表；
- 单调递增 migration ID；
- 每个 migration 的 apply、验证和恢复说明；
- CI 空数据库迁移测试；
- CI 基线数据升级测试。

普通 schema.sql 只用于构建新数据库，不再承担生产升级。

### 11.2 一次性破坏性数据模型优化

由于本轮不要求历史 API 兼容，可以对 MiSub 数据模型进行一次清理，但必须：

- 从基线导出完整逻辑数据；
- 明确旧字段到新字段的映射；
- 转换 subscriptions、profiles、settings 和 connectors；
- 在独立临时 D1 中演练；
- 验证关键 ID、customId、Profile 关系和 Secret 引用；
- 发布后不再同时维护旧字段和新字段；
- 在成功切换前保留原数据库和备份。

当前 connector 不是独立 D1 表，而是 subscriptions 主数据中的
kind=connector 来源记录，并由 Profile 引用其 ID。迁移设计必须明确：

- connector 来源字段映射；
- options.connector_type 和 options.connector_config；
- Profile manualNodes 或目标关系中的引用；
- 托管来源与用户来源的所有权；
- ECH 和 ZenProxy Secret 引用；
- 被禁用、失联和候选 connector 的状态。

### 11.3 备份合同

备份必须包含：

- subscriptions；
- proxy URI；
- connector metadata；
- profiles；
- settings；
- Cron 配置；
- schema version；
- 应用版本；
- 资源身份；
- 记录数量和校验和。

备份不得明文包含 GitHub Secrets。包含敏感 settings 的备份必须加密并存入私有位置。

备份成功条件：

- 产物非空；
- 可解析；
- 包含所有必要数据域；
- 校验和正确；
- 能在临时数据库完成恢复演练。

### 11.4 更新顺序

采用 expand-contract：

1. 添加新结构；
2. 迁移数据；
3. 证明优化前代码仍可在扩展后的结构上运行；
4. 新代码读取新结构；
5. 验证新代码并演练代码回滚；
6. 稳定一个发布周期；
7. 再删除旧结构；
8. 删除前再次备份并演练完整数据恢复。

本轮允许最终删除旧结构，但 destructive contract 不能与首次数据迁移和
候选代码部署发生在同一发布。完成 contract 后，代码回滚的下界必须写入
release manifest；若必须回到更早代码，只能先恢复对应数据备份。

### 11.5 必修缺陷

- 修复 settings 导出键错误；
- 统一前端、服务端和 D1 的完整备份格式；
- 使 Cron 状态在 D1 主路径下可持久化；
- 使 KV 到 D1 迁移可重入并可验证；
- 禁止存储类型静默切换到另一套空数据；
- 导入前自动备份；
- 导入后验证关键实体；
- 删除无写能力的静默降级路径，启动时直接报告错误绑定。

## 12. ECH Worker 生命周期

### 12.1 所有权

- workers/ech-workers-cloudflare 保留根仓库并遵循第一方行数门禁；
- upstreams/ech-workers 迁移到我们的 Fork 子模块；
- 根仓库拥有二者之间的 connector、部署、验证和发布合同。

### 12.2 普通更新

普通 Update 必须保持：

- Worker 名称；
- workers.dev URL；
- 自定义域名和路由；
- ECH Token；
- MiSub connector ID；
- 可选 server IP；
- EasyProxy 可读取的 manifest 关系。

普通 Update 禁止自动生成新 Token。

### 12.3 安全轮换

单独提供 Rotate ECH Token：

1. Worker 临时接受旧 Token 和新 Token；
2. 部署并验证新 Token；
3. MiSub connector 写入新 Token；
4. 等待 EasyProxy 完成 manifest 同步；
5. 通过真实隧道验证；
6. 移除旧 Token；
7. 更新部署清单。

Cloudflare Worker 需要支持一个有期限的双 Token 过渡窗口。轮换失败时保留旧 Token。

轮换验证必须证明：

- overlap 阶段旧 Token 和新 Token 都能建立真实隧道；
- 任意错误 Token 始终被拒绝；
- MiSub manifest 已切换到新 Token；
- 至少一个 EasyProxy 已完成同步并通过真实代理请求；
- 撤销后新 Token 成功、旧 Token 失败；
- 任一步失败时旧 Token 仍可恢复；
- 观测窗口内不存在连续健康检查中断。

### 12.4 Connector 原子切换

- 新 Worker 端点先部署和验证；
- MiSub 增加候选 connector，不立即删除旧 connector；
- EasyProxy 验证候选端点可用；
- Profile 切换；
- 观察稳定后删除旧 connector。

不得先删除旧 managed source 再测试新端点。

### 12.5 E2E

新增真实 E2E：

- Worker HTTP；
- WebSocket Token；
- 本地 Go helper；
- SOCKS5/HTTP 代理；
- 远端 TCP 连接；
- MiSub manifest；
- EasyProxy connector；
- Token 错误和回退行为。

## 13. Aggregator 生命周期

### 13.1 运行定位

Aggregator 的推荐生产路径保持为：

- GitHub Actions 定时计算；
- Cloudflare R2 或其他对象存储承载产物；
- MiSub 消费 discovery export 和 stable subscription；
- EasyProxy 在 MiSub 失败时使用 stable fallback。

不把 Aggregator Python 程序伪装成 Cloudflare Worker 常驻服务。

### 13.2 安全发布

产物发布采用候选和稳定两层：

~~~text
releases/<run-id>/...
candidate/...
subs/clash.yaml
internal/crawledsubs.json
~~~

发布步骤：

1. 生成候选；
2. 验证格式；
3. 检查节点和来源数量异常；
4. 执行协议和连通性抽样；
5. 上传版本化产物；
6. 验证公开读取；
7. 切换 stable；
8. 保留上一 stable。

失败时不得覆盖最后已知可用产物。

### 13.3 Fork 用户配置

Fork 用户只需提供：

- GitHub Actions 权限；
- 目标存储凭据；
- 可选 GitHub Token；
- 非敏感调度和产物配置。

默认配置不得包含项目维护者私有域名或凭据依赖。

## 14. EasyProxy 本地发行和更新

### 14.1 唯一容器定义

合并 service/base 和 deploy/service/base 的容器职责，只保留一个正式产品镜像定义。

统一目录：

| 路径 | 职责 |
| --- | --- |
| /usr/bin 或 /usr/local/bin | 不可变程序 |
| /usr/share/easyproxy | 不可变资产 |
| /etc/easyproxy | 操作员配置 |
| /var/lib/easyproxy | SQLite、connector 和运行时数据 |
| /var/log/easyproxy 或标准输出 | 日志 |

镜像更新不得覆盖 /etc/easyproxy 和 /var/lib/easyproxy。

现有 EASY_PROXY_RESET_STORE_ON_BOOT 不得作为普通生产选项；若保留，仅允许
明确的维护命令并要求二次确认。优化后的配置和文档不得再引入无前缀别名。

### 14.2 Docker 发布

支持：

- linux/amd64；
- linux/arm64；
- 固定版本 tag；
- 镜像 digest；
- SBOM；
- SHA 或签名验证；
- NAS bind mount 权限检查；
- host 和 bridge 网络的明确支持矩阵。

生产部署禁止默认使用 latest，Update 选择明确版本。

### 14.3 原生发布

GitHub Release 至少提供：

- easyproxy-windows-amd64.zip；
- easyproxy-linux-amd64.tar.gz；
- easyproxy-linux-arm64.tar.gz；
- easyproxyctl 对应平台包；
- 示例配置；
- systemd unit；
- Windows Service 安装入口；
- SHA256SUMS；
- release-manifest.json；
- 更新和回滚说明。

### 14.4 本地安装

Linux：

- 下载并校验；
- 创建独立 release 目录；
- 创建 /etc/easyproxy 和 /var/lib/easyproxy；
- 安装 systemd；
- 检查端口；
- 启动并验证。

Windows：

- 下载并校验；
- 创建 Program Files 与 ProgramData 分离目录；
- 安装 Windows Service；
- 创建防火墙规则；
- 启动并验证；
- 保留 previous 版本。

NAS：

- Docker 为正式推荐路径；
- 提供 Compose 模板；
- 检查 bind mount、UID/GID、端口和网络模式；
- 不声称未经验证的原生 NAS 包。

### 14.5 本地更新

Update 流程：

1. 获取目标 release manifest；
2. 校验平台、架构和 SHA；
3. 备份配置和 SQLite；
4. 停止服务或启动候选容器；
5. 执行数据迁移；
6. 运行健康检查；
7. 切换 current；
8. 保留 previous；
9. 失败时回滚程序和数据。

Docker 回滚切换回旧 digest；原生回滚切换回 previous 目录。

### 14.6 配置所有权

更新时：

- 产品默认值可以更新；
- 用户配置不得无条件重新渲染；
- WebUI 修改必须进入明确的持久层；
- root topology 不得覆盖本地运行时用户数据；
- 替换配置必须使用显式 --replace-config；
- 所有配置写入必须原子化并保留上一版本。

## 15. 第一方代码复杂度重构

### 15.1 总原则

- 先用行为测试固定边界，再提取；
- 不做无行为收益的全文件重写；
- 允许删除旧 API 和兼容分支；
- 第一方调用方必须在同一 PR 或同一阶段迁移；
- 新文件正常不超过 500 有效行，绝不超过 700；
- 旧大文件只能做最小接线，拆分阶段结束后必须明显下降；
- 纯移动不算完成，必须形成明确职责和依赖方向。

### 15.2 Wave R1：管理 API 和兼容层

目标文件：

- monitor/server.go；
- monitor/server_test.go；
- monitor/proxy_service_compat.go。

目标边界：

- server 构造和路由注册；
- auth/session；
- 静态资产；
- node/status/probe handlers；
- Local Server handlers；
- lease/usage API；
- DTO 和序列化；
- 错误分类和反馈状态机。

如果 proxy_service_compat 仅服务旧协议：

1. 定义新的稳定 lease API；
2. 迁移所有第一方调用方；
3. 增加新 API E2E；
4. 直接删除旧兼容实现；
5. 不保留旧路由代理层。

### 15.3 Wave R2：配置系统

拆分 config/config.go：

- schema/types；
- defaults；
- YAML decode；
- validation；
- path resolution；
- secret references；
- source configuration；
- routing configuration；
- runtime derivation。

目标是建立单向配置流，删除反射式和隐式覆盖路径。

### 15.4 Wave R3：节点和运行时编排

拆分：

- boxmgr/manager.go；
- monitor/manager.go；
- builder/builder.go；
- outbound/pool/pool.go。

目标依赖方向：

~~~text
config
  -> builder
  -> runtime/node lifecycle
  -> pool/dispatch
  -> monitor read model
  -> management API
~~~

monitor 不再反向拥有构建、存储和全部运行时策略。

### 15.5 Wave R4：来源、连接器和持久化

拆分：

- subscription/manager.go；
- subscription/connectors.go；
- store/sqlite.go；
- local_server.go。

按以下边界拆分：

- source registry；
- refresh scheduler；
- ECH connector；
- ZenProxy connector；
- manifest merge；
- health/blacklist；
- repositories；
- schema/migration；
- device/profile management。

### 15.6 Wave R5：前端

拆分：

- ManagePanel.tsx；
- SettingsPanel.tsx。

将以下职责分离：

- API client；
- query/state hooks；
- form model；
- validation；
- save/CAS；
- error presentation；
- domain-specific panels。

前端不得继续用一个组件管理整个产品状态。

### 15.7 Wave R6：测试和部署脚本

- 测试按行为主题拆文件；
- fixtures 与 helpers 只保留共享构造；
- 不删除覆盖以换取行数；
- deploy-subproject.ps1 退化为薄入口或删除；
- Docker、GHCR、配置和 Cloudflare 逻辑进入统一生命周期实现；
- PowerShell、Bash 不再各自维护同一业务规则。

## 16. 实施阶段与工作项

### Phase 0：基线冻结

状态：已完成。

- P0-01 提交当前稳定代码；
- P0-02 推送 main；
- P0-03 创建并推送“优化前的稳定版本”；
- P0-04 记录基线 commit；

退出条件：标签可从远端解析到 1b555e6。

### Phase 1：上游 Fork 和子模块

状态：已完成（2026-08-29）。

- [x] P1-01 确认三个上游精确 commit 基线；
- [x] P1-02 创建或确认我们控制的 Fork；
- [x] P1-03 将当前本地增量迁入 Fork；
- [x] P1-04 为每个 Fork 创建 UPSTREAM.md；
- [x] P1-05 在根仓库逐个转换为子模块；
- [x] P1-06 所有 checkout 改为 recursive；
- [x] P1-07 修复嵌套子模块、Docker build context 和缓存路径；
- [x] P1-08 建立 upstream-sync PR 流程；
- [x] P1-09 删除旧 copy-sync 文档和脚本。

退出条件：

- 三个路径均为子模块；
- 根仓库无上游源码副本；
- 第一方 workers/ech-workers-cloudflare 仍保留在根仓库；
- CI、构建和部署在全新 clone 下通过；
- Fork 用户无需拥有子模块 Fork 即可部署；
- 根子模块和 Aggregator 嵌套子模块 URL 均为公开可读；
- 使用无额外 Git 凭据的全新 Fork 完成 recursive clone。

完成证据：根提交 7479a29 固定三个 Fork；公开 fresh clone 在
7479a29 上递归检出四个 gitlink（含 Aggregator manager），根 47 项测试、
Aggregator 12 项测试、MiSub 118 项测试与构建、ECH Go 全包测试均通过。
同一 fresh clone 的 service/base 与 ECH Docker 镜像构建通过；隔离
service 管理 API smoke 与 ECH help smoke 均通过且未替换现有运行容器。

### Phase 2：拓扑配置和生命周期核心

状态：已完成（2026-08-29）。

- [x] P2-01 写 topology schema；
- [x] P2-02 建立 topology.example.yaml；
- [x] P2-03 建立 Secret/Variable 权限矩阵；
- [x] P2-04 建立确定性资源命名；
- [x] P2-05 实现 easyproxyctl 基础；
- [x] P2-06 实现 topology validate；
- [x] P2-07 实现 resource discover/create-or-reuse；
- [x] P2-08 定义 deployment-manifest；
- [x] P2-09 删除旧 config 权威冲突和重复 renderer；
- [x] P2-10 建立 reusable workflow 骨架。

退出条件：

- 一份非敏感配置可以完全描述目标拓扑；
- 所有旧部署入口均有明确删除或迁移决定；
- 生命周期逻辑只有一个权威实现。

完成证据：`topology.schema.json`、`topology.example.yaml` 和
`docs/secrets-and-permissions.md` 定义非敏感拓扑；`tools/easyproxyctl`
统一负责严格加载、语义校验、确定性命名、Bootstrap/Update 调和规则和
带 checksum 的部署清单。清单只输出启用组件与资源，可记录 provider ID、
immutable image、根和递归子模块 commit。旧 renderer、GitHub 设置同步和
配置发布 workflow 已删除；`docs/topology.md` 逐项记录旧入口决定。被 Git
忽略的本地旧 `config.yaml` 可以保留作人工迁移参考，但没有活跃脚本读取
它。PR 验证由只读、无生产 Secret 的 reusable workflow 承担。

验证：easyproxyctl 19 项 Go 测试、`go vet`、真实 topology validate、真实
manifest build/verify、根 38 项测试、33 个 PowerShell 文件解析、Python
语法检查、workflow/schema 解析、Go 格式检查、release contract 与
`git diff --check` 通过。

### Phase 3：MiSub 安全部署和更新

- P3-01 引入 migrations；
- P3-02 修复完整导出；
- P3-03 实现加密备份；
- P3-04 实现临时 D1 恢复演练；
- P3-05 实现 create-or-reuse；
- P3-06 校验 D1 ID 和 binding；
- P3-07 实现 Bootstrap；
- P3-08 实现 Update；
- P3-09 实现 Verify；
- P3-10 实现 Restore；
- P3-11 修复 D1 Cron 持久化；
- P3-12 执行一次目标数据模型 expand 迁移；
- P3-13 在后续稳定发布中执行 contract 清理。

退出条件：

- 有数据的 MiSub 从基线升级后数据不丢失；
- 重复 Bootstrap 不改变数据库；
- Update 失败不产生空数据库替换；
- 优化前代码可以在 expand 后数据库上回滚运行；
- contract 清理后可以从备份恢复到对应旧 schema；
- Restore 在隔离环境演练成功。

### Phase 4：ECH Worker 生命周期

状态：实现完成，真实账户验收待 Phase 8（2026-08-29）。普通更新先证明
现有 Token 有效并拒绝隐式轮换；独立轮换工作流实现限时双 Token、候选
connector、server IP 默认保留、Worker/Go helper/EasyProxy 真实流量验证、
旧 Token 撤销和保留双 Token 的失败回滚。当前环境没有 Cloudflare、MiSub、
专用 EasyProxy 验证器凭据，因此尚未声明下列真实流量退出条件已通过。

- P4-01 固定 Worker 资源身份；
- P4-02 区分 bootstrap、update 和 rotate；
- P4-03 实现双 Token 过渡；
- P4-04 实现候选 connector；
- P4-05 保留 server IP；
- P4-06 实现 Worker + helper + EasyProxy E2E；
- P4-07 实现回滚；
- P4-08 完成 Go helper 子模块发布。

退出条件：

- 普通更新不轮换 Token；
- Token 轮换无未计划中断；
- overlap 时新旧 Token 均通过真实隧道，撤销后仅新 Token 成功；
- 旧 connector 只在新路径验证后移除；
- 实际代理流量 E2E 通过。

### Phase 5：Aggregator 安全发布

状态：代码实现完成、阶段退出条件尚未验收，真实 R2/MiSub/EasyProxy 故障演练
待 Phase 8（2026-08-29）。
根工作流现将上游输出隔离到每次运行的 candidate，完成格式、公开读取、节点与
来源下降门后发布 immutable release；stable 固定键逐对象验证，
`manifests/stable.json` 最后提交，失败时从发布前快照恢复。由于 R2 不提供跨对象
事务，这里的原子边界是 stable manifest，而不是声称所有兼容固定键同时切换。
MiSub 同步前绑定并校验 stable manifest，EasyProxy 模板使用 fork-neutral stable
fallback。当前环境未执行真实账户的 R2 写入失败注入、MiSub 同步和故障 fallback，
因此不提前声明这些 Phase 8 退出条件已通过。

P5-08 的“旧工作流删除”指根仓库不再保留或调用第二套旧发布入口；上游子模块
自身的 workflows 作为 fork 同步参考继续存在，但嵌套子模块中的 workflow 不会在
根仓库 Actions 中执行，也不属于根发布 authority。

- P5-01 完成子模块化；
- P5-02 定义用户 Fork 最小配置；
- P5-03 生成版本化候选产物；
- P5-04 增加空产物和异常下降门；
- P5-05 原子提升 stable；
- P5-06 保留 last-known-good；
- P5-07 验证 MiSub 同步和 EasyProxy fallback；
- P5-08 删除旧工作流。

退出条件：

- 失败运行不会破坏 stable；
- MiSub 可同步 discovery；
- EasyProxy 在 MiSub 故障时可使用 fallback；
- 用户 Fork 可独立调度和发布。

### Phase 6：本地跨平台发行（实现完成，真实环境验收待 Phase 8）

- [x] P6-01 合并 Docker 定义；
- [x] P6-02 统一持久目录；
- [x] P6-03 发布多架构镜像；
- [x] P6-04 发布 Windows 和 Linux 原生包；
- [x] P6-05 实现 Linux service 安装；
- [x] P6-06 实现 Windows service 安装；
- [x] P6-07 实现 NAS Compose；
- [x] P6-08 实现 backup/update/verify/rollback；
- [x] P6-09 更新 release-contract；
- [x] P6-10 建立 Release manifest 和 checksum。

实现证据：唯一 Dockerfile 使用 amd64/arm64，统一 `/etc/easyproxy`、
`/var/lib/easyproxy` 和 `/usr/share/easyproxy`；GHCR 启用 SBOM/provenance；
GitHub Release 工作流构建六个原生归档并生成 SHA256/manifest/attestation；
Windows 二进制实现 SCM Handler；Docker/Linux/Windows 生命周期均默认保留
配置并在更新前备份 SQLite。确定性打包、manifest 篡改、PowerShell/sh 语法、
Windows amd64 与 Linux arm64 交叉构建已在本地通过。

边界：上述为实现和静态/交叉构建证据，不等同于已在真实 Linux arm64、
Windows Service 和目标 NAS 上完成安装验收，也不等同于已发布真实 GitHub
Release；这些真实环境出口条件由 Phase 8 执行并留存证据。

退出条件：

- Linux amd64/arm64 Docker 通过；
- Windows amd64 原生服务通过；
- NAS Docker 通过；
- Windows arm64 明确标记为当前不支持，除非后续补齐同级安装和更新验证；
- 更新和故障回滚不丢配置和 SQLite；
- GitHub Release 包含真实可运行产品。

### Phase 7：第一方复杂度清理

状态：已完成（2026-08-29）。

- [x] P7-01 执行 R1 管理 API；
- [x] P7-02 执行 R2 配置；
- [x] P7-03 执行 R3 编排；
- [x] P7-04 执行 R4 来源和存储；
- [x] P7-05 执行 R5 前端；
- [x] P7-06 执行 R6 测试和脚本；
- [x] P7-07 加入有效行数 CI；
- [x] P7-08 删除所有已替代兼容层。

完成证据：R1-R6 分别由 `37d9c96`、`8930d9d`、`387f173`、
`2fc1b50`、`dff665f` 和 `d1402ab` 落地。初始 ratchet 中 22 个硬超限
例外已经降为 0；`scripts/check-effective-lines.py` 当前检查 427 个第一方文件，
只报告 6 个 500-700 行软警告。巨型 Go 测试按行为主题拆分，Python 审计入口和
PowerShell Local Server E2E 保持原入口并拆出职责模块；无调用方的旧文件存储迁移
实现已删除。`proxy_service_compat*.go` 名称保留，但其实现的是公开文档和第一方
审计仍在使用的当前 lease API，不属于已替代兼容层。

阶段验证：根 92 项测试、Aggregator 12 项测试、MiSub 126 项测试和构建、ECH
Worker Go 全包、EasyProxy Go 544 项测试（15 包）、`go vet`、带正式 tags 的 Go
构建、前端 52 项测试（17 文件）、ESLint、TypeScript/Vite 构建、actionlint、
release contract、Go 格式和有效行数门禁均通过。当前源码镜像还通过隔离的
Local Server 14 项 E2E，并验证 legacy 容器身份和状态未变化。

退出条件：

- 第一方生产文件无未说明的超过 700 行文件；
- 新文件正常不超过 500 行；
- 巨型测试按行为主题拆分；
- 旧兼容代码和重复部署逻辑已删除；
- 全仓验证通过。

### Phase 8：Fork 用户端到端验收

- P8-01 使用全新 GitHub Fork；
- P8-02 使用空 Cloudflare 测试账户；
- P8-03 只按公开文档配置 Secrets；
- P8-04 一键 Bootstrap；
- P8-05 安装一个 Linux EasyProxy；
- P8-06 安装一个 Windows EasyProxy；
- P8-07 验证 LAN 代理和管理 API；
- P8-08 制造 MiSub 更新失败并回滚；
- P8-09 制造 ECH Token 轮换失败并恢复；
- P8-10 验证 Aggregator last-known-good；
- P8-11 执行完整 Update；
- P8-12 收集测试资源清单并执行受保护的测试账户清理；
- P8-13 发布优化后首个 major release。

退出条件：

- 操作者无需读取源码；
- 文档步骤能够独立复现；
- 所有数据安全验收通过；
- 生产发布清单完整。

## 17. 测试矩阵

### 17.1 代码和契约

- 根配置 schema；
- 子模块 commit 和嵌套内容；
- Aggregator 单元和产物测试；
- MiSub 单元、构建和 D1 集成；
- ECH Worker 单元和 WebSocket E2E；
- EasyProxy Go 全包测试；
- 前端 typecheck、test 和嵌入资产；
- API schema 和 manifest contract；
- 行数、格式和 Secret 扫描。

### 17.2 Cloudflare

- 空账户 Bootstrap；
- 已存在资源重复 Bootstrap；
- 有数据 Update；
- 错误 database ID；
- 缺少 binding；
- migration 失败；
- Pages 部署失败；
- Worker 部署失败；
- Secret 缺失；
- Token 轮换；
- Restore；
- 自定义域名可选路径。

### 17.3 本地

| 平台 | 安装 | 更新 | 回滚 | 数据保持 | 代理 E2E |
| --- | --- | --- | --- | --- | --- |
| Linux amd64 Docker | 必测 | 必测 | 必测 | 必测 | 必测 |
| Linux arm64 Docker | 必测 | 必测 | 必测 | 必测 | 必测 |
| Windows amd64 Native | 必测 | 必测 | 必测 | 必测 | 必测 |
| Windows arm64 Native | 当前不支持 | 当前不支持 | 当前不支持 | 当前不支持 | 当前不支持 |
| Linux amd64 Native | 必测 | 必测 | 必测 | 必测 | 必测 |
| NAS Docker | 必测 | 必测 | 必测 | 必测 | 必测 |

Cloudflare 端到端测试资源必须使用专用测试账户和固定测试前缀，记录创建的
Pages、D1、Worker、R2 和路由。清理只能由测试账户专用、需要审批的 Teardown
执行；它不得出现在普通用户 Bootstrap 或 Update 中，也不得匹配非测试前缀。

### 17.4 数据样本

升级数据集至少包含：

- 普通订阅；
- 单个 proxy URI；
- ECH connector；
- ZenProxy connector；
- 公共和私有 Profile；
- customId；
- Cron Secret；
- 用户设置；
- Local Server 设备和 Profile；
- 路由规则；
- 已有 SQLite 节点和健康状态。

## 18. 发布和版本策略

### 18.1 优化版本是新的 major 系列

由于允许删除旧兼容层和旧配置，本轮优化按 major breaking release 管理。

发布说明必须明确：

- 不支持的旧入口；
- 删除的配置和 API；
- 数据迁移方式；
- 云端资源保留方式；
- 本地更新方式；
- 回滚边界。

### 18.2 Release manifest

每个正式版本记录：

- 根 commit；
- 三个子模块 commit；
- EasyProxy 二进制 checksum；
- GHCR digest；
- ECH Worker 版本；
- MiSub 版本；
- D1 schema version；
- Aggregator artifact schema；
- topology schema version；
- 最低可升级数据版本；
- 验证结果。

### 18.3 发布通道

- candidate：集成和升级演练；
- stable：完整 E2E 通过；
- last-known-good：发生故障时保留的上一稳定版本。

## 19. 回滚策略

### 19.1 代码回滚

- 优化前整体回滚：标签“优化前的稳定版本”；
- 优化后版本回滚：release manifest 指向的前一 stable；
- 子模块回滚：恢复根仓库记录的前一 commit。

### 19.2 Cloud 回滚

- MiSub：部署前一 Pages 版本并保持同一 D1；
- D1：优先使用 expand-only schema；必要时恢复加密备份；
- ECH：恢复旧 Worker 版本和旧 Token；
- Aggregator：恢复 stable 指针；
- R2：保留版本化对象，不覆盖唯一副本。

### 19.3 本地回滚

- Docker：切回旧 digest；
- Native：切回 previous release 目录；
- SQLite：必要时恢复更新前备份；
- 配置：恢复原子写入前版本。

## 20. 风险登记

| 风险 | 影响 | 控制措施 |
| --- | --- | --- |
| 子模块未初始化 | CI 和构建缺文件 | recursive checkout + 完整性门 |
| Aggregator 嵌套子模块 | 构建产物缺失 | recursive + 产物验证 |
| Fork 本地补丁遗漏 | 功能回退 | commit 级 diff 清单 |
| D1 绑定错误 | 看似数据丢失 | ID、名称、表和数据摘要四重校验 |
| Migration 失败 | MiSub 不可用 | 备份、expand-contract、候选部署 |
| Token 意外轮换 | ECH 全断 | update/rotate 分离、双 Token |
| 两套 Docker 路径 | 数据挂载错误 | 单一镜像合同 |
| 配置重新渲染 | WebUI 修改丢失 | 配置分层和显式 replace |
| 上游更新破坏合同 | 根产品故障 | Fork PR + 根指针 PR + E2E |
| 工作流 Secret 泄露 | 凭据失陷 | 最小权限、日志扫描、Environment |
| deployment manifest 丢失或篡改 | 更新目标不确定 | Cloudflare API 重建、校验和、人工确认 |
| 纯拆文件无收益 | 复杂度转移 | 职责和依赖验收 |
| 超大 PR 无法复核 | 回归风险 | 每阶段小批次和独立退出门 |

## 21. 阶段执行纪律

每个工作项必须：

1. 写清行为目标；
2. 列出受影响路径；
3. 先建立最小回归证据；
4. 只修改一个职责边界；
5. 运行最小聚焦测试；
6. 运行阶段级测试；
7. 检查有效行数；
8. 检查 scoped Git diff；
9. 更新计划状态；
10. 记录剩余风险。

不得：

- 在同一提交同时迁移多个高风险子模块；
- 在没有备份时修改 D1；
- 用新建空资源绕过资源解析错误；
- 以兼容层掩盖未完成迁移；
- 为通过行数门把紧耦合逻辑机械切片；
- 将测试失败解释为“历史问题”而继续发布；
- 将未验证的平台标记为支持。

## 22. 每阶段完成报告

完成报告必须包含：

- 阶段和工作项 ID；
- 修改的职责边界；
- 删除的旧实现；
- 数据迁移和备份结果；
- 测试命令和实际结果；
- Cloudflare 资源身份；
- 发布产物和 digest；
- 子模块 commit；
- 行数变化；
- 剩余风险；
- 回滚入口；
- 是否满足退出条件。

## 23. 第一批实施顺序

计划批准后，第一批只做基础设施，不立即重构运行时大文件：

1. 建立三个上游 Fork 的来源和补丁清单；
2. 确认 Fork 仓库存在并可推送；
3. 先迁移 upstreams/ech-workers 为子模块；
4. 修复递归 checkout 和构建；
5. 迁移 upstreams/aggregator；
6. 再迁移 upstreams/misub；
7. 建立 topology schema 和 easyproxyctl 骨架；
8. 建立 MiSub 备份与 migration 基础；
9. 建立新的 Bootstrap、Update、Verify 工作流；
10. 云端生命周期通过后再进入 EasyProxy 内部复杂度拆分。

这样可以优先实现用户最看重的：

- 上游可同步；
- Fork 后可部署；
- 更新不丢数据；
- 本地可安全安装和升级；

同时避免先在巨型运行时代码中展开长期重构，延迟真正的产品价值。

## 24. 最终完成定义

只有同时满足以下条件，本轮优化才算完成：

- 三个上游模块已成为我们 Fork 的子模块；
- 根仓库不再保存这些上游源码副本；
- 第一方新增代码全部受行数门禁；
- 普通用户只操作根仓库；
- Fork 用户能从空账户部署 Cloudflare 拓扑；
- MiSub 更新不丢数据；
- ECH 普通更新不轮换 Token；
- ECH Token 可以安全轮换；
- Aggregator 失败不覆盖 stable；
- Linux、Windows、NAS 至少各有一条验证过的部署路径；
- 本地更新可备份、验证和回滚；
- Release 包含真实可运行产物；
- 第一方硬超限文件完成职责拆分；
- 旧配置、旧脚本和旧兼容层已经删除；
- 完整端到端验收通过；
- 所有公开文档只描述新的唯一流程；
- release manifest 能够重现正式版本；
- 优化前标签和优化后 stable 都可以明确构建。
