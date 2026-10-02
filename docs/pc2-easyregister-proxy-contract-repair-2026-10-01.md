# PC2 / EasyRegister 代理契约修复

## 故障与修复

PC2 可以访问百度，不是整机互联网不可用。实际业务有两项独立缺陷：

- `/api/nodes` 约 445 KB 的完整诊断快照未压缩，放大了某些路径上的完整响应
  超时；现在仅对明确接受 gzip 的客户端压缩，保留全部字段、原筛选和旧响应。
- pool Snapshot 的 `Port` 是共享 Listener.Port，但 compat 只判断正端口就声称
  dedicated；selected tag 没有落实到连接层。ECH Worker 对 auth.openai.com 的
  真实错误是 `proxy: socks5: request rejected, code=4`，不能用 checkout 成功
  或 providers 200 代替认证目标成功。

客户端 opt-in 请求 `requireDedicatedNode=true`。实际 per-node listener 继续用
`dedicated-node`；pool 在真实 dispatcher 覆盖该监听且有可编码认证用户名时，
返回 `pinned-node`，URL 用户名携带 `pin-strict=<tag>+nosplit`。该 token 从
HTTP/SOCKS 认证解析到 `SelectionDirective.RequiredTag`，每次候选/重试过滤
均保留 tag，失败不跨节点。strict 请求强制 PROXY，disabled profile fail closed。
选择约束在进入节点内部 transport 前仅清除 RequiredTag，避免错误限制 bootstrap
pool 的成员；外层绑定及普通 pool 其他选择行为不变。不能支持的端点拒绝创建
strict 租约，而不是伪造 dedicated metadata。

不带 opt-in 的 pool 租约如实标 `shared-pool`，保留原自动切换。请求 metadata
不能覆盖 `selectedNode*` 路由事实。EasyRegister 校验 strict token/tag/port，
random-node 模式不再把共享 pool 端口冒充 per-node listener。动态 `dev=` 不再
使 strict tag 的本地 route identity 变化。pin 是节点约束，不是对节点内部自身
connector 的无穷层级出口保证，也没有新建全局 ECH 禁用策略。

## 已部署与回滚

`.201` live image：
`sha256:4c960277ddb96494eb0ac7a16a5d859625623a1dabc62f3b0cc252c676fab7c7`，
tag `easyproxy-local:strict-lease-20261001`。旧 gzip 网关停止保留为
`easy-proxy-gateway-before-strict-20261001`。配置 SHA256 始终为
`c373dcc148665dff63c1c45f2f934049548a3424cade3175c2c1b6ad14478516`。
未切 hybrid、未关闭 local-server，保留 22323/29888、host network、mounts 和凭据。

PC2 最终 production image：
`sha256:fa0836171f8ba3523edb5e71c435d85c0e500e19b4c21394bc63b081485f15d4`，
tag `easyregister-local:proxy-strict-narrow-20261001`。
canary 基于它自身的旧 image 派生，不直接替换成整个 production image。

网关必要时回到旧 gzip 版本：

```sh
python3 /home/mjc/easyproxy-strict-fix/20261001/deploy_strict_fix.py gateway rollback
```

PC2 必要时先回到窄化前版本，再回到旧 gzip 版本；两步均不删除容器：

```sh
python3 /home/mjc/easyregister/proxy-strict-narrow-fix/20261001/deploy_narrow_fix.py register rollback
python3 /home/mjc/easyregister/proxy-strict-fix/20261001/deploy_strict_fix.py register rollback
```

网关和客户端必须配对回滚，不能混用旧网关与 strict 客户端。旧网关会忽略
`requireDedicatedNode`，仍可能误标共享 22323 为 dedicated；当前客户端对旧
dedicated 响应的历史校验不能保证拒绝它，不能声称单独回滚网关会安全失败。
当前 live 容器 restart policy 已保留；历史 compose image 定义没有自动更新。
以后若用原 compose 重建，必须显式使用已验收 image，不能直接以旧定义 `up`
覆盖 live。这里没有擅自执行 compose down、prune、删除容器/卷或提交推送。

## 证据边界

Go monitor/dispatch/pool 聚焦回归通过，包含候选降级、重试不跨 tag、未知 tag
安全失败、普通 pool 回退保留、profile DIRECT 防漏、bootstrap 约束消费和
metadata 防覆盖。Python client 33 项通过；本地 acquire 源码与本地 flow fixture
在 PC2 真实依赖中 21 项通过。部署用的 acquire 另以各自实际基线进行反向替换
一致性证明，而不是将整个本地文件覆盖进镜像。Linux production tags 构建通过。

