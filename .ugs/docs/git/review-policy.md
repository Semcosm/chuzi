# UGS Review Policy v0.2

## 1. 目标

本文件定义 UGS 仓库中 review 证据的表示方式。

本文件的核心设计规则为：

- **review 结论进入 Git**
- **review 讨论可留在托管平台或邮件线程**

## 2. 审阅层次

### 2.1 审阅结论
最终接受的 review 结果。

示例：

- 允许集成
- 技术审阅通过
- 测试验证通过
- 由子系统负责人确认

### 2.2 审阅过程
通向最终结论的互动过程。

示例：

- 评论
- requested changes
- 设计讨论
- revision 说明
- 临时性反对意见

## 3. 权威存放位置

### 3.1 最终审阅结论
最终审阅结论 **SHOULD** 存放于 commit trailers 中。

推荐键：

- `Reviewed-by:`
- `Tested-by:`
- `Acked-by:`

### 3.2 讨论过程存放位置
审阅讨论 **MAY** 保留在：

- PR / MR 评论区
- 邮件线程
- issue / discussion 系统
- patch cover letter

### 3.3 补充存放位置
仓库 **MAY** 使用 `git notes` 保存有帮助但非权威性的补充注释。

## 4. 审阅要求

在变更进入长期保护分支之前，CR **MUST** 满足仓库定义的全部适用审阅要求。

仓库至少 **MUST** 明确：

- 是否要求人工 review
- 是否要求测试证据
- 对敏感路径是否要求 maintainer acknowledgment

## 5. 审阅粒度

仓库 **MUST** 选择一种主审阅粒度：

- **change-level**：review 作用于整个 CR
- **commit-level**：review 作用于单个 commit

若仓库采用 commit-level review，则最终接受结论 **SHOULD** 反映到对应 commits 上。

若仓库采用 change-level review，但后续发生 rewrite 或 squash，则最终集成结果 **SHOULD** 在可行时保留权威性 review trailers。

## 6. 多轮修订处理

若一个 CR 存在多轮 revision，则：

- revision 讨论 **MAY** 保留在 CR 或邮件线程中
- 最终接受状态 **SHOULD** 反映到最终集成结果上
- 已被后续轮次取代的审阅结论 **MUST NOT** 被继续当作当前结论展示，除非显式重申

## 7. 紧急路径

仓库 **MAY** 为紧急修复定义审阅绕过路径。

若定义该路径，仓库 **MUST** 明确：

- 谁可以绕过常规 review 时序
- 绕过行为如何记录
- 合并后 review 如何补做
- rollback 权限如何处理

## 8. 推荐 Trailer 集

若仓库希望采用简单默认值，则推荐使用：

- `Reviewed-by:` 表示技术审阅结论
- `Tested-by:` 表示测试确认
- `Acked-by:` 表示子系统或责任人确认

## 9. 最小审阅策略模板

```text
Review Model: change-level
Human Review Required: yes
Test Evidence Required: yes
Maintainer Ack Required for Sensitive Paths: yes
Review Conclusion Storage: trailers
Review Discussion Storage: CR comments
Emergency Path: defined
```
