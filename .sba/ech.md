# ECH Worker 真实发布阶段

`ech.publish_ech` 是部署、升级、预升级动作共同复用的实际发布阶段，**不是完整生命周期
入口**。它不创建第四个常驻服务，不改变旧生产 Worker，不提交套件版本或释放 writer。
当前仍不提供可被 SpringBok catalog 接受的 manifest/deployment 声明。

## 输入与来源

调用方先从平台批准目标生成完整的 `kind/name/id` 资源列表，并用
`coordination.inventory_digest` 建立同一 Coordinator。摘要对资源按 kind/name 排序、使用
UTF-8 canonical JSON，不包含秘密、版本和运行时 URL；它不是 easyproxyctl manifest 的
整体 checksum，两者不可混用。ECH 阶段要求同一摘要、恰好一个同名同 ID 的 Worker，
以及恰好一个同名同 ID、等于共同控制 bucket 的 R2；账号来自持有许可的 identity。

`EchDeployment` 提供批准根 checkout、明确的 esbuild 二进制、全新树外构建目录、Worker
名称和上述清单。根和 gitlink 必须通过既有固定来源检查；构建前后都核验准确 SHA、origin
和 clean 状态。不把开发工作树的未提交文件当成已批准候选版本。

Windows/x64 与 Linux/x64 均固定 esbuild 0.28.2 的各平台二进制 SHA256，已联网匹配官方
元数据。新套件正式方向为 Ubuntu 24.04；`toolchain.prepare_linux_tools` 显式下载准确包、
校验摘要并赋执行权限，不使用浮动 npx。Linux 真实 bundle 已构建并在 workerd 验证；
这不是云端 ECH 业务验收，其他架构不在本阶段支持范围。详见 [toolchain.md](toolchain.md)。

构建子进程不继承 Cloudflare、GitHub、R2 或 MiSub 秘密。esbuild 实际输入必须恰好是
已审核的 edge、artifact router、原 ECH index 和 auth 四个源码文件；拒绝链接路径、树外
import 及新增输入。需要扩充 ECH 依赖时，必须一并审核并更新输入合同。

## 发布顺序和失败语义

1. 验证共同许可、资源摘要、真实批准源码及固定编译器，完成实际 ESM bundle。
2. 只读取账号已有 workers.dev subdomain；不存在则失败，不创建或更改账号级子域。
3. 首次部署/预升级拒绝已存在同名 Worker；升级检查旧 active SHA/version 的 ownership
   tags、兼容日期和准确 R2/公开版本/secret 类型绑定。不隐式导入旧资源。
4. 在首次 Worker 写入前，以 `If-None-Match: *` 创建私有
   `_sba/operations/<operationId>/ech-publication.json`。本进程同一 Permit 在尝试创建标记时
   即消费此阶段；标记 PUT 即使 UNKNOWN 也不可用同一 Permit 重试。标记保留后，另一个
   Coordinator 或重新构造的句柄也不能重入同一阶段。Permit 不支持跨进程恢复/序列化。
5. 单次 PUT 上传真实 multipart ESM、批准的私有 R2 绑定、公开版本绑定及 write-only
   `ECH_TOKEN` secret binding；不把密码写进 bundle、配置文件或阶段结果。
   同时保持原 ECH 配置的 `observability.enabled=true`，不因改用直接 API 丢失运行日志。
6. 回读 settings，核对 tags、兼容日期、日志开关、准确 R2 及所有绑定。Cloudflare 不回传 secret
   文本，只校验其名称/类型，不伪造秘密明文读回。
7. 单次 POST 开启此 Worker 的 workers.dev、关闭 Cloudflare 自动 preview URL，随后读回。
8. 在独立无 API token 的 session 中读取公开版本端点，最多 12 次只读传播轮询、间隔 5 秒，
   不因 URL 暂未传播而重发任何发布写入。要求准确候选 SHA/version；返回
   `ech-published`、`ech-bindings-verified`、`ech-version-verified` 和公开入口。

所有 API 请求固定 Cloudflare HTTPS endpoint、开启证书验证、禁止环境凭据链/代理、
redirect 和 SDK retry。网络/服务端错误、丢失响应、损坏返回或读回不符均失败关闭；不
重发、自动 rollback、删除资源或释放 writer。阶段标记、加密备份、控制记录不由产物
出口公开。未知状态恢复仍须先证明原执行器静止、核实实际资源，再单独审批设计。

归属列表/旧 settings 与 Cloudflare PUT 之间没有宣称原子条件替换保证。共同栅栏协调的是
采用本协议的发布者，不能阻止账号管理员绕过协议同时操作同一名称。此前生产流程没有
使用本协议，不能把此阶段的实现描述为已经消除了旧生产的所有竞态。

## 验证边界

Python 集成测试使用真实 Git fixture、真实固定 esbuild、真实 Requests multipart 和本机
HTTP 服务器；API 数据是明确合成的，不是真实 Cloudflare 写入。独立 S3 测试用真实
boto3/SigV4 发送条件阶段标记；共同 writer 没有被阶段代码释放或提交。

测试覆盖 deploy/update/preview 阶段、归属漂移、资源摘要不符、占位凭据拒绝、错误版本、
绑定错配、写响应丢失、redirect、UNKNOWN 重入、同阶段并发，以及树外构建输入拒绝。
真实 workerd 回归可通过 `EASYPROXY_TEST_EDGE_BUNDLE` 读取该阶段生成的 bundle；未设置
`EASYPROXY_TEST_ESBUILD` 时，Python 真实构建测试明确 skip，不能作为全部通过。

这些检查不等于 ECH 外网 TCP 业务、MiSub 消费、Aggregator 实际刷新或整套可用性验证，
因此不产生 `service-ready` 或 `suite-published`。五动作编排和独立业务验收仍必须完成。

## 本轮联网核实的官方资料

- [Workers multipart metadata](https://github.com/cloudflare/cloudflare-docs/blob/production/src/content/docs/workers/configuration/multipart-upload-metadata.mdx)：
  main module、R2/secret binding 与 tags。
- [Worker script API](https://github.com/cloudflare/cloudflare-go/blob/main/workers/script.go)、
  [settings API](https://github.com/cloudflare/cloudflare-go/blob/main/workers/scriptscriptandversionsetting.go)：
  上传、设置读回、write-only secret 和 observability。
- [script subdomain API](https://github.com/cloudflare/cloudflare-go/blob/main/workers/scriptsubdomain.go)、
  [account subdomain API](https://github.com/cloudflare/cloudflare-go/blob/main/workers/subdomain.go)：
  仅更改本 Worker 的公开开关，不创建账号级子域。
- [esbuild 0.28.2 官方 npm 元数据](https://registry.npmjs.org/esbuild/0.28.2)：固定 Windows x64 二进制摘要。

下载快照保存在 `linshi/easyproxy-sba-20261026/official-*`。这是接口与固定工具的资料核实，
不是对真实账号做发布验收。
