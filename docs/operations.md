# 部署与运维

## 配置分类

- 普通配置：服务地址、并发上限、超时、重试策略。
- Secret 配置：数据库密码、Matrix access token、凭证加密主密钥。
- 运行数据：数据库、浏览器 Profile、审计日志和待发送事件。

普通配置提供 `configs/example.*` 示例，Secret 不进入 Git。生产环境至少限制服务账户、数据库和 Profile 目录的文件权限。

## 最低运行要求

1. 可持久化的状态数据库。
2. 可持久化且受限的凭证/会话存储。
3. Matrix Bot 用户和授权房间。
4. 浏览器运行时及其资源限制。
5. 日志轮转、健康检查和任务租约回收。

## 构建与发布目标

GitHub Actions 负责远端构建，不要求开发者在本地安装完整的发布工具链。构建使用 Go 控制服务和 Node.js Worker 两套锁定的工具链，目标矩阵为：

| Target | Output | Build mode |
| --- | --- | --- |
| `windows-amd64` | `.zip` | Windows native runner |
| `linux-amd64` | `.tar.gz` | Linux native runner |
| `linux-arm64` | `.tar.gz` | Linux cross-build; native runtime coverage pending |
| `darwin-arm64` | `.tar.gz` | Apple Silicon macOS runner |

构建命令由以下脚本定义：

```bash
scripts/build.sh <target> [version]
scripts/package.sh <target> <version>
```

Windows runner 使用对应的 `*.ps1` 脚本。构建产物必须包含 Go 服务、Worker 文件和 `build-manifest.json`，并生成 SHA256 校验文件。CI smoke test 只使用本地 Worker 和测试协议，不使用真实云游戏账号或生产凭证。

当前 Worker 只提供协议和生命周期验证；Playwright/Chromium 适配必须在增加依赖、浏览器下载或原生模块前单独提交 CR，并为四个发布目标记录运行覆盖范围。

## 运维检查

- 部署前运行 `scripts/validate_policy_manifest.sh`。
- 定期检查悬挂任务、过期凭证、Profile 磁盘占用和 Matrix 发送失败数。
- 备份状态数据库前确认凭证密文与密钥分离保存。
- 发布遵循 UGS 的版本和变更记录规则。
