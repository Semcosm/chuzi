# Sequential Task Prompts

Run these sessions in order in the shared feat/ui-refactor branch. Do not run
phases 2-6 concurrently because they edit overlapping Windows UI files. Every
session should read AGENTS.md, the relevant project docs, and all of docs/ui/
before editing.

## Session 1 - Core projection contract

~~~text
你负责 CHUZI UI 重构的第一阶段：为 Session-first UI 定义并实现最小的只读 Core 投影契约。

先阅读 AGENTS.md、README.md、docs/architecture.md、docs/account-state-machine.md、
docs/storage.md、docs/security.md、docs/operations.md，以及 docs/ui/ 全部文档。
重点查看 internal/coreapi、internal/core、internal/coretransport、internal/store、
cmd/launcher 和现有 Core API 测试。

目标：让 Windows UI 能以稳定、脱敏、可分页/有界、确定性排序的方式读取 Session/Job/Account 列表。
现有 Core 只有 get_account/get_request 等单项查询；Store 已有 ListRequests。请先判断最小方案：
优先评估 list_requests + 必要的 account projection，或一个由 Core 派生的 list_sessions projection。
不要把 store.Request、bbolt、Profile 路径、凭证、房间 ID、原始账号标识或浏览器事实暴露给客户端。
不要虚构 game、region、runtime、duration 等当前服务无法提供的字段；不可用字段应省略或明确 unavailable。

实现内容：
1. 更新 internal/coreapi DTO/API、internal/core 服务和 internal/coretransport wire/method list；
2. 在 store 侧复用或补充确定性列表读取，限制 limit/offset 或等价边界；
3. 更新 docs/core-api.md，并补充 Core、transport、redaction、ordering、empty/error 的契约测试；
4. 保持旧方法兼容，UI 仍只能经 launcher/core-call 调用；
5. 不修改 ui/windows 视觉代码。

验收：go test ./...、go vet ./...、git diff --check；报告 changed files、DTO 示例、
安全/脱敏决定、测试命令和已知限制。不要顺手重写业务状态机或队列语义，不要提交 commit。
~~~

## Session 2 - Design system and shell

~~~text
你负责 CHUZI UI 重构的第二阶段：Rust + Slint 设计系统和持久化应用 shell。

先阅读 AGENTS.md、README.md、docs/architecture.md、docs/security.md、docs/operations.md，
以及 docs/ui/README.md、audit.md、implementation-plan.md、design-principles.md、
information-architecture.md、interaction-model.md、visual-system.md、component-system.md、
screen-specs.md，并查看 docs/ui/references/chuzi-ui-reference.png。

只修改 ui/windows 的 presentation layer；不要修改 account、queue、credential、browser worker、
Core 业务语义或 launcher 边界。保留 main.rs 中现有异步 launcher/core-call、错误分类和诊断同意；
保留独立的 RDP runtime boundary，但移除 Overview、Accounts、Tasks、Adapters、Settings、RDP login
等旧主窗口页面和兼容路由。不要把 IPC/Store 逻辑搬进 Slint。

先建立可复用 token 和组件：semantic colors（Light/Dark/System）、materials、typography、
spacing、radius、shadow、transitions、Sidebar、Inspector、Surface、Sheet、Overlay、Button、
StatusIndicator、ListRow、MoreMenu、Empty/Loading/Error 状态。然后实现持久化的
Titlebar + Sidebar + ContentHost + InspectorHost shell。

删除旧页面的兼容入口，不要开始完整重写 Accounts/Adapters/Tasks。不要增加 Dashboard、KPI 卡片、
DataGrid 或装饰性玻璃。先解决空间层级、响应式优先级和键盘焦点。

验收：cargo test --manifest-path ui/windows/Cargo.toml；运行 layout_snapshot 至少覆盖 800x600、
1120x760、1440x900，并检查 shell 不为空、Inspector 在窄窗口可收起；执行 git diff --check。
报告组件清单、changed files、截图路径、Light/Dark/resize/focus 检查结果和限制。不要提交 commit。
~~~

## Session 3 - Sessions vertical slice

~~~text
你负责 CHUZI UI 重构的第三阶段：完成 Sessions 的第一个可用 vertical slice。

先阅读 docs/ui/ 全部文档和 Session 1/2 的变更；检查新的 Core list projection 的实际 DTO/wire 名称，
不要猜接口。继续遵守 launcher/Core 边界，UI 不得读取 bbolt、credentials、Profile 或 named pipe。

实现：Sessions 导航项、header/subtitle、搜索（若后端不支持则明确为本地过滤已加载安全投影）、
All/Running/Queued/Failed 过滤、确定性列表加载、空/加载/错误状态、SessionRow、选中态、键盘导航、
Session Inspector、Open/Cancel/Retry 等基于 Core 状态的 contextual action 和 More menu。

Session 目前若只是 request/account 派生投影，必须在 UI 文案和模型中保持诚实：不可用的游戏、地区、
运行时、时长字段不要用截图或 fixture 冒充生产数据。Browser view 仍是一次性只读 get_browser_view，
不能增加鼠标/键盘控制。选择行后 Inspector 原地更新，不跳详情页、不重载整个窗口。

把列表/选择/Inspector 的 view-model 胶水放在 Rust，Slint 组件只渲染属性并发 callback。为 running、queued、
idle/no-request、failed、unavailable、empty、selected、compact Inspector 建立确定性 snapshot fixtures；
fixture 不得进入生产启动路径。

验收：cargo test --manifest-path ui/windows/Cargo.toml；layout_snapshot 覆盖上述状态和三种固定视口；
如接入新 Core 契约，再运行 go test ./...、go vet ./...；检查键盘焦点、More menu、action disabled、
Light/Dark 和 Inspector collapse。报告截图/路径、交互路径、changed files 和已知限制。不要提交 commit。
~~~

