# 三页 UI、Agent 与 package 对抗式审查

日期：2026-11-07（Asia/Shanghai）。范围：当前工作区的首页、项目库、制作工作台，以及 Agent / avatar package 执行链路。

后续：用户已要求修复。本文保留审查时的发现和失败证据；修复与部署验收见 [修复记录](D:/pro/ai_cat/avatar-agent/docs/reviews/2026-11-07/fixes.md)。下方问题描述、代码行号和“未修改生产代码”均指审查阶段。

结论：确认 7 个问题，2 个 P1、5 个 P2。8 个针对正确行为的隔离断言失败，其中两个断言对应同一个停止失效问题。建议先修复订单保护绕过和停止后的工具执行，再修复其余问题。

本次只新增本目录的审查报告、复现代码与证据，没有修改生产代码，没有更新部署，没有调用真实收费供应商。复现时临时放入源码目录的测试文件已删除。

## 已确认的问题

### R1 · P1 · 保存配置后可绕过未知订单保护，再次提交配音

位置：[agent_packages.go:220](D:/pro/ai_cat/avatar-agent/backend/internal/server/agent_packages.go:220)，同文件 250–251 行；下层 `create` 的 `ensureIdle` 只拦截 running。

触发：某段配音提交结果未知，运行处于 `submission_unknown`。直接调用 `avatar.voice` 会被拦截；随后调用一次 `avatar.configure`（空对象也可以），再次调用 `avatar.voice`。

实测：配置把 revision 从 1 增至 2；状态拦截位于“运行版本等于草稿版本”的条件内部，被跳过。新运行丢失原步骤的未知订单信息，向本地模拟供应商发送了 1 次新提交请求。结果为 `new_run=true new_revision=2 provider_requests=1`。

影响：原订单可能已被供应商受理，用户仅保存设置便可能重复付费、生成重复任务。这里确认的是新提交请求已经发生，未对真实供应商产生费用。

建议：在统一的运行创建入口检查项目是否存在尚未核对的订单，独立于草稿 revision；配置更新不应解除订单保护。恢复必须先核对或复用原订单。

复现：`TestAdversarialUnknownOrderAfterConfigure`。

### R2 · P1 · 停止回复后仍执行当前模型返回的工具

位置：[session.go:420](D:/pro/ai_cat/avatar-agent/backend/internal/agent/session.go:420)，以及 390–398 行。

触发一：模型调用尚未返回时执行 `Abort`，随后模型返回 `avatar_configure`。触发二：同一响应包含两个工具，第一个执行结束前停止。

实测：第一种情况下配置工具仍被调用，最后回复却为“已中止。”；第二种情况下，停止后批次内的第二个配置工具仍执行。

原因：中止检查只在模型循环开头；模型返回后和工具批次内部没有检查。`TurnAbort.Context()` 也没有连接到传入模型和工具的请求 context。

影响：用户停止后仍可能保存配置或启动后续制作。复现使用记录调用的 package host，确认了停止后工具派发；没有声称已提交真实付费制作。

建议：为每轮建立与请求关联的取消 context，模型返回后、每个工具执行前检查取消状态；让工具入口在开始副作用前再次校验。已提交的供应商订单仍须保留、核对。

复现：`TestAdversarialAbortBeforeLLMReturns`、`TestAdversarialAbortBetweenTools`。

### R3 · P2 · 同一项目并发聊天会串用工具轮次和请求标识

位置：[session.go:256](D:/pro/ai_cat/avatar-agent/backend/internal/agent/session.go:256)，工具闭包注册在 273 行。

触发：在两个标签页向同一项目发送不同 requestId 的消息；A 的模型响应较慢，B 先完成。

实测：放行 A 的模型响应后，A 的配置工具以 `turn-B/avatar.configure` 调用 host，预期应是 `turn-A/avatar.configure`。

原因：requestId 去重仅处理相同请求；不同请求共用 SessionState。B 覆盖 A 尚在使用的 `turnID`、`abort` 和工具表。短暂加锁初始化不足以隔离整个异步轮次。

影响：SSE 工具事件与用户消息错配，执行包会使用错误的幂等请求标识，停止信号也不再独立。

建议：按项目串行处理聊天或显式拒绝并发轮次；若需要并行，轮次状态、工具表、取消信号与事件归属必须独立。

复现：`TestAdversarialConcurrentTurnIdentity`。该测试用通道控制先后顺序，不依赖随机竞态。

### R4 · P2 · 服务重启或内存会话淘汰后，聊天历史返回空列表

位置：[gin_handler.go:288](D:/pro/ai_cat/avatar-agent/backend/internal/agent/gin_handler.go:288)。

触发：数据库中有历史，但内存不存在该项目的 Session；工作台请求 history。

实测：HTTP 200、messages 为 0 条，持久层 `Load` 调用次数为 0。提前返回发生在数据库读取之前。建立 SSE 连接也不会创建或加载聊天 Session。

影响：用户打开工作台会看到空白聊天区，误以为历史丢失；数据本身仍在数据库。重新发消息才会触发 Session 的 hydrate。

建议：优先读持久层；只有无持久层时才以内存会话兜底，不能把“没有内存会话”等同于“没有历史”。

复现：`TestAdversarialHistoryWithoutMemorySession`。

### R5 · P2 · 修改口播稿被后续视频制作条件阻挡

位置：[agent_package_edits.go:175](D:/pro/ai_cat/avatar-agent/backend/internal/server/agent_package_edits.go:175)。

