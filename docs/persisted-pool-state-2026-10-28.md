# 冷启动代理池状态恢复修复（2026-10-28）

## 范围

这是 Crow 采集依赖链中的 EasyProxy 修复，不修改 Crow 的验证码算法、人工暂停、
模型池、请求预算或重试次数，不调整代理入口、节点清单、凭据和探测间隔。
本次只修复代理池冷启动时持久状态恢复不完整的问题。

## 根因与最小改动

实际启动顺序为 pool constructor 注册 monitor entry 并绑定零值 shared state，
随后 boxmgr 从 SQLite 恢复 monitor，最后执行 pool 的 Start。
恢复的 blacklist、截止时间和流量字节总量没有同步回 shared state。
管理 API 读 monitor，选路读 shared，因此可能出现管理端拉黑、运行端仍选中的分叉。
下一次流量记账也会让 shared 累计字节从零重新开始，不能保留恢复后的总量。

- `service/base/internal/outbound/pool/pool.go`：在 StartStateStart 成功初始化后、
  inbound 开始受理前执行一次状态同步；此前已 lazy 初始化也不跳过。
- `persisted_start.go`：持有既有 entryMu，从 monitor snapshot 同步普通 blacklist、
  截止时间和总字节；不修改 active、UDP、认证隔离、probe callback 或连续失败计数。
- `shared_state.go`：零截止时间不自动过期，与 monitor 已有无期限语义一致。
- 两个 `persisted_start*_test.go`：构造与 lazy 路径、期限、真实候选筛选、流量续记、
  累计失败不变成连续失败、认证隔离、UDP、并发和 rollback 回归。

同步不进入逐请求选路热路径；没有新增 SQLite 字段或要求用户手工改库。
既有 TCP 全黑名单恢复策略保留，零期限普通 blacklist 也不升级为认证硬隔离。
认证隔离仍不能被普通 blacklist release 解除。

## 证据与验证

旧代码在离线回归中暴露 constructor/lazy 两条选路分叉、shared 流量从零续记、
无期限 blacklist 丢失；补丁后同一回归通过。

- Windows Go 后端 `go test ./... -count=1` 与 `go vet ./...` 通过。
- pool、monitor、boxmgr 三包常规测试及 race 通过。
- 生产 tags 的 Linux amd64 构建通过，同候选在 network=none 容器内三包回归通过，
  275 个顶层测试，不重复累计子测试。
- Go 格式检查通过；有效代码行 tracked gate 通过，0 硬超限、7 个既有软警告。
  本次新增文件另查为 19、93、137 行；修改文件为 252、270 行，均未超过 500。
- 精确切换及故障回退的离线注入测试 5 passed；源码清单、UTF-8 无 BOM、diff 检查通过。
- 未额外调用付费模型，未运行全目录上游探测、无关前端或云端验收。

证据根：`AI/GameEditor/linshi/crow-persisted-pool-20261028`。
候选清单绑定完整 tracked service/base 加明确新增文件；相对已安装上一候选，
仅有本次五个 Go 文件变化，上一认证/恢复修复保持不变。

## 运行证据的限制

修复前 UTC 14:05 的只读刷新显示三个 Crow detail worker 已自然恢复抓取，
一个批次为 10 attempts / 10 completed；本地 solver 有一次绑定认证确认，
但没有 verified automatic drag 证据，不能称为自动滑块通过。
因此未根据上一轮短时快照放宽 seed/detail 仲裁或清除人工状态。

本补丁解决冷启动恢复一致性，不宣称解决热 reload 中 constructor 已覆盖旧累计值的
历史问题，不宣称代理全节点长期健康或滑块成功率提高。

## 部署状态

候选：`easyproxy/local-hotfix:persisted-20261028-788fc1745afe`。
二进制 SHA-256：`00054479d8e34053e52181f435c452bd5abc6ab3453e07ea3e9e1a99804f834d`。
源码清单 SHA-256：`788fc1745afe9bf4c72ec031ecbeaf00c6bbbfae8ee2bc49f21d27ac388904b7`。
已于 UTC 14:16:53 限定切换 Debian VM `192.168.15.201` 的唯一网关。
新容器 ID：`19790a8278b279878e3c7a61d83a9b80544a57204cc9189104bcbd70fd2113a9`。
VM image：`sha256:40a5fe1485045005f5d8e0fbac88a2c80ada390a079d07332fcd69f7c2215176`。
Compose：`/opt/easyproxy-gateway/compose/docker-compose.persisted-20261028-r1.yaml`，
project：`easyproxy-gateway-persisted-20261028-r1`。

独立回验 UTC 14:21:02 通过安装哈希、配置、Mounts、HostConfig、唯一运行网关、
TUN、rule、table100、nft 和 source-sync；RestartCount=0。
PC2 的 12 个运行容器 ID/PID/StartedAt 与切换前相同，Crow 浏览器未重启。
前两份启动期回验未通过 gateway/kernel 门禁，原回执保留：初始节点探测后，
配置中的 min_available_nodes=1、health_check_timeout=1m 未满足并记录 warning，
随后 gateway 完成启动；没有改阈值或清 blacklist 让其通过。

本轮只追加一次非业务 HTTPS，经原 Crow bridge 返回 204，1.421s、TLS 校验开启。
没有主动导航商品页、追加验证码或调用付费模型。
管理快照仍为 49 节点、5 available、0 effective、46 blacklisted、34 auth quarantine；
**安装通过不等于全节点健康，0 effective 尚不能宣称已消除。**
既有日志已匹配这次 Google 请求确实进入 pool；相应节点记录 UTC 14:21:02 成功，
但随后 UTC 14:21:30 又失败并拉黑。因此后续 0 effective 不能单凭总数认定仍是
同一恢复分叉，也不能将短时成功外推为长期健康。

原网关 `f6153a145a695c6993ba2964531e34f7698d716d3e5e857903af8eb69e521994`
现名 `easy-proxy-gateway-rollback-20261028-persisted-r1`，停止保留；更早回退容器也保留。
备份位于 `/opt/easyproxy-gateway/backups/persisted-20261028-r1`，33 文件、2,586,299 bytes，
包含一份 SQLite online backup；目录 0700、文件 0600，没有删库、删容器或轮换密钥。
唯一接入 JSON 仅更新 deployment 元数据，其他对象深比较不变；源码未提交、未推送。

后续自然业务也有波动：UTC 14:24 三个 detail worker 的最新批次均为
`detail_stale_challenge_ignored`、1 attempt / 0 completed，任务和预算保留；
UTC 14:26:57 再次只读查询，三者最新结果均已变成 `detail_item_raw_captured`。
没有人为唤醒、清状态或触发额外业务请求，不能据短时快照计算长期成功率。
该次 suppression 的具体 report status 未在现有精简历史日志中找回，保留为下一步调查，
不据此放宽归属或 stale 防护。
