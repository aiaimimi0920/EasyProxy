# WRITER_UNKNOWN 升级恢复

`v1.0.23` 声明单一 SpringBok 修复动作 `continue-writer-idle-update`，用于恢复任务
`dc-11293c42b5134efabfefb45113f1691b` 的准确 `EASYPROXY_SBA_WRITER_UNKNOWN` 回执。
这是现有实例的应用修复，不是新建实例，也不是把失败任务改成成功。

## 为什么不能继续普通升级

SpringBok 仅对最近任务为 `succeeded` 或 `deployed-unverified` 的实例启用普通升级。
该父任务已消耗执行许可并返回 `unknown`，因此不符合未启动任务的“恢复升级”条件。
即使 EasyProxy 的写入占用已经解除，平台仍需通过正式 repair 新建关联执行记录。

## 修复边界

- 精确绑定父任务、run、请求摘要、结果摘要、执行器 SHA 和回执 artifact 摘要。
- 平台请求的 previous 是失败目标 `1.0.21`；实际云端 active 必须仍是 `1.0.20`。
- 在构建前和正式发布前执行只读检查：配置归属、准确父回执、writer 空闲、旧刷新恢复证据、
  稳定 manifest 及全部七项产物与公开字节一致。
- 任一检查失败即停止；不清锁、不接管新的 writer、不重放父任务、不跳过备份或迁移。
- 正式执行走正常 update：保留注册来源、读取实际 active 的生成输入，执行备份、数据保留、
  迁移、发布和业务验证；最终获得新的 repair 回执，旧 unknown 记录保留。
- 该动作只接受本次准确父回执及候选版本 `1.0.23`，不是通用恢复许可。

## 验收状态

请求边界、父回执读取、只读检查、正常 update 路由及相关部署回归已在本地验证。
生产升级是否成功，必须另行核对 SpringBok 新任务回执、实际发布版本及数据保留；
代码测试或发布 GitHub Release 不代表生产升级完成。
