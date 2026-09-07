# UGS Release Policy v0.2

## 1. 目标

本文件定义 UGS 仓库的发布对象规则。

## 2. 发布对象

正式发布 **MUST** 以 signed annotated tag 表示。

lightweight tag **MUST NOT** 作为正式公开发布的规范标识。

## 3. 发布前置条件

在创建正式 release tag 之前，仓库 **MUST** 确保：

- 目标提交明确
- 发布版本号明确
- release notes 已准备
- 兼容性影响已知
- 在适用场景下，已提供回滚或恢复说明

## 4. 版本策略

每个仓库 **MUST** 声明一种版本策略：

- `semver`
- `calendar-versioning`
- `project-specific`

若采用 `semver`，仓库 **MUST** 明确：

- 什么构成 public API
- 什么构成向后兼容的功能新增
- 什么构成向后兼容的修复
- 什么构成 breaking change

## 5. 签名 Tag 要求

正式 release tag **MUST** 被签名。

仓库 **MUST** 明确：

- 谁被允许签发 release
- release key 如何轮换
- 密钥泄露或失效时如何吊销
- 下游用户如何进行验证

## 6. Commit 签名等级

### 6.1 基线等级
在 UGS 基线符合性下，日常开发提交 **SHOULD** 使用签名提交。

### 6.2 高信任等级
对高信任或高供应链安全要求仓库，所有进入保护分支的 commits **MAY** 被要求签名。

若启用这一更高等级，仓库 **MUST** 同时明确：

- 可信签名者登记机制
- 签名者移除流程
- 机器人或自动化提交的签名策略
- 密钥丢失后的恢复流程

## 7. Release Notes

每个正式发布 **SHOULD** 提供 release notes，至少包括：

- 变更摘要
- 兼容性说明
- breaking changes
- 迁移说明（如适用）
- 回滚说明（如适用）

## 8. 维护发布

采用 `release-line` 的仓库 **SHOULD** 区分：

- 正常前进发布
- 维护发布
- hotfix 发布
- 仅 backport 发布

release notes **SHOULD** 显式体现该区分。

## 9. 验证指引

仓库 **SHOULD** 为下游用户提供验证指引。

至少应包括：

- 如何验证 release tag
- 可信 release key 在何处发布
- 验证失败时如何处理

## 10. 最小发布策略模板

```text
Versioning: semver
Formal Release Object: signed annotated tag
Release Signers: release-managers
Commit Signing Level: release-tags-signed
Release Notes Required: yes
Verification Guide Published: yes
Maintenance Release Distinction: yes
```
