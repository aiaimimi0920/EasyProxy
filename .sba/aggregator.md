# Aggregator 发布阶段与剩余接入

## EP-SBA-07A（负责人：主 AI）

本切片增加本地生成适配、真实 EasyProxy 审计调用及 R2 候选/稳定发布；不是完整 Actions
实例管理，也不是已上线的五动作。仍无 manifest/deployment/成功空壳入口。用户已授权最终
真实上线与旧部署迁移；删除旧资源仍须准确清点、备份并取得明确“同意”，不扩大删除范围。

## 已实现路径

1. `aggregator_build.py` 检查批准根及准确 Aggregator gitlink，在源码外全新目录复制运行。
   所有 storage item 改为固定文件名与绝对本地目录；不继承 R2、Cloudflare、GitHub、MiSub
   管理凭据、代理或 Python 注入变量。仅 materializer 单独取得批准的种子凭据，日志私有。
   显式空 `.env`、关闭 update/remark、拒绝注册域名和 scripts/repositories/pages 插件。
   保留批准的公开采集频道，不以永久关闭采集来冒充迁移功能等价。
2. 原 upstream `PushToLocal` 生成 Clash/sing-box/mixed 与公开 crawledsubs；V2Ray 调用根仓
   已有纯转换，不进入其 R2 main。原始 crawledproxies 不发布，不修改任何独立 upstream。
3. `publish_candidate` 取得共同许可与持久单次阶段标记后，create-only 上传准确 operation
   的候选；每个对象完整 S3 回读，并与同一 ECH 公开出口正文核对。
4. `aggregator_audit.py` 调用批准根的真实 EasyProxy 消费审计，将 operation、全部候选
   SHA-256、准确 gitlink、固定 image ID/source/platform 和审计正文绑定。promotion 要求
   同一候选 Clash URL、成功消费探针、已确认容器清理，以及 operation/digest/gitlink
   全部一致，不能借用别的运行或 URL 的审计结果；消费阶段禁止隐式 build/pull。
5. `promote_candidate` 先检查原 stable 的版本、全部 immutable bytes/hash/size，遇漂移
   不自动修复。保留节点/来源最低值与原有 60%/80% 跌幅门；写新 immutable release、旧
   LKG，再条件更新 canonical stable，最后写 stable manifest。旧 release 从不覆盖或删除。

`artifact_store.py` 复用 `S3Store._put_once` 的固定 HTTPS endpoint、显式凭据与 SDK
单发送栅栏；新对象 `If-None-Match: *`、更新 `If-Match: 原 ETag`。JSON/YAML/文本元数据
明确传递；没有 `upload_fileobj`、写重试、重定向、删除、自动 rollback 或解锁。

**多对象 canonical 更新不是原子事务。** 发生中断时可能已有部分 stable bytes 改变而
manifest 仍旧，必须报告 UNKNOWN 并保留 writer，后续不得重放或把负读取当恢复授权。
只读消费者应以 manifest 指向的 immutable release 及其摘要为一致性依据。

## 验证边界与下一步

实际执行了批准 gitlink 的原 `PushToLocal`：网络连接被禁止，全部八个 item 均本地写入
并读回。发布测试使用真实 boto3/SigV4 连接本机合成 S3，覆盖条件头、内容元数据、错误
ETag/body、响应丢失及恶意 SDK 重发 handler；不是对真实 R2 的发布验收。

已完成固定 Linux 工具/28 个 Python wheel、真实 venv、准确 gitlink 的两个原生工具摘要与
实际离线转换，以及原上游 `http_get` 的准确版本化 TLS 最小补丁。关闭 group regularize，
拒绝进入浮动 MMDB 下载路径；不修改独立 upstream。源码抓取、探针目标与节点协议的 TLS
是三层边界，不能把源码 HTTPS 已验证等同于每个采集节点都启用了 TLS；完整说明见
[toolchain.md](toolchain.md)。整个联网采集与真实 R2 publication 尚未执行。

真实固定 ID 的最小审计镜像已构建，本机测试订阅成功经过真实 EasyProxy 节点、租约与
探测，并确认容器清理后独立查验其不存在。该镜像只负责订阅消费，不包含完整 UI、CFST
或 cloudflared/ECH connector 能力，也不是已发布产品镜像。

下一块是实例 GitHub environment/secrets/vars 与批准 SHA 的定时入口，然后串联五动作、
独立 verify、preview 数据副本/源不变
证明和准确清单销毁。当前没有改旧生产定时任务，旧任务还未使用新共同栅栏。

## EP-SBA-07B：MiSub 受限管理同步

`misub_sync.py` 复用同一候选和真实审计校验，读取无迁移/Cron 副作用的 backup export 与
settings，在双快照及 Permit 所有权检查后只发一次受限保存。响应不明不重试，保留 writer。
profile 无变化时仍显式绑定已有 aggregator-global，第二次 refresh 不会因空 diff 误拒绝。

新增 Pages app-owned `/api/sba/managed-sync`，复用原登录 Cookie、同 Origin 和准确
sourceSha 校验，仅允许 `proxy_runtime_node_<n>` / `conn_ech_workers_pref_<n>` 及唯一
aggregator-global profile。直接调用原 D1 adapter，避免原保存 handler 即使面对 disabled
订阅也会重探测全部旧来源的副作用。请求逐 chunk 限制 16 MiB，超限取消而非全量读取后拒绝。

保留未管理节点、未改 profile、自定义字段、可见性、到期值及非管理 connector。Python 的
settings 证据是 sanitized export，不宣称读取了原始 settings 全表；实际 workerd 测试另行
对比原表和完整用户行。多次 D1 按 ID 操作不是原子 CAS，外部管理员在比较后直接修改仍可能
竞争；途中失败 UNKNOWN，不自动恢复或解锁。历史非数字管理 ID 不取得本次删除权限。

2026-10-26：准确 b14 根和三个 gitlink 的隔离构建生成 99 个资产与 Functions bundle，
bundle SHA-256 `80f84646f94acf8b8a2f618bb0b8361c740ba84b9321a8675043733cc349a9a6`。
此最终 bundle 的真实 workerd 与流式请求测试共 11 项通过、无 skip，包含第二次同步、
auth/Origin/source/scope 负例、原始数据保持且 outbound 为空。证据 `real-build-18.safe.json`
与 `workerd-19` 位于 linshi 对应 easyproxy-sba-pages/aggregator-20261026
目录。额外固定容量缓冲消除按输入 chunk 对象累积的内存放大，13 万个单字节输入回归通过。
完整 Python `all-17.xml` 为 203 passed、无 skip；这批 Python 使用 build-15，最终 body 修正
另由 build-18 的上述 Node/workerd 验证覆盖。没有云写入。
