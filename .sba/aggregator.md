# Aggregator 生成、审计与发布

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

周期刷新支持受限的前晋级失败终止分支：候选发布失败，或审计失败但其进程组
已确认终止、准确审计容器已确认不存在时，当前进程可先 create-only 保存 `refresh-aborted`
失败证据，再以原 Permit/ETag 条件写结束本次刷新。只允许 candidate/audit 阶段，保留所有
候选和历史证据，不改变 active、stable 或 MiSub，不将失败报告为成功。审计终止/清理未知、
已尝试 promotion、归属漂移或终止记录/CAS 未知仍保留阻断；不新增跨任务抢锁或自动重放。
只读消费者应以 manifest 指向的 immutable release 及其摘要为一致性依据。

## MiSub 受限管理同步

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
