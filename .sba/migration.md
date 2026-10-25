# 受保护的 MiSub 升级阶段

## 状态与责任

EP-SBA-04 当前实现的是可执行的备份/迁移阶段，不是五个 SBA 动作已经全部完成。
`migration.py:migrate_misub` 由整套升级的同一进程调用，必须传入已经持有的 update 许可。
该阶段不自行 begin、不提交 active、不释放许可，也不输出 SpringBok 成功结果。

根目录、三个 gitlink 可先由 `sources.py:materialize_workspace` 准备为统一可构建目录。
这里只使用批准对象，不读取开发工作树的未提交内容。工具与生成的运行配置应放在实例
专属工作目录，不能改写批准源或复用上次失败目录。

## 实际执行顺序

1. 核对持久 writer、候选根 SHA、manifest 中的 D1 身份和 Pages 身份。新标准套件当前仅
   接受与批准项目一致的 `https://<project>.pages.dev`，不将管理密码发到任意配置 URL。
2. 调用已有 `easyproxyctl cloud backup`。仍是原来的 scrypt age + tar.gz 格式，包含 SQL、
   logical-backup.json、deployment-manifest.json、backup-metadata.json，不另造归档格式。
3. 使用 R2 S3 的 `If-None-Match: *`，只创建
   `_sba/backups/<operationId>/misub.age`。不覆盖、不删除；SDK 总尝试一次，并额外阻止
   区域重定向/自定义 retry handler 引发第二次 PUT。任何上传未知都停止，不靠随后 GET
   认定此前未知的写入已经失败。
4. 上传明确成功后完整读回加密对象，比较实际字节数和 SHA256，再重新核对许可。
5. 调用 `cloud migrate --backup-input ... --expected-backup-sha256 ...`。CLI 先将输入归档
   单次读取到独立临时副本并同时计算摘要，写前匹配已持久化摘要，再解密这个副本，核对
   源身份、归档完整性和当前 D1 数据基线。之后即使原路径被替换，也不会更换使用的归档。
6. 只允许与两份审核 SQL 的准确 SHA256 匹配的迁移，逐条向固定 Cloudflare D1 query
   endpoint 发送一次。请求无可回放 GetBody、无 Idempotency-Key，不跟随 redirect；某条
   写入结果未知后不再执行下一条，不重发、不自动 restore。
7. 回读并比较所有原有非内部表的行、表/索引/触发器/视图定义。原有对象和行必须原样
   保持；新业务表必须为空，只允许审核的表/索引与准确的 schema version 1/2 记录。
   成功仅返回本阶段检查及加密备份引用，整套升级仍保持 held。

R2 本身保持私有，组合 ECH Worker 的公开产物路由不提供 `_sba/`。R2 回执只含私有键、
密文字节数/摘要和公开身份，不包含 SQL、业务配置、密码或订阅 URI。

## CLI 使用边界

现有 `cloud update` 仍然只负责资源解析，不静默改变旧命令语义。新增命令示例：

```text
easyproxyctl cloud migrate
  --topology <instance-topology.yaml>
  --manifest <deployment-manifest.json>
  --wrangler-config <runtime-wrangler.json>
  --wrangler-command <absolute-node.exe>
  --wrangler-prefix <absolute-wrangler.js>
  --wrangler-dir <prepared-root>/upstreams/misub
  --migrations-dir <prepared-root>/upstreams/misub/migrations
  --account-id <approved-account-id>
  --confirm-database-id <approved-d1-id>
  --backup-input <retained-local-copy.age>
  --expected-backup-sha256 <sha256-read-back-from-private-r2>
```

独立运维还可使用互斥的 `--backup-output <new.age>`，由 CLI 自行创建本地备份；**SBA
不得使用这个模式替代远端持久备份**。输入归档模式必须提供准确摘要，输出模式禁止提供
该参数。需要逻辑导出时可提供 `--base-url`；直接 D1 逻辑导出仅在显式
`--allow-direct-d1-fallback` 时启用。秘密仍从 topology 引用的环境变量读取，不作为参数。

