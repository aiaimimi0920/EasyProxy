# SBA 协调与产物安全边界

## 记录和状态转换

固定键 _sba/control-v1.json。记录只包含公开身份、资源清单摘要、随机 revision、已提交
版本、当前 writer 及 retired 状态，不含 API token、业务密码或订阅正文。

1. 第一次 deploy/preview 检查 bucket 为空，以 If-None-Match: * 原子创建 held 记录。
   不接管非空但没有该记录的旧 bucket；资源归属仍须调用方在任何业务写入前核验。
2. 已有实例只能以 If-Match: 原 ETag 将空闲记录改为 held。持久版本必须等于请求的
   previous；update 必须更高版本且 SHA 不同，refresh 必须与 active 完全相同。
3. writer 持有期间拒绝其他操作；没有 TTL、心跳抢占或后台回收。进程重启不会解锁。
4. 真实发布和动作要求的检查完成后，调用 commit_verified。它重新读取并核对完整记录、
   ETag、operationId，再以 If-Match 同时提交 active 和释放 writer。每次空闲也生成新
   revision，旧 ETag 不能因空闲记录重复而再次生效。
5. 预升级环境不允许周期刷新；正式环境不允许 destroy-preview。预升级 retired 是终态，
   协调模块的 retired 标记不是云资源清理回执；实际销毁另遵循生命周期的终结协议。

调用方必须在 begin 返回许可后才进行业务写入，且在 commit_verified 前确认所有远端
异步发布已经结束、所有子进程退出、所有要求的回读完成。不能只取得许可后继续后台
写入，也不能在 finally、取消处理或失败路径中释放。该模块不是任意代码/凭据的沙箱。

受限例外：同进程捕获的 refresh 在尚未尝试 promotion/sync、且
审计进程组已停止和准确容器已确认不存在时，可先保存不可变失败证据再以原 ETag 结束
writer；仅候选阶段失败也适用。未知终止、未知清理和发布阶段仍保留 writer，不能在
finally 通用释放，也不能跨进程接管。

## 失败语义

只有明确的 412 PreconditionFailed 视为已拒绝的冲突；网络中断、缺少 ETag、异常响应
或 SDK 错误均为安全 UNKNOWN。写入不重试，也不通过负读取认定之前未知的写入失败。
如果完成提交已实际生效但响应丢失，远端可能已经空闲且 active 已更新；调用方仍报告
unknown，不重放原动作。后续新任务只能按实际持久状态和平台审批继续。

S3 客户端使用显式凭据、固定 HTTPS 账号 endpoint、有限连接/读取截止、SDK 总尝试一次；
额外 before-send 栅栏阻止 SDK 区域重定向绕过普通 retry 配置再次发送控制 PutObject。
不接受环境中的 AWS session token 或 endpoint 替换，也不输出 provider 原始错误。

当前没有自动或手工解锁命令。未知操作的恢复必须先停止并证明原写入者已经静止，核实
云端实际状态和资源完整性，再设计独立审批恢复路径；不能删除控制键后直接重跑。

## 私有 R2 出口与 ECH 组合

新套件将同一个 ECH Worker 的入口设为 .sba/edge.mjs，并为它绑定 AGGREGATOR_STORE。
原 workers/ech-workers-cloudflare 入口和配置不变，没有新增常驻 Worker 或代理控制服务。
套件发布前还必须在 Wrangler 生成配置中明确绑定批准的 bucket。

公开产物的 base URL 可由批准的 ECH origin 加 /aggregator 得到，不需要用户预先取得
随机 r2.dev URL，也不需要公开整个 bucket 或增加 DNS 操作。其值不是新的外部配置入口。

公开路径是与现有根发布合同一致的七个固定 artifact，以及明确的 candidate、releases、
last-known-good 和 stable manifest 路径。internal/crawledsubs.json 是既有发布合同中
已公开且 verifier 必需的来源清单，继续按公开产物对待；不得将私密来源放入该公开清单。
原始 crawledproxies、_sba 控制记录、备份和任意新键不公开。未来新增产物必须单独审核
允许列表，不能改成整个前缀透传。

仅支持 GET/HEAD，不从请求拼接远端 URL，不复制对象的任意 HTTP metadata。稳定/候选
路径 no-store，固定 release 路径可 immutable 缓存。大小限制 64 MiB，错误只返回固定
文本，不泄漏 provider 异常。R2 ReadableStream 在 workerd 可能以流式传输返回，因此 GET
不保证 Content-Length；正确性以正文和内容摘要验证，不把该可选响应头当作业务成功。

/.well-known/easyproxy-release.json 仅在合法 SHA/稳定版本配置存在时返回公开版本信息，
不包含 ECH token；它不是完整业务健康证明。ECH 仍须完成独立 WebSocket/鉴权检查，
MiSub/订阅消费也须分别验收，不能拿普通 200 或版本元数据代替完整验证。
