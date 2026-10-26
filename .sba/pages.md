# MiSub Pages / Functions 发布阶段

## 可执行边界

`pages.py::publish_pages(owner, permit, PagesDeployment(...), api_token=..., secrets=...)`
消费同一个 Coordinator 的 deploy/update/preview 许可与完整资源清单，实际构建并通过
Cloudflare Pages Direct Upload API 发布 MiSub。它只返回以下阶段证据：

- `pages-published`
- `pages-bindings-verified`
- `pages-version-verified`

它不提交套件版本、不释放 writer，不返回 `suite-published`、`service-ready` 或 SpringBok
成功回执。此前的加密备份/D1 迁移是另一个阶段，尚待五动作编排强制串接；不能单独调用
本阶段就宣称已完成保数据升级。没有导入、覆盖或迁移现有生产 Pages 项目。

首部署还必须先调用 `d1_init.initialize_d1`，不能假设平台新建的空 D1 已有表，也不能以
公开版本路由 200 证明 schema 已初始化。该阶段仅接管空用户 schema，见
[migration.md](migration.md) 中的首次部署段落。

## 来源、构建与依赖

`pages_build.py` 检查批准根提交和准确 MiSub gitlink 的 HEAD/origin/dirty/ignored 文件及
skip-worktree 状态，将普通文件复制到源码之外的全新目录。构建前后复核来源，不在开发
工作树或独立 upstream 中安装依赖、写配置或输出构建产物；链接和解析到树外的路径拒绝。

当前要求 Node 22 系列、npm **10.9.7**，以及批准 lockfile v3 中的精确包：Wrangler
**4.93.1**、Vite **7.3.3**、blake3-wasm **2.1.5**。其余依赖同样使用原锁文件中的 tarball
与 SHA512 integrity，通过 `npm ci --ignore-scripts --strict-ssl=true` 安装，不重新解析版本、
执行安装脚本或使用浮动 npx。上游历史 npmmirror URL 只在构建副本改为相同路径的官方
npm registry，**版本和 integrity 不变，原锁文件不改**；未知来源、本地链接或非 SHA512
依赖拒绝。Linux 应用工具链进一步固定 Node 22.23.3 发行包与 SHA256，并单独固定 npm
10.9.7；不依赖通用 Node 22 合同的浮动补丁。真实 Linux 构建已完成，详见
[toolchain.md](toolchain.md)。

安装和构建使用独立 HOME、npm 配置与缓存，采用环境白名单；不继承 Cloudflare API token、
R2 密钥、GitHub token、业务秘密、NODE_OPTIONS、NODE_PATH 或用户的 npm 认证配置。
复制出的原 Wrangler 配置保留为 `.source`，不作为本次构建配置加载。Wrangler 的唯一用途
是本地 `pages functions build`，保留其官方 Functions 路由和 MiSub 原中间件，不执行任何
远程 Wrangler 命令。仅在副本中添加根 `_middleware.js`，提供不读 D1、不含秘密的
`/.well-known/easyproxy-release.json`，其余路由继续原来的 `next()` 链。

Functions 产物是官方生成的完整嵌套 multipart `_worker.bundle`，不是原始 JS 或 ZIP。
检查 metadata、唯一 ESM 主模块、MIME、体积上限，且禁止内层 `metadata.bindings`，避免
覆盖项目端绑定权威。当前只接受全部打包为单一 ESM 的 MiSub 产物；未知额外模块失败关闭。
`_routes.json` 必须覆盖 `/*` 且没有排除项。Worker 源码和特殊控制文件不作为静态资产公开。

## 项目归属和配置

清单必须恰好包含一个 Pages、一个 D1，以及与 Coordinator 身份一致的私有 R2。Pages
目标 ID 按 SpringBok 合同使用项目名，D1 使用准确 UUID。首次部署仅创建不存在的同名
direct-upload 项目，遇到已有项目即拒绝，不隐式接管。update 必须匹配旧 active 版本、
完整清单摘要、生产分支、D1、变量键集合和 canonical deployment；不接受 Git 自动构建源。

生产分支固定 `main`，compatibility date 为 `2026-03-25`，关闭 latest-date 自动漂移和
fail-open，拒绝额外 KV/R2/service/queue 等绑定。生产配置包括：