Go CLI 的 Wrangler prefix 当前用 strings.Fields 拆分；SBA 阶段对含空白的脚本路径
明确拒绝，不能偷偷调用 shell 或浮动 npx 下载。Wrangler 这里只用于读取身份、快照和
导出，不负责迁移写入。Linux Go 1.27.3 和应用构建来源现已固定；精确 Wrangler 仍来自
批准 MiSub 锁文件的隔离安装。正式入口仍待串接，通用 runtime 不代替应用工具链。

## 数据与兼容性

备份 metadata 增加可选 `preservation_sha256`，归档 schemaVersion 不变。旧归档仍可走
原来的读取/恢复路径；**新迁移拒绝缺少此完整基线的旧归档**，应先创建新备份。
原来的 canonical snapshot 仍保留，且迁移前同时比较它与完整 preservation digest。

完整检查覆盖非内部表的全部行，而不仅是 MiSub 的 canonical main/last 行；明确排除
SQLite 保留的 sqlite_ 对象（但保留有业务状态的 sqlite_sequence）与 D1 的 _cf_KV。
未知自定义表不能因为 _cf_ 前缀而被忽略。
对象数和读取数据量有界，超限/不支持的数据类型应失败，不截断后冒充完整校验。

目前审核的迁移仅为 0001_initial.sql、0002_cron_executions.sql，内容都是追加式创建与
版本记录。未知 SQL、额外文件、symlink、修改过的迁移在任何迁移写前被拒绝。未来新
迁移必须显式审核并扩展代码政策；这不是宣称支持任意 SQL 的通用“安全解析器”。

持久 writer 只约束使用共同入口的写入者。它不会冻结管理员、旧 workflow 或其他持有
凭据的程序；多次只读比较也不是数据库事务快照，不能保证检测先改后还原的外部写入。
因此仍不导入旧生产实例，不把这个阶段称为旧生产竞态已解决。

## 验证与尚未完成

Go 回归实际使用 SQLite 创建已有业务与 Unicode 自定义数据，生成/解密原格式 age 归档，
执行审核迁移并恢复到新库比较。还覆盖过期备份、合法归档被另一合法归档替换、备份失败、
响应丢失、数据保持失败、禁止重发/自动恢复。SBA 阶段用真实 boto3/SigV4 对接本机合成
S3，证明先持久备份再调用迁移、条件写、未知结果保留 writer，以及缺少整套完成检查时
不能提交。它们不是外网 R2/D1/Pages 生产验收。

证据在 `linshi/easyproxy-sba-20261026/`。Pages/Worker 实际发布协议阶段现已实现；接下来
仍须串接正式入口、Aggregator 实例工作流与批准 SHA 调度、独立无秘密 verify、预升级
副本/副作用隔离及批准清单销毁。
旧同步脚本的写重试、推广脚本的内部重试/自动回滚在完成审查适配前，不得接到 SBA 路径。
五动作完整可执行之前，仍不增加 manifest.json/deployment.json 或可被 catalog 接受的空壳入口。

## 首次部署：空 D1 初始化

`d1_init.initialize_d1` 是独立于 update 的首部署阶段，只接受持有的 deploy/preview 许可、
尚无 active release 的状态及准确资源清单。核对账号和 D1 uuid/name 后，拒绝任何已有用户
schema；只排除 SQLite 内部命名空间和准确 `_cf_KV`，不忽略任意 `_cf_` 开头自定义表。

阶段使用同两份准确迁移字节，在本地 SQLite 推导预期 schema，首次写入前持久 claim
`database-initialization`。共 11 条 SQL 逐条单次发送；任何未知结果停止、不重试。最后读回
完整 schema、准确两条 migration history、四张空业务表和远端身份。不会借用 `/api/data`
或 KV-to-D1 migrator 的隐含副作用来初始化，也不提交套件版本或释放 writer。

测试实际执行本机 SQLite SQL，并覆盖非空库/身份漂移/篡改迁移/UNKNOWN 后禁止重入；
尚未在真实 Cloudflare D1 上执行该初始化阶段。
