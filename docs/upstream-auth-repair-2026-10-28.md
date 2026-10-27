# EasyProxy 上游认证失败修复与 NAS 回验（2026-10-28）

## 结论与范围

已定位此前出网失败的直接原因，并在 NAS 内 Debian VM `192.168.15.201` 部署本地修复镜像。
HTTPS CONNECT、SOCKS5 TLS、匿名拒绝，以及 DSM 原生 IPv4 透明 HTTPS 的最终回验均通过。
另已准确定位 PC2 上持续发送旧 LAN 凭据的浏览器代理桥，更新其唯一持久秘密文件，只重启该消费者一次；真实桥接出网与修复后日志观察均通过。
本轮没有修改云端部署、manifest 内容或第三方代理凭据，没有删除资源；除上述准确消费者外，没有修改或重启其他消费者。

以下是带时间的验证快照，不代表全部节点或所有业务目标长期可用。修复源码与本记录一并纳入 Git 版本管理；本次不创建新 GitHub Release，也不触发 SpringBok 云端更新。

## 已证原因

1. `de-1-2-9mb-s`、`ch-1-2-4mb-s`、`de-3-1-5mb-s` 三个 HTTP 上游，对 VM 发出的 CONNECT 返回 `407 Proxy Authentication Required`，认证方案为 Basic。
   这三个节点的 manifest 原始 URI 和 NAS 实际 URI 均无用户名、密码，二者一致，不是 HTTP builder 丢失认证字段。
   不知道第三方代理何时改变了认证策略，也没有可合法推导的上游密码；没有拿 LAN 密码去尝试第三方代理。
2. 节点先前成功探测的状态，在后续真实业务认证失败后仍保留。原逻辑为避免把目标站点故障当作节点故障，刻意不让流量失败改变探测可用状态；探测间隔为两小时。
   这对普通超时有合理性，但对明确的代理层认证拒绝缺少例外，而且兜底选路可能继续使用这些节点。
3. 回归测试另外证实：普通 HTTP 健康探测会接受 407，且第二次暖请求成功可能掩盖第一次认证失败。
   这是额外修复的明确缺陷，不将其声称为当前所有 CONNECT 失败的唯一原因。
4. 旧 sing 依赖的 HTTP/SOCKS 认证失败错误包含提交的密码或原始认证头，可能被写入运行日志。
5. PC2 `192.168.15.104` 的持续入站认证失败是另一条独立故障：`fapaifang-pc2-browser-solver` 的本地代理桥从持久秘密文件加载了旧 LAN 密码。
   已用 TCP SYN 来源与命名空间证据准确归因，并核对运行桥参数与旧文件完全一致，不只依据某个容器配置中出现旧值来推断来源。

## 源码修改

- 在 HTTP outbound 边界，仅将当前固定依赖对 CONNECT 407 返回的准确错误 `authentication required` 转换为可识别的上游认证失败类型。
- monitor 收到该类型后立即撤销旧可用状态，记录 `upstream_auth_failed`。普通目标超时、目标站点 401/403 和其他认证文本不触发这项隔离。
- 认证隔离不受普通 blacklist release、旧连接的成功回执或 source/secondary fail-open 绕过；只有新的成功探测才能恢复。
- 早于认证拒绝启动的探测不能通过迟到成功覆盖新失败；失败探测不清除隔离。
- 健康探测明确拒绝 407，首次认证拒绝不再被暖请求或其他目标的兜底掩盖。
- 在本项目 HTTP/mixed/SOCKS 入站 logger 边界截断认证失败消息的凭据后缀，保留来源和错误类别，不修改 vendored/module-cache 依赖。
- 加入真实本地 CONNECT 407、真实 mixed 错误认证日志脱敏、恢复、并发迟到回执、普通故障不误伤等回归。
- 脱敏采用最早的真实协议认证错误标记，回归覆盖提交的凭据本身包含另一协议认证错误标记的情况，避免标记混淆泄漏凭据。

没有删除或批量禁用云端节点，也没有通过猜测密码、弱化证书校验或盲目增加重试来让验收变绿。

## 当前运行版本

