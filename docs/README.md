# 项目文档

## 文档地图

| 文档 | 内容 |
| --- | --- |
| [roadmap.md](roadmap.md) | 长期演进阶段、依赖关系和验收门槛 |
| [architecture.md](architecture.md) | 系统边界、模块职责和建议目录树 |
| [account-state-machine.md](account-state-machine.md) | 账号业务状态、转换条件和异常处理 |
| [security.md](security.md) | 凭证、浏览器 Profile、日志和权限安全 |
| [matrix-api.md](matrix-api.md) | Matrix 房间命令、事件和状态通知约定 |
| [operations.md](operations.md) | 配置、部署、备份、监控和故障恢复 |

## 术语

- **账号（Account）**：一个可被授权登录的云游戏服务账号。
- **会话（Session）**：账号对应的浏览器运行实例及其持久化 Profile。
- **请求（Job）**：外部请求者提交的一次登录或状态查询任务。
- **队列（Queue）**：根据并发限制调度请求的组件。
- **业务状态（Business Status）**：对外可见的账号处理状态，不等同于底层浏览器进程状态。

## 推荐阅读顺序

先阅读路线图了解阶段依赖，再阅读架构和状态机，最后阅读安全、Matrix
接口与运维约束。实现阶段应以状态机作为领域模块的单一事实来源。
