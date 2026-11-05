# NarratoAI Web/PC 工作流容错、断点恢复与计费幂等设计

状态：已确认  
日期：2026-07-13

## 1. 背景与目标

Web 通用剪辑流程目前会在浏览器到 API、API 到 AI Provider、SSE 流或项目保存出现一次短暂网络中断时直接失败。PC 真实任务执行器在应用重启后会把运行中任务重新排队并从头执行，无法复用已完成步骤。现有项目级 Credits 预留在前端失败时依赖额外请求释放；如果释放请求同样失败，后续重试可能把旧预留和新预留一起确认，存在重复扣费风险。

本设计采用“共享工作流运行记录 + 客户端断点”方案，同时改造 Web 和 PC：

- 网络瞬时故障自动重试，连续失败后允许用户从断点继续。
- Web 刷新、PC 重启或页面关闭不会被视为取消。
- 未生成并保存最终视频时，用户主动取消不扣 Credits。
- 最终视频成功保存后，对同一运行只确认一次 Credits。
- PC 的视频、字幕、文案、中间产物和最终作品仍只保存在用户电脑本地。
- 未完成运行按最后活动时间保留 24 小时，超时后自动取消并释放预留 Credits。

本轮不把剪辑改造成服务端后台任务。页面或 PC 应用关闭后，客户端执行暂停，用户返回后手动继续。

## 2. 核心规则

### 2.1 运行与步骤身份

- 每次剪辑创建一个后端签发的 UUID `workflow_run_id`。
- Web 运行关联平台 `project_id`；同一项目同一时间只允许一个未结束运行。
- PC 运行不创建平台项目，使用 `client_type=pc` 和本地稳定 `client_task_id` 关联用户本地任务。
- 每个步骤由 `workflow_run_id + step_id + input_hash` 唯一标识。
- 后端计算 `execution_key = SHA256(workflow_run_id|step_id|input_hash)`；客户端不能自行替换。
- 视频、剪辑配置或影响输出的上游输入变化会改变 `input_hash`。输入变化后不得继续旧运行，必须取消旧运行并创建新运行。

### 2.2 自动重试

- 初始请求失败后最多自动重试 3 次，退避时间为约 1 秒、2 秒、4 秒，并加入 0-300ms 随机抖动。
- 可重试条件：浏览器网络错误、SSE 流读取中断、HTTP 408、429、500、502、503、504。
- 429 优先遵守 `Retry-After`，但单次等待不超过 30 秒。
- 401 只执行现有 Token 刷新流程；刷新成功后重试一次，刷新失败转登录。
- 400、402、403、404、409 业务冲突和输入校验错误不做通用自动重试。
- 用户主动停止产生的 AbortError 不重试。
- 普通 API 单次超时 30 秒；上传接口按现有上传进度执行，不应用 30 秒总超时。
- SSE 不设置总时长限制。后端每 15 秒发送心跳，客户端连续 45 秒未收到任何数据时中断并使用同一逻辑步骤重连。
- SSE 重连前丢弃本次未完成的思考流和答案流，避免把两次尝试内容拼接到一起。
- FunASR 等可查询异步任务优先复用后端保存的 Provider task ID，只重新轮询，不重复提交。
- Provider 不支持恢复时允许重新调用，但同一逻辑步骤只能产生一份用户 Credits 预留；额外 Provider 成本由平台承担。

### 2.3 中断、取消和最终结果

- 自动重试耗尽后，运行状态变为 `interrupted`，预留 Credits 保持 pending。
- 页面关闭、刷新、PC 退出、断网和进程异常均视为中断，不视为取消。
- 用户主动取消时，如果最终视频尚未成功保存，后端释放该运行的全部 pending 预留，不创建消费流水。
- “最终结果”严格定义为：Web 最终视频已写入浏览器本地持久存储；PC 最终视频已完整写入用户选择的本地目录。
- 字幕、文案、配音、画面匹配等中间结果不属于最终结果。
- 最终文件保存成功后，客户端先持久化 `final_result_ready` 本地 outbox，再调用幂等完成接口。
- 完成接口在一个数据库事务中写入最终状态并确认该运行的 pending Credits。响应丢失后重复调用只能返回同一个成功结果，不能再次扣费。
- 本地已记录 `final_result_ready` 时不再展示“取消并释放积分”，客户端持续重试完成确认。

## 3. 后端数据模型

### 3.1 `workflow_runs`