- 容器：`easy-proxy-gateway`
- 容器 ID：`848d679f485cd1757a6dac66bb4b2e3f0516c1610bedab57b79829c19c357425`
- 镜像：`easyproxy/local-hotfix:auth-20261028-a7bcf23b93b0`
- VM 镜像标识：`sha256:830343bbb88ad671f4e2753cdd884aa6773aace046616d58a9eafd0f9a0cc253`
- 构建时源码基线：`24b26417b57bd3fc4ffd3f3fb0334c3155373460`，应用基线 1.0.13；其上叠加本记录所述修复，实际内容以修复源码集合及二进制哈希核对。
- 修复源码集合 SHA-256：`a7bcf23b93b02ac496a02bcf1fe6f1e6e221c8112835fe5edc1a8cf15e863782`
- `easy-proxy` 二进制 SHA-256：`9fc5667478fc71a99cbf7e4f28c2cbf595d358446a76181903cffb0fc9c5318c`
- 新 Compose：`/opt/easyproxy-gateway/compose/docker-compose.auth-20261028-final.yaml`
- Compose project：`easyproxy-gateway-auth-20261028-final`
- 启动时间：`2026-10-27T18:00:48.150940384Z`；最终快照 `RestartCount=0`。

镜像保留准确旧生产镜像的所有基础层、connector 二进制和 entrypoint，仅替换 `easy-proxy` 并增加修复标识。
Windows 与 VM 的镜像标识可能因 Docker/OCI 存储表示而不同；已核对 VM 实际二进制哈希和基础层。

运行配置、挂载、host network、权限能力、设备和日志轮转沿用原部署。配置文件原始 SHA-256 未变，权限保持 0600。
无秘密的 Compose 文件为 0644，以便普通 SSH 用户运行 Docker Compose；不得据此放宽业务配置和密钥权限。

后续使用准确新文件，不要在旧 Compose 目录无参数启动历史部署：

```sh
docker compose --env-file /dev/null -p easyproxy-gateway-auth-20261028-final \
  -f /opt/easyproxy-gateway/compose/docker-compose.auth-20261028-final.yaml \
  up -d --no-build --pull never
```

## 最终通过的验证

- `go test ./... -count=1`、`go vet ./...`。
- monitor、pool、boxmgr 的 `go test -race`。
- Linux amd64 完整生产 tags 编译；同一 Linux 镜像内执行 monitor、pool、boxmgr 的相关全部测试。
- NAS 实际二进制哈希、挂载和运行身份一致；新容器最终 `RestartCount=0`。
- manifest healthy、无 fallback、source-sync 无 last_error。
- gateway enabled/applied、TUN ready、接口、table 100、nft 表存在。
- 三个准确故障节点均 `upstream_auth_failed=true`、`effective_available=false`。
- 北京时间 2026-10-28 02:16 的最终快照共有 47 个节点，其中 32 个因实际认证拒绝隔离、4 个 effective available。这不是 47 个节点全部可用的承诺，也不把其他失败一律归为认证故障。
- 使用唯一当前接入文件：HTTPS CONNECT 到 Google 返回 204，SOCKS5 后的 TLS 请求返回 204，证书校验均启用。
- 最后一轮匿名 HTTP 返回 407，SOCKS5 无认证协商被拒绝。
- 一次随机错误认证请求被关闭连接，并按准确客户端 IP、端口匹配到服务端认证拒绝日志；日志不含该随机凭据，保留 `credentials=[REDACTED]`。没有将 reset 本身当作认证拒绝成功。
- DSM 不使用显式代理、强制 IPv4，经 `.201` 默认下一跳访问 Google，TLS 验证成功，返回 204。
- PC2 浏览器容器内，经 `127.0.0.1:3128` 本地桥的一次真实 HTTPS CONNECT 到 Google 返回 204、空响应体；TLS 校验启用。新运行桥使用当前秘密文件，profile 与全部挂载保持不变。
- 北京时间 02:15:30 至 02:16:38 的修复后观察窗口记录到 PC2 30 条入站连接，认证失败为 0；消费者与网关均无额外重启。这是有真实流量的有界验证，不是因为无流量才没有错误。

## PC2 消费者的准确修复

- 消费者：`fapaifang-pc2-browser-solver`，容器 ID `4321756727df43cb56989866897c5ee6bdd3da83749edb6879e8f46f58d7ab8a`。
- 宿主秘密文件：`/srv/data/fapaifang-worker/secrets/browser-proxy-upstream`，容器内路径 `/data/secrets/browser-proxy-upstream`。
- 先用 DPAPI 备份原文件，再严格核对容器身份、启动时间和原文件哈希，只更新该文件为唯一当前接入文件中的 LAN 凭据。
- 文件属主为桥进程对应的 `1000:1000`，权限由旧 0644 收紧为 0600，并验证容器用户可读取。没有在文档或终端展示凭据 URI。
- 桥启动时将上游 URI 加载到内存，因此只重启该容器一次。修复后启动时间为 `2026-10-27T18:08:28.796559759Z`，最终仍是同一容器、同一启动时间，`RestartCount=0`。
- 未删除 profile、Cookie、数据或挂载，未访问业务网页，也未启动注册、SMS、OAuth 或付费流程。

