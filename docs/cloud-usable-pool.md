# 云端可用池与本地 connector

## 目标与边界

普通订阅和 agg 采集节点在云端汇总并接受同一轮 EasyProxy 运行时审计。
NAS 使用的 `aggregator-global` manifest 只包含审计通过的实际代理 URI，以及选中的
ZenProxy/ECH connector 参数；不包含普通订阅的原始地址。

来源记录仍在 MiSub 独立管理。`kind=subscription` 且 `enabled=true` 的来源每轮都会读取，
与是否被某个普通订阅组勾选、上次格式探测是否成功无关。禁用或删除来源后，下一轮成功
刷新不再带入该来源的旧节点。现有 HTTPS 来源使用其 `customUserAgent`（默认 `clash.meta`）。
云端输入只接受无 URL 用户名密码、无 fragment 的公开 HTTPS 地址，重定向逐跳校验且最多三跳。

## 一条生成与发布链路

1. 正常 update 和周期 refresh 从准确项目 `pages.dev` 的备份导出接口读取来源登记；不调用
   有迁移/Cron 副作用的 `/api/data`，不把管理密码交给第三方订阅或 upstream 子进程。
2. 每个普通订阅重新下载到私有生成目录，再交给批准的 agg 解析/转换器。每个来源有单独的
   本地转换产物，检查它确实贡献了候选节点，不能以 HTTP 200 或整批 agg 成功掩盖漏源。
3. 所有普通订阅候选与 agg 候选合并、去重，交给准确版本 EasyProxy 镜像进行真实消费审计。
   下载成功、格式正确和转换成功均不代表节点可用。
4. 只有审计返回的 `stable_available_uris` 才进入 `subs/effective.*` 和 MiSub Global 托管节点。
   Global 的 `subscriptions` 固定为空；普通订阅 URL 不下发 NAS。
5. ZenProxy/ECH 不在云端预先展开为静态节点。Global 下发它们的 connector 参数，NAS 自行
   取节点或建立连接，并与所有普通代理节点一起接受本地健康检查和故障隔离。

原始候选格式产物用于生成/审计和兼容既有产物合同，不是 NAS 的可用池入口。客户端应使用
Global runtime manifest 或 `subs/effective.txt` / `subs/effective.json`，不能把 raw Clash
候选列表当作可用性承诺。云端可用与 NAS 所在网络可用仍是两个独立结论。

## 轮换与失败语义

每轮 Clash 候选附带不含订阅 URL、密钥或源内容的用户节点摘要。下一轮继承 agg 候选时，
先剔除这部分用户节点，再使用订阅的本轮内容；因此每日轮换、来源禁用/删除不会把旧订阅
节点经 `remains` 缓存继续带回。摘要不是健康结论，任何继承的 agg 节点也必须重新审计。

下载失败、空响应、某个来源没有可解析候选或转换后未进入合并池时，本轮生成明确失败，
不以空源伪装成功、不发布半批新池。生成在获取发布 writer 之前完成，因此这类失败保留
上一成功发布的池，不占用新的持久 writer。已进入发布阶段的 UNKNOWN 继续遵循原有停写规则。

## 周期

现有 GitHub Actions `sba-aggregator-refresh.yml` 每六小时运行一次（UTC `25 */6 * * *`；
北京时间 02:25、08:25、14:25、20:25，实际开始时间可能受 Actions 调度延迟影响）。
每次都从当前 active 的批准源码执行，重新读取 MiSub 登记、下载普通订阅、合并并重新审计。

NAS 现有 source-sync 每小时刷新一次可用池和 connector；不需要在 NAS 再保存两条原始订阅。
云端或本地刷新失败均不得被报告为本轮刷新成功。

## 1.0.20 实施状态

以上链路已随 `1.0.20` 提交、推送并部署至唯一 SpringBok 云套件及 NAS 网关。
运行源码为 `103fdb250ad48536ee06d14a887021e803c72511`；active、stable、公开七产物、
MiSub Global 和 NAS 读回均已核对。普通订阅的实际节点已进入云端审计池并下发 NAS，
ZenProxy/ECH 继续由 NAS 展开和检查。

具体节点快照、测试与业务验收、NAS 健康检查窗口调整及已批准的旧副本清理，见
[2026-11-08 上线与验收记录](cloud-usable-pool-2026-11-08.md)。节点可用性随时间和网络变化，
记录中的数量不是长期可用性承诺；下一次自然定时刷新尚不在该次验收范围内。