- `MISUB_DB` 指向批准 D1；
- 当前项目的 `pages.dev` public/callback URL、环境名、项目名与 D1 身份；
- SBA 清单摘要、根 SHA、应用版本；
- `ADMIN_PASSWORD`、`COOKIE_SECRET`、`MANIFEST_TOKEN` 三项 `secret_text`。

三项业务秘密必须显式提供，不接受 SpringBok verify 的占位值；当前要求非空、长度不超过
512 的无空格 ASCII 字符串。secret 读回仅核存在性和类型，**不声称读取了明文或完成真实
登录验证**。正式独立 verify 的业务验收仍需后续补齐。

SBA preview 将使用另一套独立 Pages 项目的 production 环境；每个项目附带的 Pages preview
环境无业务秘密和存储绑定。这里的配置隔离并不等于 SBA preview 的数据副本、源不变证明或
销毁流程已经实现。当前只发布标准 `pages.dev` origin，不接管旧自定义域名。

## 单次写入与未知结果

首次 Pages 写入前，调用 `claim_stage(permit, "pages-publication")` 创建不可覆盖的私有 R2
阶段标记。即使同一持有者重入或并发调用，也只能尝试一次。所有业务写入前继续核验共同
许可；持久记录和 marker 在失败时保留，不因 HTTP 错误、丢失响应、超时或取消自动解锁。

账户 token 只用于项目/部署 API。资产接口使用该项目 GET upload-token 返回的临时 JWT。
hash 精确采用 Cloudflare 的 `BLAKE3(base64(file bytes) + extension without dot)` 的前 32
个 hex 字符。先check-missing，按缺失hash单次上传，再单次upsert-hashes，最后发送
唯一一个创建部署multipart POST。匹配固定Wrangler4.93.1的官方上传顺序，不把
check-missing当成未登记资产的即时读屏障；登记前不再要求第二次查询立即为空。
上传/登记的ACK不代替后续部署、project和公开版本验证。JWT到期不刷新或重放写入。

共用 `cloudflare_http.py` 的固定 HTTPS endpoint、TLS 校验、环境凭据隔离、响应大小限制、
重复 JSON 键拒绝、重试 0、重定向关闭。即使通常被官方 CLI 当成可重试/可忽略的资产缓存
写入失败，这里也停止，而不是带着未知状态继续发布。错误只暴露固定安全码，不输出
provider 正文、业务秘密或原异常。构建日志仅留在隔离输出目录，不进入公开回执。

POST 返回 deployment 并不算完成：有界 GET 轮询必须观察到 `deploy/success`，核对项目
ID/name、production、Functions、branch、根 SHA，再读取项目 canonical deployment 和
公开版本。API 状态轮询最多 36 次、公开传播最多 12 次，间隔 5 秒；这些是只读轮询，
不会重新发送发布。任何失败都不自动恢复、回滚、删除或释放 writer。

Cloudflare 项目 PATCH 和部署 POST 之间没有本接入可用的配置 CAS；不宣称这两步原子化，
也不能阻止账号管理员绕过共同锁。阶段标记之后的异常会留下需要人工核对的部分状态，
当前没有提供可跨进程恢复或自动重放的入口。

## 验证与事实边界

官方 API/SDK/Workers SDK 源码已联网核对，副本位于
`linshi/easyproxy-sba-pages-20261026/`。本轮仅使用本机合成 provider 验证 HTTP 写入协议、
token 隔离、归属漂移、丢失响应、错误/重定向、并发重入、状态传播与失败关闭；未向真实
Cloudflare 账号发送写入。真实源码、npm 安装、Vite/Functions 编译和本地 workerd 另有
独立证据，不能将这些结果表述为已在云端上线。

测试需设置 `EASYPROXY_TEST_PAGES_BUILD` 指向实际完成的隔离构建目录，否则真实产物的
Python 测试明确 skip。运行 `.sba/tests/pages.workerd.test.mjs` 还需指定
`EASYPROXY_TEST_MINIFLARE` 和 `EASYPROXY_TEST_ARTIFACT_ROOT`；workerd 使用同一实际 bundle、
实际前端和本地 D1，显式拒绝外部网络，验证版本 GET/HEAD/405、前端、原登录/签名 Cookie、
原 D1 数据与跨源写拒绝。所有测试产物放到 linshi。
