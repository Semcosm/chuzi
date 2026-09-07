# Universal Git Standard（UGS）Core v0.2

## 1. 目的

本文件定义一个**平台无关**、**Git 原生可实现**、**适用于个人与团队协作**的变更治理标准。

本标准的规范核心必须能够仅通过以下 Git 原生对象与机制表达和执行：

- refs 与 branches
- commits
- tags
- patch series
- commit trailers
- hooks

托管平台提供的 Pull Request、Merge Request、标签、评论、检查页等能力，均视为实现映射层，而不是规范本体。

平台适配器必须把平台事件转换为 Core 的 refs、commit、CR、patch、tag
和 trailer 输入；Core 校验不得把某一托管平台的 API、事件文件、URL 或
目录布局作为唯一前提。

## 2. 术语

### 2.1 Repository
一个 Git 仓库。

### 2.2 Branch Profile
仓库声明采用的分支档案。

合法取值仅有：

- `release-line`
- `continuous`

### 2.3 Topic Branch
承载一个单一变更主题的短生命周期分支。

### 2.4 CR（Change Request）
一次可审阅、可集成、可追踪的变更提案。

CR 必须落盘为 `cr/CR-*.md`；托管平台上的 PR/MR 只能作为该记录的审阅
视图，不得替代落盘记录。

CR 可映射为：

- 托管平台上的 PR / MR
- 一段 branch 与 commit range
- `git request-pull` 生成的文本
- `git format-patch` 生成的 patch series

### 2.5 Revision
同一 CR 的一个修订版本。

### 2.6 Trailer
位于 commit message 尾部的结构化键值行。

## 3. 规范级别

本标准使用以下术语：

- **MUST**：必须遵守
- **SHOULD**：强烈建议遵守；若偏离，必须有明确理由
- **MAY**：可选

## 4. 仓库级声明

### 4.1 Branch Profile
每个仓库 **MUST** 声明一个 Branch Profile。

### 4.2 主合并策略
每个仓库 **MUST** 声明且仅声明一种主合并策略：

- `merge-commit`
- `rebase-ff`
- `squash`

同一仓库在正常流程中 **MUST NOT** 混用多种主合并策略。

### 4.3 版本策略
每个仓库 **MUST** 声明一种版本策略：

- `semver`
- `calendar-versioning`
- `project-specific`

若采用 `semver`，仓库 **MUST** 明确其 public API 边界。

### 4.4 签名等级
每个仓库 **MUST** 声明一个签名等级：

- `release-tags-signed`
- `high-trust-commits-signed`

所有符合 UGS 的仓库，至少 **MUST** 满足 `release-tags-signed`。

## 5. 变更模型

### 5.1 一个主题，一个变更单元
每个非平凡改动 **MUST** 作为一个独立的 topic branch 或一个独立的 patch series 提交。

一个 CR **MUST NOT** 混入多个无关主题。

### 5.2 Topic Branch 生命周期
Topic branch **SHOULD** 为短生命周期。

Topic branch **MUST NOT** 充当长期个人集成分支。

### 5.3 CR 最小字段集
每个 CR **MUST** 至少包含以下字段：

- `base`
- `head` 或 `range`
- `title`
- `summary`
- `motivation`
- `test evidence`
- `risk`
- `rollback`
- `breaking change`（是 / 否）
- `backport target`（如适用）

### 5.4 CR 可移植性
CR **MAY** 映射到不同平台或不同提交介质，但其语义与最小字段集 **MUST** 保持一致。

### 5.5 多轮修订
若一个 CR 存在多轮修订，则其修订历史 **SHOULD** 保持可比较性。

仓库 **SHOULD** 保留 revision 编号，并在可行时提供轮次间差异说明。

## 6. 提交规范

### 6.1 提交粒度
每个 commit **MUST** 是一个小的、逻辑自洽的步骤。

每个 commit **SHOULD** 能够独立解释其存在意义。

### 6.2 提交消息结构
提交消息 **MUST** 使用以下结构：

