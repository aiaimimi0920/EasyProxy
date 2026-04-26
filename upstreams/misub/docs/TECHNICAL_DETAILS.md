# MiSub 技术细节

本文档面向维护当前 `EasyProxy` monorepo 中 `MiSub` 分支的开发者，描述这条分叉的关键实现和运行边界。

与上游文档相比，这里只记录当前仓库**已经存在且仍在使用**的实现，不描述尚未同步进来的运行时能力。

## 1. 运行形态

当前 `MiSub` 在本仓库中有两条兼容运行路径：

- Cloudflare Pages + Functions
- Docker / VPS Node 运行时

两条路径共享同一份前端和大部分后端业务逻辑，要求尽量保持以下契约一致：

- 登录与管理后台接口
- `GET /api/manifest/:profileId` 机器接口
- `/cron` 与 `/api/cron/*` 定时任务链路
- source registry / profile 组合逻辑

## 2. 数据模型与角色划分

当前分支中的核心对象可分为三类：

### 2.1 source

`source` 是统一来源对象，当前会承载：

- `subscription`
- `proxy_uri`
- `connector`

这也是为什么当前 `MiSub` 不再只像传统订阅工具那样管理“机场 URL”，而是承担整个 `EasyProxy` 栈的全局 source registry。

### 2.2 profile

`profile` 是对 sources 的组合与发布层。

它负责：

- 选择启用哪些 `subscription`
- 选择启用哪些 `manualNodes`
- 控制是否公开展示
- 生成面向机器或用户消费的稳定标识

`/api/manifest/:profileId` 实际上就是把 profile 解析成一份面向机器消费的 source 清单。

### 2.3 settings

全局设置负责：

- 默认输出文件名
- token / callback / 访问控制
- 默认前缀配置
- 默认节点净化配置
- aggregator 自动同步配置

## 3. 存储层

当前仓库保留了 KV 与 D1 双路径，但职责已经不同：

- D1：Cloudflare 生产主路径
- KV：兼容、迁移、保底读取

统一入口位于：

- `functions/storage-adapter.js`

当前这条分叉仍然以“主键对象整体读写”为主，没有完全切到上游后来新增的 row-level helper API。因此在测试和文档同步时，需要特别区分：

- 哪些测试建立在老的 blob 读写模型上
- 哪些测试建立在新的 row-level helper 模型上

后者目前不能直接硬搬。

## 4. 节点净化管道

当前分支已经具备一套稳定可用的节点净化管道，核心位于：

- `functions/utils/node-transformer.js`

它目前聚焦在四类能力：

1. 正则重命名
2. 智能去重
3. 模板重命名
4. 排序

配置来源分两层：

- 全局：`defaultNodeTransform`
- profile 级：`profile.nodeTransform`

profile 级配置优先于全局；若 profile 未自定义，则继承全局默认值。

这条分叉当前**没有**把上游更激进的脚本型过滤运行时完整同步进来，因此文档和测试都应围绕现有这四类能力展开，而不是假设已经拥有完整 operator runtime。

## 5. 前缀设置与净化管道的关系

本地分支同时保留了两套能力：

- `defaultPrefixSettings` / `profile.prefixSettings`
- `defaultNodeTransform` / `profile.nodeTransform`

它们不是互斥关系，但职责不同：

- 前缀设置更像“轻量标记”
- 节点净化管道更像“最终输出格式控制”

如果启用了模板重命名，最终节点名称通常会被模板完全重写，因此排查命名问题时要先区分：

- 是前缀层导致的
- 还是模板层导致的

## 6. Manifest 机器接口

当前 `EasyProxy` 与 `MiSub` 的核心集成点是：

- `GET /api/manifest/:profileId`

鉴权方式：

- `Authorization: Bearer <MANIFEST_TOKEN>`

输出规则：

- 解析 `profile.id` 或 `profile.customId`
- 仅返回启用的 source
- 返回 `subscription`、`proxy_uri`、`connector`

这里的 `connector` 当前只输出元数据，不在 `MiSub` 内部执行运行时连接逻辑。

## 7. Aggregator 同步链路

本仓库中的 `MiSub` 不只是消费用户手填订阅，还承担 aggregator 同步入口。

当前同步模型分两层：

- discovery 层：`crawledsubs.json`
- stable 层：`clash.yaml`

这两层在 `MiSub` 中扮演的角色不同：

- discovery 用于内部同步和二次探测
- stable 用于默认公开 profile 暴露

因此在维护同步逻辑时，要避免把“原始 crawler 发现结果”和“稳定公开产物”混成同一类 source。

## 8. Cron 链路

当前与 cron 相关的三条入口分别承担不同职责：

- `/cron?secret=...`
- `/api/cron/status`
- `/api/cron/trigger`

配套的 dashboard 页面是：

- `/cron-dashboard`

这套链路里：

- `/cron` 是外部调度入口
- `/api/cron/*` 是已登录管理员的运维入口
- `/cron-dashboard` 是对后两者的轻前端封装

因此后续任何改动都要避免把这三类职责重新混在一起。

## 9. Docker / VPS 兼容边界

当前仓库仍保留 Docker / VPS 路径，这意味着开发时不能只站在 Pages Functions 视角思考。

需要同时注意：

- Cloudflare 绑定对象是否存在
- 本地 Node 运行时变量是否存在
- SQLite / 本地路径兼容
- 回调 URL 是否能够在反代环境下稳定解析

这也是为什么当前很多配置项会同时存在：

- `MISUB_PUBLIC_URL`
- `MISUB_CALLBACK_URL`
- `MISUB_DB_PATH`

## 10. 当前最重要的维护原则

如果后续继续做上游回灌，建议遵循这条顺序：

1. 先同步测试和文档
2. 再同步不会改变部署契约的小型工具补丁
3. 最后再考虑会改变运行入口、存储抽象或路由面的功能

原因很简单：当前 `MiSub` 已经不是“轻度改主题的上游镜像”，而是 `EasyProxy` 栈中的 source registry 和 manifest center。对它做机械式 rebase，风险会比表面上看起来大很多。

