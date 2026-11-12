# EasyProxy 剩余验收计划

> 本文仅保留未完成的 Phase 8，不再用已完成阶段的实施清单指导当前开发。
> 原完整计划及 Phase 0–7 的历史证据保存在 Git 历史中；清理前版本为 `06af906`。
> 优化前基线提交：`1b555e682a25d5b7bbcfd3c6a384cecb737019aa`。

## 当前实施入口

- 产品结构与所有权：[架构](docs/architecture.md)、[上游同步](docs/upstream-sync.md)。
- 开发规范：[开发标准](docs/DEVELOPMENT_STANDARD.md)、[贡献指南](CONTRIBUTING.md)。
- 操作合同：[AI 操作手册](EASYPROXY_AI_OPERATIONS.md)、[Fork 操作指南](docs/fork-operator-guide.md)。
- 发布和验收：[发布清单](docs/release-checklist.md)、[发布合同](docs/release-contract.md)。
- 数据保护：[MiSub 数据保护](docs/misub-data-protection.md)、[ECH 生命周期](docs/ech-lifecycle.md)。

## Phase 8：独立 Fork 端到端验收

原计划记录的状态是“准备完成、真实验收未完成”。本次文档清理没有执行这些验收，
也不将历史 NAS 审计或静态合同测试算作独立 Fork 的验收通过。
过去记录的账户、节点数、镜像运行时间和故障快照已过时，不再作为当前状态引用。
执行前必须重新核实独立 GitHub Fork、专用 Cloudflare 测试账户、受保护 Environment、
测试凭据及 Windows Service/Linux（含 arm64）测试资源；不得借用生产资源伪造破坏性验收。
- [ ] P8-01 使用全新 GitHub Fork；
- [ ] P8-02 使用空 Cloudflare 测试账户；
- [ ] P8-03 只按公开文档配置 Secrets；
- [ ] P8-04 一键 Bootstrap；
- [ ] P8-05 安装一个 Linux EasyProxy；
- [ ] P8-06 安装一个 Windows EasyProxy；
- [ ] P8-07 验证 LAN 代理和管理 API；
- [ ] P8-08 制造 MiSub 更新失败并回滚；
- [ ] P8-09 制造 ECH Token 轮换失败并恢复；
- [ ] P8-10 验证 Aggregator last-known-good；
- [ ] P8-11 执行完整 Update；
- [ ] P8-12 收集测试资源清单并执行受保护的测试账户清理；
- [ ] P8-13 发布优化后首个 major release。

## 退出条件与边界

- 按公开文档独立复现，记录所用提交、资源清单和真实测试结果。
- 数据保护、更新、回滚和发布清单均经验证；未验收的平台不得宣称支持。
- Git 历史只能恢复代码，不能替代 D1、R2、运行状态、配置或凭据备份。
- 此计划不构成生产发布、付费、权限变更或永久删除的执行授权。
