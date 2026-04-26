# MiSub 节点净化配置迁移指南

本指南用于说明当前仓库中的 MiSub 应该如何从旧式前缀/净化配置，迁移到现在的：

- `defaultPrefixSettings`
- `defaultNodeTransform`
- `profile.prefixSettings`
- `profile.nodeTransform`

当前分支没有引入上游那套“自动一键迁移 UI 流程”，因此旧配置迁移以**人工确认**为主。

## 1. 当前配置模型

### 1.1 全局级

全局默认配置主要放在设置对象里：

- `defaultPrefixSettings`
- `defaultNodeTransform`

它们会影响“默认订阅输出”，也会作为订阅组未自定义时的继承来源。

### 1.2 订阅组级

订阅组高级设置里会保存：

- `prefixSettings`
- `nodeTransform`

其中：

- `nodeTransform = null` 表示继承全局设置
- `nodeTransform = { ... }` 表示当前订阅组使用自己的净化配置

## 2. 常见旧字段与新字段映射

如果你的旧备份或旧分支里还保留以下字段，可以按下面的方式迁移：

| 旧字段 / 旧概念 | 新位置 | 说明 |
| --- | --- | --- |
| `prefixConfig.manualNodePrefix` | `defaultPrefixSettings.manualNodePrefix` 或 `profile.prefixSettings.manualNodePrefix` | 迁到全局或 profile 级手动节点前缀 |
| `prependSubName` | `defaultPrefixSettings.prependGroupName` 或 `profile.prefixSettings.prependGroupName` | 表示是否把分组名加到输出节点名前 |
| 旧的全局 `nodeTransform` | `defaultNodeTransform` | 迁成全局默认净化配置 |
| 旧的 profile 级净化配置 | `profile.nodeTransform` | 保留为订阅组自定义配置 |

## 3. 推荐迁移顺序

### 第一步：先迁全局默认值

进入：

`设置 -> 全局设置`

先把这两类默认值补齐：

- 节点前缀设置
- 节点净化管道

这样即便某些订阅组还没有单独配置，也能先继承一套可工作的默认规则。

### 第二步：再迁特殊订阅组

进入每个订阅组的：

`高级设置 -> 净化配置来源`

按场景选择：

- 如果该组沿用统一规则，保留 `使用全局设置`
- 如果该组需要特殊重命名或排序，切到 `自定义`

### 第三步：检查模板是否覆盖旧前缀意图

很多旧配置依赖“简单前缀拼接”，而现在的模板重命名更强：

- 旧前缀更适合做“兜底标识”
- 模板更适合做最终输出格式

如果你已经启用了模板重命名，建议重新确认是否还需要：

- `手动节点前缀`
- `机场订阅前缀`
- `分组名称前缀`

## 4. 迁移示例

### 示例 A：旧配置只做简单前缀

旧目标：

- 手动节点前加 `手动节点`
- 订阅组节点前加分组名

迁移建议：

1. 在 `defaultPrefixSettings` 中保留基础前缀
2. 不启用模板重命名
3. 只在需要特殊输出的 profile 中单独启用 `nodeTransform`

### 示例 B：旧配置做统一命名

旧目标：

- 所有节点都变成 `地区-协议-序号`

迁移建议：

1. 启用 `defaultNodeTransform.enabled`
2. 开启模板重命名
3. 模板设置为：

```text
{emoji}{region}-{protocol}-{index}
```

4. 视情况关闭旧前缀类开关，避免重复前缀

## 5. 迁移后检查清单

迁完后建议逐项确认：

1. 默认订阅输出是否符合预期
2. 自定义 profile 是否仍然继承全局规则
3. 启用自定义的 profile 是否只影响自己
4. 节点名称中是否出现重复前缀
5. 模板编号是否按预期递增
6. 去重后是否误删了希望保留的不同协议节点

## 6. 备份导入注意事项

如果你使用旧备份 JSON：

- 先导入
- 再进入设置页逐项检查
- 最后重新保存一次

这样可以让当前分支把新的默认字段结构稳定写回。

尤其要注意：

- `defaultPrefixSettings`
- `defaultNodeTransform`
- `profile.prefixSettings`
- `profile.nodeTransform`

这些字段如果缺失，当前 UI 会在加载时补默认值，但仍建议保存一次，把结构固化下来。

