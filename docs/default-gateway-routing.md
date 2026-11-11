# 默认网关规则分流

## 本次修改边界

修复 `gateway.mode: tun` 且未启用 `local_server` 的默认网关路径。
不修改 HTTP / CONNECT / SOCKS5 的客户端参数解析、`split` / `nosplit`、
显式节点选择、严格固定节点以及设备 profile 的既有行为，也不通过开启
`routing.enabled` 或 `local_server.enabled` 来间接改变这些入口。

默认 TUN 网关复用已有规则引擎和配置，匹配顺序为：

1. `routing.rules` 中的手动规则。
2. `routing.rule_files` 中的本地规则。
3. `routing.rule_providers` 中的远程规则，沿用已有定期刷新机制。
4. `routing.use_default_rules` 开启时的内置规则，包括私网、常见国内域名和 `GEOIP,CN,DIRECT`。
5. `gateway.routing.final_policy`，可选 `PROXY` 或 `DIRECT`；不再被内置规则里的 `FINAL,PROXY` 覆盖。

`geoip.enabled` 开启时，TUN 网关现在真正加载 `geoip.database_path`。
数据库配置为空或加载失败会明确报错，而不是悄悄让国内 IP 落入代理兜底。
数据库自动更新沿用 `geoip.auto_update_enabled` 与 `geoip.auto_update_interval`。
网关停止时关闭其 GeoIP 查询资源，并停止规则 provider 刷新。

TUN 保留真实目的 IP，同时使用嗅探域名匹配域名规则；不会为了嗅探而用域名
替换真实 IP、丢失 IP-CIDR / GEOIP 判断依据。Fake-IP 的域名还原仍由 sing-box
处理，不把合成地址当作真实国家归属依据。
决定使用 PROXY 后仍向代理链路传递嗅探域名，保留原有代理端域名解析行为；
DIRECT 则使用真实目的 IP，避免再次解析后改变客户端选择的目标。

## 配置示例

以下仅是现有运行配置中的相关片段，不应直接覆盖完整配置或凭据：

```yaml
routing:
  use_default_rules: true
  rules:
    - DOMAIN-SUFFIX,force-direct.example,DIRECT
    - DOMAIN-SUFFIX,force-proxy.example,PROXY

gateway:
  routing:
    final_policy: PROXY
```

未知目标的策略是明确的配置选择，不是可达性探测：

- `PROXY`：未命中直连规则的目标继续代理，保留原默认行为。
- `DIRECT`：未知目标直连，需要用代理规则列表覆盖被墙目标。

此次没有新增“直连失败后自动重发请求”的机制。GeoIP 也不是网站可达性的
充分证明；境外但可直连的网站可加入 DIRECT 规则。Fake-IP / 纯域名目标没有
真实目的 IP 时，需要域名规则覆盖，不能仅依赖 `GEOIP,CN,DIRECT`。

## 验证和上线边界

相关回归覆盖默认网关 TCP / UDP、国内 IP 与嗅探域名并存、手动覆盖、
provider 规则优先级和更新、DIRECT 最终兜底、损坏数据库显式报错，以及
builder 不启用客户端 dispatcher / device profiles。现有客户端参数、profile、
gateway 和 app / boxmgr 回归也应一起运行。

本地代码与构建通过不代表 NAS 已更新。上线需使用新的 Linux 运行程序或镜像，
保留现有运行配置与数据，随后实际核对国内访问出口、需要代理的网站和显式选节点请求。
`local_server` 的 profile 路由与旧式非 TUN 透明 TCP 路径不属于本次修复范围。