## Session 4 - Accounts, Jobs, and Adapters

~~~text
你负责 CHUZI UI 重构的第四阶段：把 Accounts、Jobs、Adapters 接入同一套 list + selection + Inspector 语言。

先读 docs/ui/ 全部文档、现有 Core/launcher DTO 和 Session vertical slice。不要新增后端业务语义，
不要让 UI 决定 account state、queue retry、lease 或 adapter trust。

实现：
1. Accounts 作为授权账号对象列表，展示安全的 redacted identity、state、last activity/current request
   （仅 Core 提供时），并复用 Account Inspector；
2. Jobs 作为 request/queue 对象列表，展示 state、attempt、deadline/failure（仅 DTO 提供时），复用
   Job Inspector；queue position 只能来自可信 Core projection；
3. Adapters 复用 Adapter Inspector，保留 install/update/trust/untrust/enable/disable/remove 的显式
   launcher action，把低频/危险操作收进 More menu 和确认 sheet；
4. 删除或收敛旧的 page-local GroupBox/button 堆叠，但保留功能等价和 redacted errors；
5. 不显示 credentials、raw path、room ID、token 或内部错误文本。

验收：现有 UI 与 Core/launcher 测试全部通过；layout_snapshot 覆盖每种 object 的 selected/empty/error、
destructive confirmation、dark mode、compact width；验证所有操作仍经 launcher facade。报告改动和任何
无法由当前 Core 提供的字段。不要提交 commit。
~~~

## Session 5 - Session Workspace and HUD

~~~text
你负责 CHUZI UI 重构的第五阶段：实现 Session Workspace 的空间和轻量 HUD。

先阅读 docs/ui/interaction-model.md、screen-specs.md、component-system.md、README.md 中 RDP/browser
约束，以及 ui/windows/src/desktop_rdp.rs 和现有 browser-view callback。不要修改 FreeRDP 安全边界，
不要增加任意 URL、CDP endpoint、Profile path、鼠标/键盘控制或凭证持久化。

实现：Back to Sessions、Session identity/status、More、默认嵌入的 runtime surface、底部状态栏和
hover/edge 显示的 HUD，并提供将同一会话 Float 到 `DesktopRdpWindow`、再 Dock 回主窗口的入口。
游戏/RDP/只读 browser surface 是主体，控制默认隐藏；resolution/FPS/duration 只有 Core/runtime
真正提供时才展示，否则省略或标 unavailable。先抽出 host-neutral RDP runtime，保证 Dock/Float 不会
创建第二个 worker 或隐式重连；保留 credential clearing、diagnostic consent、performance
HUD/recording 的安全语义。不要恢复旧 RDP login 页面或旧主窗口路由。

验收：cargo test --manifest-path ui/windows/Cargo.toml；layout_snapshot 覆盖 workspace connected/connecting/
failed、HUD hidden/visible、compact width、Light/Dark；Windows 构建路径可解释（本机非 Windows 时不要假称
FreeRDP 已运行）。报告截图/路径、输入和凭证边界检查、changed files 和限制。不要提交 commit。
~~~

## Session 6 - Settings, accessibility, and visual QA

~~~text
你负责 CHUZI UI 重构的第六阶段：设置分组、无障碍、主题和系统性视觉 QA。不要新增业务功能。

先阅读 docs/ui/ 全部文档、当前 shell/session workspace 和 ui/windows/README.md。

如本阶段产品范围重新引入设置入口，把它组织为 General、Appearance、Accounts、Sessions、Automation、
Notifications、Network、Security、Advanced；把 CDP/backend/storage/debug/worker 等技术项放到 Advanced
或 diagnostics。不要恢复旧 Settings 页面。完成 Light/Dark/System
语义 material 对称、focus/keyboard/tab order、logical labels、minimum hit target、high-contrast-safe
separator/text、reduced-motion 路径和窗口缩放优先级（Content > Inspector collapse > Sidebar collapse）。

只修视觉和交互一致性：alignment、spacing tokens、typography hierarchy、density、sidebar/inspector proportion、
material layering、border/shadow strength、button hierarchy、status indicators、hover/focus、empty/loading/error。
不要添加渐变、霓虹、KPI cards、永久 blur 或复制 Apple 品牌。

验收：cargo test --manifest-path ui/windows/Cargo.toml；layout_snapshot 覆盖 800x600、1120x760、1440x900，
Light/Dark、selected Inspector、menu/sheet/focus；若可用，运行 Windows build/installer smoke；报告逐项 QA 结果、
截图路径、known limitations。不要提交 commit。
~~~

## Session 7 - Hardening and handoff

~~~text
你负责 CHUZI UI 重构最后阶段：只做集成检查、清理和交付记录。

阅读 AGENTS.md、README.md、docs/ui/ 全部文档、docs/core-api.md、ui/windows/README.md，以及此前所有变更。
检查是否仍存在绕过 launcher/Core、直接读取 Store/credentials/Profile、虚构生产 Session 数据、UI 重复实现
业务状态机、残留旧页面控件、旧兼容路由或未使用 token 的问题。

执行并修复必要的集成问题：go test ./...、go vet ./...、cargo test --manifest-path ui/windows/Cargo.toml、
cargo run --manifest-path ui/windows/Cargo.toml --features layout-snapshot --example layout_snapshot -- --output dist/windows-layout、
./scripts/test_build_contract.sh、git diff --check。不要扩展范围，不要重写后端。

更新 ui/windows/README.md（如果实现行为已改变）和 docs/ui/ 中的 known limitations/decision notes，
记录 changed files、architecture changes、UI decisions、test/build results、snapshot/visual QA result、
security/redaction checks 和 rollback notes。不要提交 commit。
~~~
