# ech-workers-cloudflare

这是 EasyProxy 仓库直接维护的 Cloudflare WebSocket/TCP 中继，不是
`upstreams/ech-workers` 子模块；后者是本地连接端，两者不能互换。

`wrangler.jsonc` 指向 `src/index.js`。WebSocket 请求通过
`Sec-WebSocket-Protocol` 提供 `ECH_TOKEN`；轮换期间可接受尚未过期的
`ECH_TOKEN_PREVIOUS`，到期时间由 `ECH_TOKEN_PREVIOUS_EXPIRES_AT` 指定。
未配置当前 token 时返回 503，WebSocket 鉴权失败返回 401。
普通根路径 HTTP 200 仅证明入口响应，不代表 TCP 隧道或实际代理业务可用。

部署与验证使用根目录的 [部署指南](../../docs/deployment.md)，凭据与轮换边界见
[安全指南](../../docs/security.md)。不要将真实 token 写入源码、文档或跟踪的配置文件。
本目录不是 `.sba` 云套件 Worker 的替代发布入口。

从仓库根目录执行本地鉴权和连接生命周期测试：

```sh
node --test workers/ech-workers-cloudflare/src/auth.test.mjs workers/ech-workers-cloudflare/src/session.test.mjs
```

这些测试不替代 Cloudflare 实际部署及本地连接端的端到端验收。