| 字段 | 说明 |
| --- | --- |
| `id` | UUID 主键，即 `workflow_run_id` |
| `user_id` | 所属用户，必填 |
| `project_id` | Web 项目 UUID，PC 为 NULL |
| `client_type` | `web` 或 `pc` |
| `client_task_id` | PC 本地任务稳定 ID；Web 可为空 |
| `input_hash` | 本次运行输入哈希 |
| `status` | `running/interrupted/blocked/finalizing/succeeded/cancelled/expired` |
| `billing_status` | `pending/confirmed/released` |
| `current_step_id` | 当前或最后执行步骤 |
| `last_error_code` | 结构化错误码，不存敏感输入 |
| `last_error_message` | 脱敏后的用户可读错误 |
| `last_active_at` | 最后心跳、进度或恢复时间 |
| `expires_at` | `last_active_at + 24h` |
| `final_result_at` | 最终文件成功保存时间 |
| `completed_at/cancelled_at` | 结束时间 |
| `created_at/updated_at` | 审计时间 |

约束：

- Web 对同一 `project_id` 只允许一个 `running/interrupted/blocked/finalizing` 运行。
- PC 对 `(user_id, client_type, client_task_id)` 建活动状态部分唯一索引；同一本地任务可以保留历史运行，但同时只能存在一个未结束运行。
- 所有读取、恢复、完成和取消操作必须校验 `user_id` 所有权。

### 3.2 `workflow_step_executions`

| 字段 | 说明 |
| --- | --- |
| `id` | UUID 主键 |
| `workflow_run_id` | 所属运行 |
| `step_id` | 共享 frame workflow 步骤 ID |
| `input_hash` | 本步骤输入哈希 |
| `execution_key` | 服务端生成的唯一键 |
| `status` | `running/succeeded/interrupted/failed/blocked` |
| `attempt_count` | 实际执行尝试次数 |
| `provider_request_id` | 可选 Provider request/task/transcription ID |
| `error_code/error_message` | 最近错误，内容脱敏 |
| `usage` | 计费所需 Token、字符数或时长，不存业务内容 |
| `started_at/completed_at/updated_at` | 时间字段 |

`execution_key` 全局唯一。同一 key 的重复请求只能更新同一逻辑步骤，不创建第二个用户计费单元。

PC 步骤记录不得保存视频、字幕、文案、提示词、音频、文件路径或最终输出路径。Web 的业务输出继续保存在现有项目 workflow；步骤执行表只保存运行和计费元数据。

### 3.3 Credits 预留扩展

- `credit_reservations` 增加 nullable `workflow_run_id` 和 `workflow_step_execution_id`。
- 新客户端的预留幂等键基于 `workflow_run_id + execution_key`，不再依赖每次变化的 Provider request ID。
- 每个逻辑步骤仅以第一次成功且可交付的尝试创建或确定预留金额；后续同 key 尝试不增加用户 Credits。
- 完成、释放和过期必须按 `workflow_run_id` 处理，禁止新流程继续按 `project_id` 批量确认。
- 旧客户端没有 `workflow_run_id` 时保留旧计费路径，作为发布过渡兼容。

## 4. 后端接口

### 4.1 运行接口

- `POST /api/v1/workflow-runs`
  - 请求：`client_type`、`project_id?`、`client_task_id?`、`input_hash`。
  - Web 必须拥有 project；PC 必须提供 client_task_id 且 project_id 为空。
  - 相同作用域、相同 input_hash 存在活动运行时返回该运行；不同 input_hash 返回 409，要求先取消旧运行。
- `GET /api/v1/workflow-runs/:run_id`
  - 返回运行、步骤摘要、Credits 状态、剩余恢复时间和是否允许继续。
- `POST /api/v1/workflow-runs/:run_id/heartbeat`
  - 每 30 秒调用；请求携带客户端会话级 `client_instance_id`，更新 `last_active_at/expires_at`、执行租约和当前步骤。
- `POST /api/v1/workflow-runs/:run_id/resume`
  - 请求携带新的 `client_instance_id`；将 `interrupted/blocked` 恢复为 `running`，续期 24 小时，并获取或显式接管执行租约。
- `POST /api/v1/workflow-runs/:run_id/interrupt`
  - 客户端捕获不可恢复的当前会话网络错误时调用；页面卸载仅 best-effort，不能依赖该请求保证状态正确。
- `POST /api/v1/workflow-runs/:run_id/complete`
  - 请求包含最终文件本地保存时间，不包含 PC 文件路径或内容。
  - 原子设置 `final_result_at/status=succeeded/billing_status=confirmed` 并确认该运行预留。
