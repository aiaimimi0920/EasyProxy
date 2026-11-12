# EasyProxy 标准套件生命周期与调度

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
回执只记录固定白名单阶段错误码，不含内部异常或 provider 响应正文。
诊断仅返回固定白名单错误码，不能代替真实部署或业务验收。

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

## 受限恢复

代码中的 repair 入口不是通用重试。各入口仅接受其声明绑定的准确父任务、版本、
请求/结果摘要、资源身份、control 字节/ETag 与阶段对象。执行前必须重新核查现场，
任何漂移都停止；不得复用历史批准、删除控制键、重放初始化或把 UNKNOWN 改写为成功。
持锁移交必须先保存不可变证据再进行单次条件写，旧 writer 不凭超时释放。
具体允许入口和前置以当前 manifest 及对应 repair 实现为准，历史一次性入口不可泛化。

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
更新遇到索引缺失会拒绝，不擅自重新启用。共同锁不能约束未接入它的外部写入者。

两个历史私有种子字段可使用准确值 `sba-disabled-source-v1` 显式停用。此值不是密码或 URL，
会在调用 materializer 前移除，使对应缺失私有种子禁用；公开种子和公开 crawl 仍运行。
这样套件可不依赖旧 seed relay。没有可恢复明文的旧加密 secret 不伪造为已提取，
也不为维持它另建长期服务。产物仍须至少有真实可消费节点并通过相同审计，停用不绕过验收。


## 测试条件

Python 真实构建测试需提供摘要匹配的 `EASYPROXY_TEST_ESBUILD`、真实产物
`EASYPROXY_TEST_PAGES_BUILD`、sealed-box helper `EASYPROXY_TEST_SEALER`；否则相关用例会 skip。
workerd 需 `EASYPROXY_TEST_MINIFLARE` 和源码外 `EASYPROXY_TEST_ARTIFACT_ROOT`。
测试依赖不进入产品；本地合成 provider 或 fixture 不能证明真实云端已验收。
