# UGS Branch Profiles v0.2

## 1. 目的

本文件定义 UGS 下唯一合法的两种分支档案：

- `release-line`
- `continuous`

每个仓库 **MUST** 二选一。

## 2. Profile：`release-line`

### 2.1 适用场景
适用于：

- 同时维护多个已发布版本
- 经常执行 backport
- 需要 hotfix 流程
- 需要区分维护线与开发主线

### 2.2 长期分支
最小长期分支集合：

- `main`
- `maint/<major.minor>`
- 可选 `next`

### 2.3 短期分支
推荐命名：

- `feat/<scope>-<slug>`
- `fix/<scope>-<slug>`
- `hotfix/<target>-<slug>`
- `backport/<target>-<slug>`
- `release/<version>`

### 2.4 起分规则
Topic branch **MUST** 从其最终会接收该变更的**最老目标线**起分。

### 2.5 修复传播规则
若一个修复适用于多个受支持分支，则该修复 **MUST**：

1. 先落到最老仍受支持的目标线
2. 再逐级向更新分支传播

### 2.6 新功能规则
新功能 **MUST NOT** 直接进入维护线。

### 2.7 默认合并策略
`release-line` 的默认主合并策略 **MUST** 为 `merge-commit`。

### 2.8 Release 分支
`release/<version>` 分支 **MAY** 使用，但仅限以下情况：

- 存在冻结窗口
- 需要将“继续开发”和“准备发布”隔离
- 发布前需要一轮或多轮 QA、打包或文档修正

### 2.9 禁止事项
以下行为被禁止：

- 新功能直接进入维护线
- 先把修复打到新线，再回填老线，且无明确理由
- 对公开维护线做常态化 rebase

## 3. Profile：`continuous`

### 3.1 适用场景
适用于：

- 只有一条真实活跃开发主线
- 发布频繁
- 很少维护历史版本分支
- 更关注快速集成和低分支治理成本

### 3.2 长期分支
最小长期分支集合：

- `main`

可选：

- `stable`，但仅在确有短期稳态缓冲需求时启用

### 3.3 短期分支
推荐命名：

- `feat/<scope>-<slug>`
- `fix/<scope>-<slug>`
- `chore/<scope>-<slug>`

### 3.4 起分规则
所有 topic branch **MUST** 从 `main` 起分，除非仓库显式声明 `stable` 为短期缓冲线。

### 3.5 集成规则
一个完成的 topic branch **MUST** 在准备就绪后尽快回到 `main`。

### 3.6 develop 分支规则
采用 `continuous` 的仓库默认 **MUST NOT** 保留长期 `develop` 分支。

### 3.7 默认合并策略
`continuous` 的默认主合并策略 **MUST** 为 `rebase-ff`。

### 3.8 发布规则
正式发布默认直接从 `main` 打 tag，除非仓库显式声明不同的短期稳态分支。

正式发布标签仍 **MUST** 为 signed annotated tag。

### 3.9 禁止事项
以下行为被禁止：

- 一边声称单主线 continuous，一边长期维护 `develop`
- 用长期个人私有分支替代 topic branch
- 对已公开主线历史做常态化 rebase

## 4. 命名建议

分支命名属于建议标准化，而不是核心不变量。

真正的硬约束是：

- 从哪起分
- 合回哪里
- 哪些分支是长期分支
- 哪些分支受保护
- 哪些分支允许 rewrite history

## 5. 选择指引

满足以下任意两项时，优先选择 `release-line`：

- 同时维护多个已发布版本
- backport 是常规需求
- API / ABI 稳定性要求高
- 发布通常伴随冻结窗口
- hotfix 是高频需求

满足以下任意两项时，优先选择 `continuous`：

- 只有一条真实开发主线
- 发布频繁
- backport 很少
- 希望将分支治理成本压到最低
- feature 生命周期通常较短

## 6. 仓库声明模板

```text
UGS Profile: continuous
Merge Strategy: rebase-ff
Versioning: semver
Signing Level: release-tags-signed
Core Commit Types: feat, fix, refactor, docs, test, build, ci, chore, perf, revert
Extended Commit Types: <none>
Review Conclusion Policy: trailers
Review Discussion Policy: CR comments
Hooks Path: .githooks
```

```text
UGS Profile: release-line
Merge Strategy: merge-commit
Versioning: semver
Signing Level: high-trust-commits-signed
Core Commit Types: feat, fix, refactor, docs, test, build, ci, chore, perf, revert
Extended Commit Types: backport
Review Conclusion Policy: trailers
Review Discussion Policy: CR comments
Hooks Path: .githooks
```
