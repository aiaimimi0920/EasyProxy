# MiSub 自动发现来源的保留与清理

仅处理 `kind=subscription` 且 `options.managed_by=aggregator_sync` 的来源。手工机场、Aggregator Stable、健康检查来源、运行时代理和 Connector 不在删除范围。

## 判断规则

- 上游 export 缺失：MiSub 只标记 `aggregator_missing`，不再直接禁用。
- 已停用或上游缺失的发现来源：由 `Maintain MiSub Discovery` 每 6 小时复查。
- HTTP 200 不是节点可用的证明。非 HTML 响应需通过独立 Docker EasyProxy 审核，至少一个节点完成真实代理探测才判定健康。
- 上游缺失但审核健康：保留；若此前由缺失规则停用，则重新启用。
- 连续两次确认无效的链接（404/410、非挑战型 HTML 页面），或连续两次完成节点加载但实际代理探测失败，才删除来源，并清理订阅组引用。
- DNS/TLS、403、429、5xx、验证码、审核容器构建/启动异常等均视为不确定，保留供下一轮复查。节点审核失败时还需检查宿主网络连通性，避免断网导致批量误删。
- 写入前重新读取生产数据，有并发编辑则中止。采用增量补丁，不上传或记录订阅密钥。

脚本默认只预览，实际写入需 `--apply`：

```text
python scripts/maintain-misub-discovery.py --apply
```

认证从环境变量 `MISUB_ADMIN_PASSWORD` 读取；`MISUB_PUBLIC_URL` 指定站点。运行需要 Docker、requests、PyYAML，节点审核复用 `easyproxy_source_audit.py`。本地运行用 `--work-dir` 将审核产物指向 `linshi`；完整审核产物可能含节点凭据，禁止提交或作为公开 CI artifact 上传。

## ECH 订阅组

NAS 使用 `aggregator-global`，不依赖独立 `easyproxies-ech-runtime` 组。ECH 部署与 Token 轮换统一使用 `--connector-only --attach-profile-id aggregator-global`，只更新受管 ECH 来源引用，不重建独立组。删除该独立组不删除 Connector 或 Worker。