## 中间失败与排除项

首次切换因新 Compose 设为 root:0600，普通 SSH 用户读取失败；脚本自动恢复原容器并核实。
随后只修正无秘密 Compose 的权限、重新核对哈希和现场身份，再完成切换。

早期回验在管理 API 已监听、但完整 gateway 初始化未完成时，读取到了持久化旧节点状态和 gateway disabled。
因此最终门禁同时要求 gateway enabled/applied/TUN ready，不能只看 API 200 或节点 initial_check_done。

DSM watchdog 在重启窗口自动退回 `.1`；后续已自行恢复 `.201`。对照证明这台 DSM 的旧 `sh /dev/tcp` 探测和标准 TCP connect 均成功，排除了 Shell 不兼容猜测。
未修改 watchdog、timer、DSM 路由配置或 IPv6 配置。没有把启动窗口内的透明测试失败改写成通过，最终另留恢复后的成功回执。

部分较早的错误认证/匿名探测遇到连接 reset。测试夹具现只接受明确 407，或 reset 加准确客户端 IP、端口对应的服务端认证拒绝证据；无对应证据仍失败。
最终随机错误凭据是后一种情况，最后一轮匿名 HTTP 则明确返回 407；保留之前失败回执，不以新回验覆盖旧证据。

最终版启动时，`ch-1-2-4mb-s` 首轮探测遇到非认证临时故障，不能把它猜成认证拒绝。等待门禁后，只对该准确节点执行一次定向探测，真实返回认证失败后正确隔离；没有重新探测全部节点。

PC2 凭据修复曾在写入前因原文件属主/权限假设不符停止，现场未变更；随后按真实桥用户收紧权限。
完成写入和唯一一次重启后，Docker Mounts 返回顺序变化使辅助脚本的直接列表比较断言失败；已只读规范化对账确认挂载完全一致，没有重放写入或再次重启。

## 保留与未完成边界

- 两个回退容器 `easy-proxy-gateway-rollback-20261028-auth`（原生产）和 `easy-proxy-gateway-rollback-20261028-auth-r2`（上一修复版）均保留且已停止，不是额外运行实例。当前只有一个运行网关，旧镜像和持久数据保留；未经另行确认不删除回退材料。
- 所有原始运行响应、日志和配置证据只保留为 DPAPI；没有删除历史日志。
- 另发现 `easy-register-protocol-python` 的 `EASY_PROXY_MANAGEMENT_PASSWORD`、`EASY_PROXY_API_KEY` 仍为旧值，但它不是这批持续错误请求的已证来源。本轮没有盲目改写或重启该消费者；这两个潜在陈旧配置未纳入本轮业务验收。
- 当前仅保证明确列出的测试。没有对所有上游、所有目标、长期稳定性、完整 OS 重启、IPv6 透明接管或 UDP 代理出口做新的全面验收。
- 修复源码与本记录一并提交至 EasyProxy 产品仓库；不能把提交源码等同于创建 GitHub Release 或更新 SpringBok 云端。NAS 已部署的修复镜像保持不变，云端仍使用此前版本。
- 本地凭据库 `aikey` 的说明和统一 JSON 的非秘密部署元数据已同步更新，凭据未轮换，也不随产品源码推送。

## 证据

```text
C:/Users/Public/nas_home/AI/GameEditor/linshi/easyproxy-auth-repair-20261028
```

最终脱敏回执：`candidate-r3.safe.json`、`rollout-cutover-r3.safe.json`、
`production-verification-final.safe.json`、`production-transparent-dsm-final.safe.json`、
`production-traffic-confirmed.safe.json`、`final-ch-targeted-probe.safe.json`、
`pc2-browser-credential-update.safe.json`、`pc2-browser-proxy-verification.safe.json`、
`post-browser-auth-window.safe.json`、`final-auth-snapshot.safe.json`。

诊断与中间过程另见：`selected-direct-connect.safe.json`、`health-r1.safe.json`、
`rollout-failure-reconciled.safe.json`、`rollout-permission-repair.safe.json`、
`dsm-watchdog-probe-comparison.safe.json`、`pc2-syn-origin.safe.json`、
`pc2-browser-credential-preflight.safe.json`、`pc2-browser-reconcile.safe.json`。
