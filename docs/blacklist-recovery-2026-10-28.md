# 认证隔离与黑名单恢复边界修复（2026-10-28）

> 本文保留为该次恢复修复的历史回执。后续已部署冷启动状态恢复修复，当前镜像、
> Compose 和剩余问题请先读 [持久状态恢复修复](persisted-pool-state-2026-10-28.md)。

## 范围与结论

本次从 Crow 的浏览器代理故障继续调查，修复 EasyProxy 的一个选路恢复死锁，
并在 Debian VM `192.168.15.201` 限定更新唯一运行网关。
没有修改云端部署、代理凭据、`22323` 入口、节点清单或 Crow 消费者配置。
源码尚未提交、推送或创建 Release。

本次证明了安装正确，以及同一 Crow 代理桥更新前失败、更新后两次 HTTPS 请求成功。
**未证明长期代理健康、管理面与数据面完全一致或滑块通过率提高。**

## 可复现根因与修改

现场配置为连续失败阈值 `3`、普通黑名单 `24h`、健康探测间隔 `2h`。
更新前最新快照为 49 个节点，34 个标记为探测可用，46 个在普通黑名单中，
6 个认证隔离，0 个 `effective_available`。管理接口和实际网关身份已交叉核对。

`internal/outbound/pool/selection.go::releaseIfAllBlacklistedLocked()` 原先遍历全部节点：

1. 常规选路先排除上游认证失败节点，行为正确。
2. 全黑名单恢复仍把这些已不可选的认证隔离节点算进去。
3. 只要一个认证隔离节点未进入普通黑名单，就永远不满足“全部拉黑”，
   其他普通节点也无法通过既有全黑名单兜底恢复。
4. 反之，如果认证隔离节点也在普通黑名单中，旧代码会无谓释放其普通黑名单。

现在恢复集合和释放集合均排除认证隔离节点；非隔离集合为空时不恢复。
只有普通节点触发既有的全黑名单重试，不增加重试预算，不修改黑名单时长。
认证隔离仍只能由新的成功探测解除；UDP 和单次请求排除边界保持。

新增 `internal/outbound/pool/auth_recovery_test.go` 固定上述两种反例，
并覆盖全认证隔离、存在健康节点、请求排除、UDP 不释放等边界。

## 验证

- 旧代码离线复现两个断言失败，补丁后聚焦回归通过。
- Windows `go test ./... -count=1`、`go vet ./...` 通过。
- monitor、pool、boxmgr 三包 `go test -race` 通过。
- Linux amd64 完整生产 tags 编译通过；同一候选在断网容器内通过上述三包测试，
  日志记录 267 个顶层测试通过（不将子测试重复计为独立场景）。
- Go 格式检查通过；有效代码行门禁通过，495 个 tracked 文件、0 个硬超限、
  7 个既有软警告；新增测试另行核对 116 行，修改文件 426 行。
- 门禁工具相关 Python 测试 9 passed；实际切换脚本的故障注入测试 5 passed。
- release contract、UTF-8 无 BOM、`git diff --check` 通过。
- 前端、云端、全量订阅及其他产品未修改，未重新执行无关完整验收。

## 当前部署定位

- 容器：`easy-proxy-gateway`
- ID：`f6153a145a695c6993ba2964531e34f7698d716d3e5e857903af8eb69e521994`
- 镜像：`easyproxy/local-hotfix:recovery-20261028-20ad6eef0a4b`
- VM 镜像：`sha256:fe23998457f17bbe210a7fd55f8e8e7724bc37197312b8b9d74fd313ce4fd6a8`
- 二进制 SHA-256：`44f47b322520a121e9a93478beabeda5b535148312ca67369e990c5395a4e873`
- 构建源码基线：`533949afa7e2368a5b014fcd0af1774f62b90fa9`，其上叠加本次未提交修复。
- 构建源码清单 SHA-256：`20ad6eef0a4b06e03d682a86166144cf568faa9b7ec6ba10e6af120b9a3e7e56`
  （tracked `service/base/` 加本次新增测试，不是上次认证补丁清单的计算范围）。