- `POST /api/v1/workflow-runs/:run_id/cancel`
  - 仅在 `final_result_at` 为空且运行未 succeeded 时允许；幂等释放 pending 预留。

### 4.2 步骤接口与收费工具接口

- `PUT /api/v1/workflow-runs/:run_id/steps/:step_id`
  - 上报本地步骤的 `input_hash`、状态和脱敏错误；后端生成并返回 execution_key。
- 所有收费工具接口增加 `workflow_run_id`、`workflow_step_id`、`workflow_input_hash`。
  - JSON/SSE 接口放入 JSON body。
  - multipart 接口放入 form field。
  - FunASR 查询接口使用 query 参数携带运行身份。
- 后端在调用 Provider 前原子获取步骤执行租约；同一 execution_key 的活跃请求返回 409 `step_execution_in_progress`。
- 租约使用 `client_instance_id` 和 90 秒有效期。显式 resume 可由同一用户强制接管旧客户端租约，旧客户端下一次心跳收到冲突后必须停止。
- Provider 成功后先更新步骤 usage/Provider ID 和 Credits 预留，再向客户端发送最终结果。
- 后端每 5 分钟清理一次已过期运行，并在 create/resume/complete/cancel 前执行对应运行的惰性过期检查，确保清理任务延迟不会影响计费正确性。

## 5. Web 客户端设计

- 启动剪辑前创建或恢复 workflow run，并把 run ID 写入当前本地运行状态。
- `authenticatedFetch` 上增加统一的可重试请求层；现有 Token 刷新保持单例并与网络重试区分。
- `runFrameWorkflow` 接收稳定 run ID，不再每次恢复生成新的计费运行身份。
- 每次步骤状态变化继续保存项目 workflow，同时把运行快照和可恢复本地资产写入现有 IndexedDB/OPFS 存储。
- 刷新或重新进入项目时，先查询活动 workflow run，再使用现有 recovery 逻辑从第一个未完成且依赖可恢复的步骤继续。
- 如果浏览器本地源视频或中间资产缺失，提示用户重新选择原视频；校验文件名、大小和最后修改时间后再恢复。
- 失败后不再自动调用“释放全部项目预留”。瞬时故障改为 interrupt；只有用户点击取消或重新开始时调用 cancel。
- 页面卸载时中止当前浏览器任务并 best-effort 标记 interrupt，但保留 24 小时恢复窗口。
- 最终视频写入 OPFS 成功后持久化 finalization outbox，再调用 complete；重新进入页面时优先重放未完成 outbox。

## 6. PC 客户端设计

- 每个 `DesktopClipTask` 保存稳定 `workflowRunId`、`inputHash`、`workflowStepId`、`finalResultReady` 和本地 checkpoint key。
- `DesktopRealTaskExecutor` 不再使用 `desktop-${task.id}` 充当后端 project_id；创建 `client_type=pc` 的独立 workflow run，收费接口不发送平台 project_id。
- `onRunUpdate` 将可序列化的 FrameDebugRun 步骤记录写入 IndexedDB；视频和中间媒体继续保存在本地文件系统或 OPFS。
- 应用重启时将遗留 running 任务改为 `interrupted`，不自动 queued。用户点击继续后读取 checkpoint 和本地源文件，从最近可恢复步骤执行。
- 暂停等同于中断当前执行，但不取消 run、不释放 Credits。继续使用同一 workflowRunId 和 execution key。
- 取消仅在 finalResultReady=false 时调用后端 cancel，并同步更新本地任务为 cancelled。
- 最终 MP4 使用临时文件写入并原子重命名；只有完整写入目标目录成功后才设置 finalResultReady 并调用 complete。
- complete 响应丢失时任务显示“正在完成扣费确认”，保留输出路径并在联网、应用启动和定时器触发时重放完成请求。
- PC 后端记录不得包含源文件路径、输出目录、文件名以外的业务内容；诊断日志中的文件名默认脱敏。

## 7. 用户界面状态

Web 弹窗和 PC 队列统一使用以下用户状态：

- `自动重试中`：显示步骤、重试次数和下一次重试倒计时。
- `任务已中断`：显示失败步骤、最后成功步骤、诊断编号和 24 小时剩余时间。
- `等待充值`：402 不自动重试；充值后继续同一运行。
- `正在完成扣费确认`：最终文件已保存，complete 尚未确认。
- `已完成`：最终文件可打开，Credits 已确认一次。
- `已取消/已过期`：运行结束，pending Credits 已释放。