```text
<type>(<scope>): <summary>

<body>

<footer trailers>
```

### 6.3 核心词表
核心 type 集 **MUST** 固定为：

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

### 6.4 可扩展词表
仓库 **MAY** 扩展 type，但仅当满足以下条件：

- 已在仓库规范中登记
- 不得与核心词表语义冲突
- 自动化工具至少完整支持核心词表

### 6.5 正文要求
当 summary 无法充分解释变更时，body **MUST** 说明：

- 为什么要改
- 改动影响了什么边界
- 有何兼容性影响
- 还有哪些未解决问题

### 6.6 Trailer 规则
结构化元数据 **MUST** 存放于 commit trailers 中。

推荐 trailer 集：

- `Signed-off-by:`
- `Reviewed-by:`
- `Tested-by:`
- `Co-developed-by:`
- `Fixes:`
- `Refs:`
- `Backport-to:`

### 6.7 历史改写
Topic branch 在进入共享保护分支之前 **MAY** rewrite history。

一旦提交已经进入受保护主线或公开发布线，仓库 **MUST NOT** 任意改写历史，除非仓库另有书面化恢复流程。

## 7. 审阅规范

### 7.1 审阅对象
审阅对象是 CR 或 patch series，而不是托管平台页面本身。

### 7.2 审阅结论与审阅过程分离
审阅信息分为两层：

- **审阅结论**：最终接受的 review / test 结论
- **审阅过程**：评论、讨论、修订建议、临时结论

### 7.3 审阅结论存放规则
最终接受的审阅结论 **SHOULD** 进入 commit trailers。

推荐键名：

- `Reviewed-by:`
- `Tested-by:`
- `Acked-by:`（若仓库采用）

### 7.4 审阅过程存放规则
审阅过程 **MAY** 保留在以下位置：

- PR / MR 评论区
- 邮件线程
- patch cover letter
- issue / discussion 页面

### 7.5 补充记录
仓库 **MAY** 使用 `git notes` 或等效补充记录保存非权威性注释。

## 8. 集成规范

### 8.1 集成前提
变更进入长期分支前，以下条件 **MUST** 同时成立：

- 目标分支明确
- 冲突已解决
- 测试结论明确
- CR 最小字段集完整

### 8.2 合并策略一致性
仓库 **MUST** 按声明的一种主合并策略持续执行。

### 8.3 保护分支规则
长期保护分支 **MUST** 拒绝未经审阅且非紧急路径的直接推送，除非仓库另有书面化紧急例外流程。

## 9. 发布规范

### 9.1 正式发布标签
正式 release **MUST** 使用 **signed annotated tag**。

### 9.2 Commit 签名
日常开发提交 **SHOULD** 使用签名提交。

对高信任或高供应链安全要求的仓库，commit 签名 **MAY** 上升为 **MUST**。

### 9.3 发布说明
每个正式 release **SHOULD** 提供 release notes，至少包括：

- 变更摘要
- 兼容性说明
- breaking changes
- 升级与回滚说明

## 10. 自动化与执行

### 10.1 自动化优先
任何可自动化检查的规则，都 **SHOULD** 通过自动化执行。

### 10.2 Hooks
仓库 **SHOULD** 提供受管理的 hooks 目录。

推荐最小 hooks 集：

- `commit-msg`
- `pre-push`
- `pre-receive` 或 `update`

### 10.3 平台独立性
平台特性 **MAY** 使用，但仓库 **MUST NOT** 将其作为任何一条规范规则的唯一实现方式。

## 11. 最小符合性清单

仓库仅在满足以下全部条件时，才可声称符合 UGS Core v0.2：

1. 已声明 Branch Profile
2. 已声明且仅声明一种主合并策略
3. 所有非平凡改动都走 topic branch 或 patch series
4. commit message 使用统一结构
5. 结构化元数据进入 trailers
6. 正式发布使用 signed annotated tag
7. 至少存在一层自动化执行机制