触发：用户先制作文案，选择数字人出镜但尚未上传人物照片；在文案审核阶段生成并确认文案修改提案。

实测：F03 所需条件检查通过，提案可以生成；确认时报“请先上传人物照片”。原因是所有编辑提案统一按 F12 校验，会提前要求照片、配音或视频服务，而文案编辑后的下一步本应重新审核文案。

影响：用户无法按“先改稿，再准备人物素材”的步骤推进，分步骤 package 的能力在提案确认处被限制。

建议：按提案实际修改节点和下一个审核边界校验、保存执行范围；到达配音或视频节点时再检查相应前置条件。

复现：`TestAdversarialScriptProposalWithoutVideoPrerequisites`。

### R6 · P2 · 项目重命名后，保存制作设置会恢复旧名称

位置：[handlers_auth_projects.go:597](D:/pro/ai_cat/avatar-agent/backend/internal/server/handlers_auth_projects.go:597)，以及 [StudioWorkspace.tsx:106](D:/pro/ai_cat/avatar-agent/frontend/src/features/agent/StudioWorkspace.tsx:106) 的设置保存逻辑。

触发：从项目库把项目改名；再进入制作设置，仅修改画幅并保存。

实测：通用更新接口修改 `Project.Name`，没有同步 `avatar_v2.title`；工作台读取旧 draft title 并随设置提交，`avatar.configure` 随后把名称改回。测试得到 `draft title="旧名称" final name="旧名称"`。

影响：用户成功改名后，另一次无关设置修改会覆盖这个修改。

建议：统一项目名称的数据来源，或在同一事务内同步名称与草稿标题，并纳入版本校验。

复现：`TestAdversarialRenameThenSaveSettings`，使用实际通用项目更新 handler 与 package host。

### R7 · P2 · 附件上传失败后无法移除或替换错误文件

位置：[StudioHome.tsx:50](D:/pro/ai_cat/avatar-agent/frontend/src/features/agent/StudioHome.tsx:50)，项目创建和异常处理在 35–41 行。

触发：首页选中无法解码的图片，发送创意；项目创建成功，上传接口返回格式/解码错误。

实测：错误信息显示后，移除文件按钮仍为 `disabled`。创建项目后 `draft` 一直存在，文件移除、添加、文本、制作类型和画幅均被 `busy || !!draft` 锁定；只能重传同一错误文件，或离开/刷新后重新开始。

影响：永久性文件错误没有就地恢复路径，还会留下已创建但未进入的草稿。

建议：保留已成功上传的素材，允许替换/移除失败项；用文件身份记录上传结果，不要把草稿存在当作禁用所有编辑的条件。提供继续进入已有草稿的入口也可避免重建项目。

复现：前端 `allows removing an invalid attachment after project creation succeeds but upload fails`。使用实际 StudioHome 与 Mantine 控件；仅模拟网络、外壳、CSS 以及 jsdom 缺少的字体事件接口，未模拟按钮禁用逻辑。

## 测试与部署观察

- 新增对抗断言：后端 7 项、前端 1 项，均在所述行为断言处失败，归为上述 7 个问题。测试按期望的正确行为编写，失败用于证明缺陷，不表示测试已修复。
- 现有后端相关测试选集通过：package 归属检查、未登录/跨用户访问、路径注入、过期版本、提案确认幂等、目标步骤边界、暂停标志保留、未知订单直接重试拦截，以及 Agent 工具循环等。
- 现有前端相关测试 30/30 通过，包括 SSE、返回数据归一化与动态翻译。它们没有覆盖本次上传失败后的恢复路径。
- 后端测试在 WSL `Ubuntu-24.04` 的已有 `aicat-avatar-go-check:latest` 镜像中执行，使用隔离测试数据库与临时素材目录。供应商重提请求仅到 `httptest` 本地服务器。
- 对 7 个相关源码文件比较哈希，Windows 工作区与 `/opt/aicat/avatar-mvp/src` 中的部署源码一致。这是源码一致性检查，不能单独证明运行二进制的全部内容一致。
- 只读观察时，前端、API、PostgreSQL 容器均 healthy。Windows 实测 `http://localhost:8080/` 和 `http://172.17.234.187:8080/` 均 HTTP 200。该项仅验证 HTTP 入口可达，不代替浏览器业务验收。
- 未在生产数据上尝试破坏性操作，未执行真实收费全链路。本次没有重启服务。

未列为确认缺陷的初始疑点：崩溃后自动跑完整流程（实际恢复逻辑先标记 interrupted，需人工恢复）；首页失败后直接编辑文件顺序（控件实际已禁用，真正的问题见 R7）；跨项目切换的异步污染（本轮未建立完整用户可达路径复现）。

## 重跑与证据

后端（在 Windows PowerShell 执行）：

```powershell
wsl -d Ubuntu-24.04 -- python3 /mnt/d/pro/ai_cat/avatar-agent/docs/reviews/2026-11-07/run_backend.py
```

前端：

```powershell
& D:\pro\ai_cat\avatar-agent\docs\reviews\2026-11-07\run_frontend.ps1
```

修复阶段已将用例转为源码目录中的正式回归测试。两个 runner 现在直接运行正式测试并写入 `fix-*` 结果，不再复制临时文件，也不覆盖原始失败证据。后端附加 `--race` 可执行 Agent 全套竞态检查。

本目录的 `backend-results.json`、`frontend-results.json` 保存失败断言；`baseline-backend.txt`、`baseline-frontend.json` 保存现有测试结果；`deployment-observation.json` 和 `access-observation.json` 保存源码比较、容器状态与入口访问记录。