中断时提供“继续任务”“取消任务”“重新开始”。重新开始必须先幂等取消旧运行；输入哈希变化时只允许重新开始。finalResultReady=true 后隐藏取消和重新开始，只保留完成确认重试、打开文件和诊断信息。

## 8. 监控与隐私

- API 为每个请求生成 `request_id`，日志字段包含 `workflow_run_id`、`step_id`、`execution_key`、`attempt`、HTTP 状态、Provider、耗时和错误分类。
- SSE、FunASR、项目保存、运行完成、Credits 预留/确认/释放/过期均写结构化事件。
- 前端错误提示展示可复制的 request_id 或 run 诊断编号，不直接显示原始堆栈。
- 指标至少包括：网络重试次数与成功率、SSE 中断率、手动恢复成功率、重复计费拦截数、pending 预留数、24 小时过期数、完成确认重放数。
- 日志不记录 Authorization、密码、API Key、完整请求 body、视频、字幕、文案、提示词、本地绝对路径或二进制内容。

## 9. 迁移与发布顺序

1. 后端增加 workflow run/step 表、Credits 外键、接口和兼容分支；旧接口行为保持可用。
2. 部署后端并验证旧 Web、旧 PC 不受影响。
3. Web 接入共享运行、自动重试、interrupt/cancel/complete 和恢复 UI。
4. PC 同步 shared Web 代码，接入独立 PC run、本地 checkpoint 和 finalization outbox。
5. 本轮始终保留旧计费路径，不删除旧接口；移除旧路径不属于本设计范围。

旧 pending 预留不迁移到新 run，继续按原 TTL 最长 24 小时自然过期。数据库迁移只增加表、nullable 字段和索引，不删除现有项目、流水或预留记录。

## 10. 测试与验收

### 后端

- 相同 run/step/input 重复调用只创建一个步骤执行和一条 Credits 预留。
- 并发请求同一 execution_key 时只有一个获取租约，其余返回 409。
- complete 请求响应丢失后重复调用，只生成一份消费流水并返回 succeeded。
- 最终结果前 cancel 释放所有 pending 预留，用户实际扣除为 0。
- succeeded/final_result_at 已存在时 cancel 返回 409，不释放已确认 Credits。
- interrupt 不释放；resume 续期；连续 24 小时无活动转 expired 并释放。
- 402、输入校验和权限错误不创建预留。
- Web project ownership、PC run ownership 和跨用户访问全部被拒绝。
- 旧请求不带 workflow_run_id 时仍走旧兼容路径。

### Web

- GET、PUT、FunASR 轮询和 SSE 网络中断按策略重试，重试耗尽后显示 interrupted。
- SSE 重连不会拼接上一次未完成输出。
- 刷新后从第一个未完成步骤恢复，已完成收费步骤不重复扣费。
- 本地资产缺失时要求重新选择文件，文件不匹配时拒绝恢复。
- 取消、重新开始、等待充值、完成确认重放均符合状态和按钮规则。
- 最终视频未成功写入 OPFS 时不能调用 complete。

### PC

- 多个本地任务各自拥有独立 run，仍按 maxConcurrency 顺序执行。
- 应用重启后 running 变 interrupted，只有用户点击继续才执行。
- checkpoint 可恢复步骤、进度、run ID 和计费 identity；本地文件内容不上传后端。
- 暂停/继续使用同一 execution key；取消前无最终文件时不扣 Credits。
- MP4 写入失败不设置 finalResultReady、不调用 complete。
- MP4 已落盘但 complete 响应丢失时，重启后重放 complete 且只扣一次。

### 端到端故障注入

- 在 Provider 成功前断网、成功后响应前断网、SSE 中途断开、后端容器重启、Cloudflare 502/504、Token 临期刷新、完成接口响应丢失等场景下验证恢复。
- 每个场景核对最终文件、步骤执行次数、CreditReservation、TokenUsageRecord 和 CreditLedgerRecord。
- 验收标准：最终视频前取消扣费为 0；最终视频成功后确认一次；所有重试、刷新和重启均不会重复确认 Credits。

## 11. 明确假设

- 自动重试为初始请求后的最多 3 次重试。
- 24 小时为不活动 TTL，心跳、有效进度和显式 resume 会续期。
- Web 和 PC 均不在客户端关闭后后台继续执行。
- PC 平台后端仅承担认证、AI 工具、运行元数据和计费，不保存 PC 剪辑内容。
- 对不支持 Provider 级恢复的步骤，平台接受重复 Provider 成本以保证用户逻辑步骤只扣费一次。
- 最终文件本地保存成功由受信任客户端上报；本轮不上传最终文件供后端二次校验。
