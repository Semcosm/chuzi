# 架构与目录规划

## 目标

系统负责接收请求、调度账号、运行隔离的浏览器会话、保存必要状态，并通过 Matrix 向授权请求者发送结果。浏览器运行时应当使用每账号独立 Profile；“指纹”在本项目中首先表示会话隔离和实例标识，不作为规避第三方安全检测的功能目标。

## 逻辑组件

```text
Matrix Adapter ──> Request Service ──> Queue/Scheduler ──> Session Runner
       ^                  │                    │                 │
       └──── Status Notifier <──── State Store ┴──── Credential Store
```

- **Matrix Adapter**：验证房间/用户权限，解析命令，发送状态事件。
- **Request Service**：创建请求、幂等检查、权限校验和结果查询。
- **Queue/Scheduler**：按全局、账号和服务限制分配并发，处理超时与重试。
- **Session Runner**：启动和回收浏览器实例，绑定账号 Profile，报告运行结果。
- **State Store**：持久化账号、请求、状态转换和审计信息。
- **Credential Store**：提供加密凭证的读写，不向业务层暴露不必要的明文。
- **Status Notifier**：将领域事件转换为 Matrix 可读消息。

## 建议目录树

以下是目标目录，当前阶段只提交文档和 UGS 文件；随着实现推进再创建代码目录。

```text
.
├── cmd/                         # 可执行程序入口
│   └── service/
├── internal/
│   ├── account/                 # 账号实体与状态机
│   ├── browser/                 # Profile 生命周期与会话运行器
│   ├── credential/              # 凭证加密、轮换和访问接口
│   ├── queue/                   # 排队、租约、超时和重试
│   ├── matrix/                  # Matrix 适配器与事件格式化
│   ├── store/                   # 数据库、迁移和事务封装
│   ├── config/                  # 配置加载与校验
│   └── observability/           # 日志、指标、审计
├── migrations/                  # 数据库迁移
├── tests/                       # 集成测试与端到端测试
├── configs/                     # 脱敏示例配置
├── deploy/                      # 容器、服务编排和运行时配置
├── docs/                        # 项目文档与 ADR
├── scripts/                     # 开发和治理脚本
├── cr/                          # UGS 变更记录
├── .githooks/                   # UGS Git hooks
└── .ugs/                        # UGS 版本与策略清单
```

## 关键边界

浏览器模块不能直接决定对外业务状态；它只能报告运行事实，由账号状态机根据事件和持久化数据完成状态转换。Matrix 模块不能直接操作凭证，只能提交请求和消费脱敏后的领域事件。