02:04 UTC，正常业务租约自动选 SG-X5-3，providers 与 auth/login 全正文 200、
无 challenge；网关日志显示两个目标均走该 shadowsocks tag，release 成功。
02:17 UTC 的一次 unpaid seed 则在较大的 ChatGPT auth/login 预检正文超时；
没有新 seed、邮箱或付费短信。这个剩余问题不能用“200 头”或小接口成功掩盖，
也不说明已完成最终 OAuth。详见 EasyRegister 对应最新调查文档。

脱敏证据：`linshi/easyproxy-pc2-business-repair-20261001/`。

## 02:39 UTC 复核及 registration-only 预检范围

PC2 实际生产容器重新完整读取 all=88、available=46；subscription enabled、
非刷新中且无错误，gateway applied=true。生产仍关闭付费，prepared paid005
仍 created/never-started，SMS sessions=0、供应商活动订单=0、余额 4.5953 USD。
country16/service `dr` 报价 0.045 USD、库存 271320，均仅为这个时间点的快照。

ChatGPT 大登录页的 HTTP/1.1 对照同样超时：收到 824706 decoded bytes 仍未
结束。默认 HTTP/2 对照收到 195116 decoded bytes 也未结束，因此未强制更改
HTTP version、增加超时或将部分正文视为成功。两次 auth 登录页和 CSRF 均完整。

实际 unpaid seed 的六个 step 仅包含注册和 Platform 初始化，没有 ChatGPT
login-init。provider 的注册路径使用 Platform/Auth0，不能把大 ChatGPT landing
HTML 作为该样本的必要前置。新 004 独立配置只从这个 registration-only 样本
的 `probe_urls` 移除该 landing 页面；完整 auth/login 与 CSRF 200、拒绝 challenge、
原 timeout、单次限制和短信关闭全部保留。原 003、production/full flow、paid
continuation 和真实 ChatGPT login-init 调用均未修改。这是预检 owner 修正，
不是对大响应故障或最终 OAuth 的验收。

## 后续真实节点失败

004 已通过代理和邮箱领取，但注册端收到 curl code 7 / CONNECT 502。
真实严格 tag `fast-b2-2` 出现三次无返回数据的 EOF，被原 TCP blacklist 政策
拉黑 24 小时；随后同 tag 的 auth CONNECT 返回 no healthy proxy available。
未清除 blacklist，也未让 strict 改选其他节点。节点曾通过入口探针，不代表
长注册链路中每个后续连接都会健康，不能将其归为 PC2 整体离线。

SG-X5-3 的独立四目标传输对照完整，包含 auth 83547 bytes / 200 和 Platform
4875 bytes / 200；Sentinel 根路径 404 只作 TLS/HTTP 可达证据。新 unpaid 005
仅使用现有 native static 模式绑定 `pin-strict=sg-x5-3+nosplit`，保留全部严格
预检，不更改全局 pool 或生产/付费配置。当前配置仍不能保证全池所有节点都
适合注册，也未宣称最终 OAuth 已完成。对应脱敏证据为
`seed004-final.json`、`seed004-gateway-routing.json`、`seed004-node-blacklist.json`、
`strict-sg3-transport-030931.json` 和 `seed005-launched.json`。

## 03:29 UTC 验收边界

005 在真实 SG-X5-3 static strict 路线下正常退出，代理、邮箱领取、注册、
Platform 初始化步骤、代理/邮箱释放六步均 ok，正常 paid-image seed validator
亦接受新 seed，原字节/hash 已保留。这证明 PC2 确实有可用的共享代理业务路线，
不是整体互联网不可用，也不是仅把公开 API 的 200 头当成功。

没有将这个特定节点的成功扩大为全池注册适配，也没有把新固定模式推广成
全局 production/paid 配置。后续 full paid flow 的真实 ChatGPT landing HTML
仍不能完整读完，未修改其门禁或启动付费。最终 Codex OAuth/free-personal 未
验收；旧容器及 rollback 路径保留，未执行删除、prune、compose down、commit
或 push。最后 live gateway image 仍为 `4c9602...` / unless-stopped，PC2 production
仍为 `fa0836...` / paid=false / restarts=0。完整证据及下一停点见 EasyRegister
`docs/auth-session-repair-2026-09-30.md` 文末。
