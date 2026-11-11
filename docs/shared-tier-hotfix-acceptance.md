# 共享策略热修验收与上线准备

## 状态

运行时代码提交：`c3ae1585c8e23015a6f30ccf778e4f3ea0bae609`。
隔离 Linux 验证 ELF SHA-256：
`7ee872848688866c63987f063e32bbeb414cbf877ed8628cdf22966811b58ef2`。
该 ELF 在提交前以相同源码构建，VCS 元数据仍显示基线加 dirty；不要把元数据当成代码不一致的证据，也不要用另一产物替代后省略复核。

独立 Linux 验证通过原 82 条 TUN 流、7 条行为断言及新增 168 条 legacy 共享策略流。
新增覆盖 HTTP CONNECT、SOCKS、TUN TCP/UDP、IPv4/IPv6、多源、US/JP/清空、回退、跨 API CAS。
隔离容器与网络已经清理，主机路由、规则、nft 基线未变。该测试没有真实注入生产 reload 失败。
Go 全包、pool/gatewayroute/app/boxmgr/monitor race、vet、格式与有效行数检查通过。

## 完整 PUT 与兼容性

`/api/local-server/profiles/shared` 的 PUT 是完整 Profile 替换。
省略 `selection_tiers` 与显式 `[]` 都清空策略，此行为已在原功能文档规定，不是本次热修的新行为。
设备 Profile 完整 PUT 使用相同替换模型。
只有 `/api/routing/config` 兼容入口在省略新增字段时保留已有策略，显式 `[]` 才清空。
旧完整 Profile 编辑器若只序列化其认识的字段，会清除新增策略；不能称其完全向后兼容。
完整 Profile 客户端必须 GET 当前定义后修改并 PUT，携带当前 revision；过期 revision 返回 409。

## 回滚测试边界

boxmgr 单测在 legacy 和 Local Server 两种模式均向真实 reload 事务代码注入候选提交失败，
使用 fake managed box 验证旧 revision/tiers 恢复、候选 admission 关闭、恢复后的 admission 开启。
这是事务故障注入单测，不是原生 TUN、真实 sing-box 启停或生产断网演练。
若需要真实隔离 reload 故障演练，可在现有隔离 fixture 中使用本地监听端口冲突使候选启动失败，
核实服务恢复及 US 配置修订，再通过既有三入口流量矩阵；不得在生产制造端口冲突。

## 候选镜像与部署顺序

1. 可在开发机以已验证的当前 runtime 镜像为基础，仅替换上述已测试 ELF；本修复不改前端或依赖。
   记录 base 镜像、source commit、最终镜像 ID、导出 tar SHA-256，以及从候选镜像实际提取的 ELF SHA-256。
2. 将候选镜像放入隔离测试，至少复核启动、共享策略 API、HTTP/SOCKS/TUN；不得仅凭 build 成功上线。
3. 另外准备可信来源、有许可的 Country MMDB，验证真实 reader 可读、country.iso_code/names.en 字段及数据日期。
   地区来自节点服务器 IP，不承诺实际公网出口国家。不得把测试 fixture 的标签带到生产。
4. 上线前提出具体确认：替换哪一个 NAS gateway 容器、启用哪份 GeoIP 文件、共享一级 US（其余策略保持），
   对现有连接的影响、维护窗口及失败回滚；不能顺带切换 Linux 系统代理。
5. 确认后备份当前 compose/config/database 及镜像引用，单实例替换，避免两个 TUN 容器并行。
   允许启动预热充分完成后检查就绪、真实 US 标签可用数量及共享 revision，验证显式与透明代理。
6. 任一验收失败恢复旧镜像与对应配置/数据库快照，比较 nft/route/rule，并验证原管理通道。
7. Linux 从 FlClash 切换是独立后续动作：先验证 Windows 台式 SSH 中转及自动回滚，保留 FlClash、DNS、Tailscale，
   再就具体系统网络修改取得确认。当前不得提前更改 Linux 网络。