- Compose：`/opt/easyproxy-gateway/compose/docker-compose.recovery-20261028-r1.yaml`
- Project：`easyproxy-gateway-recovery-20261028-r1`
- 启动：`2026-10-28T13:38:36.826823337Z`；独立回验时 `RestartCount=0`。

当前启动入口须使用以上准确文件、project，不能无参数重放旧 Compose。
切换前核实新 project 无同标签实例，停止及重命名准确旧 ID；没有服务级替换旧备份。
启动期曾暂时拒绝管理连接，随后独立验证 API、gateway enabled/applied/TUN、
kernel 路由及 nft、配置哈希、HostConfig、Mounts 与回退材料，不把早期启动结果当最终验收。

## 数据与回退

- 原网关 ID `848d679f485cd1757a6dac66bb4b2e3f0516c1610bedab57b79829c19c357425`
  现名 `easy-proxy-gateway-rollback-20261028-recovery-r1`，已停止，保留原镜像和配置。
- 更早两个认证修复回退容器也保留且停止。当前只有一个运行网关。
- `/opt/easyproxy-gateway/backups/recovery-20261028-r1` 保留配置与数据副本：
  30 个文件、5,139,148 bytes，其中一份 SQLite 通过在线 backup API 制作。
  备份目录 0700、文件 0600；无删除、清库或凭据轮换。
- 如需紧急回退，先重新核对上述准确 ID/名称及其他 owner，再停止新实例、
  将其重命名保留，并恢复旧 canonical 名称和旧实例；不要运行 `down`、`rm` 或 `prune`。
  旧版本仍含本次修复的恢复缺陷，回退不是永久解决方案。
- 切换前后 PC2 的 12 个运行容器 ID、PID、StartedAt 均一致；没有重启 Crow 浏览器。

## 仍未完成的边界

1. 更新后的同一 PC2 桥在 UTC 13:41:23、13:45:23 两次返回 Google 204，证书校验开启；
   更新前 UTC 13:34:26 的同一路径为 `SSLEOFError`。总共只有三次显式非业务请求。
2. UTC 13:42:56 后管理 API 再次显示 `effective_available=0`，但第二次真实桥请求仍成功。
   当前仍有持久黑名单管理视图与实际选路的一致性问题；本次没有手工清黑名单或数据库来掩盖它。
   不将 `readback passed`（安装及有界传输门禁）表述为整体长期业务恢复。
3. 自然 seed 滑块尝试 UTC 13:41:12–13:41:42：一次拖动完成，六次验证均有错误，
   一次重试点击后最终 `manual_required`；无 local success、认证 ACK 或绑定健康证明。
   原始拒绝码仍为安全分类 `other`，不能据此推测站点具体拒绝原因。
4. 三个 detail 在切换附近的上一批仍因连接关闭退避 900 秒；没有人为唤醒。
   UTC 13:54:32 独立读取下一自然批次：三者均改为 `detail_challenge_page`，
   均保留 pending detail 和重试预算、发送 captcha report；均为 1 attempt / 0 completed。
   这证明真实调用已推进至挑战页，不是认证成功或商品抓取完成。
5. UTC 13:54:36，detail scope 已登记 challenge、manual flags 为 false；
   seed scope 则 manual_required/manual_only，solver 新 detail 启动数仍为 0。
   后续应核对 scope 仲裁和 legacy 人工锁归属；不能凭此直接删除或忽略人工状态。

证据目录：`C:/Users/Public/nas_home/AI/GameEditor/linshi/crow-easyproxy-recovery-20261028`。
原始部署输入只存 DPAPI；脱敏证据包括 candidate、preflight、staged、activation、
readback、bridge、natural-attempt 及失败/通过日志；一次性 stage/activation 不可重放。
