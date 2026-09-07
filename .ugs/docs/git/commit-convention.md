# UGS Commit Convention v0.2

## 1. 目标

本文件定义 UGS 仓库使用的 commit message 规范。

该规范旨在提供：

- 对人类可读的历史
- 对机器可读的语义
- 可自动化处理的结构化元数据
- 可迁移、可归档的审阅与追踪痕迹

## 2. 标准格式

所有 commit message **MUST** 遵循以下格式：

```text
<type>(<scope>): <summary>

<body>

<footer trailers>
```

`<scope>` 为推荐项；若不写 scope 反而更清晰，则可省略。

## 3. Summary 规则

summary 行 **MUST**：

- 以合法的 commit type 开头
- 简洁且具体
- 描述改动本身，而不是工单流转状态

summary 行 **MUST NOT**：

- 混入无关问题讨论
- 写入 review 讨论内容
- 重复本应放在 body 或 trailers 的信息

## 4. 核心词表

所有 UGS 仓库 **MUST** 支持以下核心 type：

- `feat`
- `fix`
- `refactor`
- `docs`
- `test`
- `build`
- `ci`
- `chore`
- `perf`
- `revert`

## 5. 扩展词表

仓库 **MAY** 定义扩展 type。

扩展 type **MUST**：

- 在仓库本地规范中登记
- 不得与核心词表产生歧义重叠
- 在脱离平台上下文后仍可被人类理解

可接受的示例：

- `backport`
- `deps`
- `security`

## 6. Body 规则

body **SHOULD** 回答以下问题：

- 为什么需要这个改动
- 改动影响了什么边界
- 做了什么取舍
- 会带来什么兼容性影响

若变更影响外部行为，则 body **MUST** 说明兼容性与迁移信息。

## 7. Trailer 规则

结构化元数据 **MUST** 出现在 trailers 中。

推荐 trailer 键：

- `Signed-off-by:`
- `Reviewed-by:`
- `Tested-by:`
- `Co-developed-by:`
- `Fixes:`
- `Refs:`
- `Backport-to:`

仓库 **MAY** 增加其他 trailer 键，但必须登记。

## 8. Breaking Changes

若一个 commit 引入 breaking change，仓库 **MUST** 至少通过以下方式之一显式暴露：

- 在消息约定中使用明确的 breaking 标记
- 在 trailer 或 body 中显式声明不兼容性
- 在发布规范中确保该不兼容性不会被静默遗漏

## 9. 示例

### 9.1 Feature

```text
feat(parser): support inline schema overrides

Allow inline schema override blocks during configuration parsing.
This reduces duplication in multi-environment config trees.

Refs: CFG-142
Reviewed-by: Alice Example <alice@example.com>
Tested-by: Bob Example <bob@example.com>
```

### 9.2 Fix

```text
fix(auth): reject expired refresh tokens

Refresh token validation previously accepted tokens beyond the
configured expiry boundary under clock skew.

Fixes: #128
Reviewed-by: Alice Example <alice@example.com>
Tested-by: Bob Example <bob@example.com>
```

### 9.3 Backport 扩展示例

```text
backport(auth): carry refresh token expiry fix to maint/2.4

Backport the expiry validation fix without the unrelated refactor
that exists on main.

Backport-to: maint/2.4
Reviewed-by: Alice Example <alice@example.com>
```
