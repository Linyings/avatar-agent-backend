# 剪辑失败重试累计计费设计（方案 A：显式继承预留）

状态：已确认
日期：2026-08-06

## 1. 背景与目标

用户场景：第一次剪辑失败且任务未取消（客户端崩溃/退出，workflow run 保持 `running`，Credits 预留停留在 `pending`），用户手动进行第二次剪辑并最终成功。

现状问题（生产实测数据，用户 `f944712f`）：

- 第一次剪辑 run `55bb7c0f` 预留 357，run 一直是 `running`，预留未释放；
- 第二次剪辑 run `61f80e76` 成功，确认 123，消费记录为 `-123 / 余额 4294`（gross）；
- 余额页显示 `可用余额 = gross − pending = 4294 − 357 = 3937`，与账本口径不一致，用户看到“消耗 480”，但消费记录只有 123。

目标（用户确认的规则）：

- 同一逻辑任务的多次尝试，最终成功时按**所有尝试的预留累计扣费**（例如第一次 357 + 第二次 123 = 480）；
- 消费记录只记该任务的一条**总消耗**；
- 最终失败/取消则全部释放、不扣费；
- 本次只改代码让规则生效，**历史数据不动**（`55bb7c0f` 的 357 到期后自动释放）。

## 2. 现状基础

后端 main 已包含 commit `111faa2`（feat: inherit credit reservations on workflow retry for cumulative billing）：

- API 新增 `inherit_reservations_from_run_id`；
- 创建新 run 时，源 run 若为 active 状态则取消（`sourceSuperseded`），创建新 run 后调用 `migrateWorkflowRunReservationsTx` 把源 run 的 `pending` 预留迁移到新 run，再触发 `AfterCancel`（迁移后源 run 已无 pending，释放为 no-op）；
- 新 run 完成确认时，`confirmCreditReservationsTx` 汇总其全部 `pending` 预留并生成**一条** ledger 记录（`change_amount = -totalCredits`）。

与目标之间的差距：

1. 生产后端 `v2.1.13/14` 未包含 `111faa2`；
2. PC 客户端 `WorkflowRunClient` 类型已支持 `inheritReservationsFromRunId`，但**没有任何调用点传该参数**；
3. PC 端继承校验要求 `client_task_id` 完全相同，而客户端每次尝试都生成新 task id（`clip-文件ID-时间戳-随机数`），无法命中。

## 3. 核心规则（最终状态）

### 3.1 PC 端继承校验

创建新 run 并携带 `inherit_reservations_from_run_id` 时，满足以下条件才允许继承：

- 源 run 属于同一用户（否则 404）；
- `client_type` 相同（否则 `invalid_reservation_inheritance`）；
- `client_project_name` 相同（trim 后非空，否则 `invalid_reservation_inheritance`）。

不再要求 `client_task_id` 相等。

### 3.2 源 run 状态处理

| 源 run 状态 | 处理 |
| --- | --- |
| `running / interrupted / blocked / finalizing`（非终态） | 取消源 run，迁移其 `pending` 预留到新 run，成功后累计确认 |
| `succeeded / expired`（终态） | 无可继承预留，**no-op**：不报错、不迁移，新 run 正常创建 |
| `cancelled`（终态，PC 同 `client_task_id` 重试） | **复活**该 run 已释放（released）的预留到新 run（恢复 pending），最终成功时累计确认；最终失败则再次释放 |

终态源 no-op 与“复活”的边界：`succeeded`/`expired` 源无未计费工作，直接忽略；PC 端同一 `client_task_id` 链条内被取消的旧 run，其已做工作会在后续重试成功时一并计费（用户确认的规则：多次尝试最终成功 = 累计扣费、一条记录）。为避免复活过期数据，仅复活 `released_at` 在 24 小时内的预留。

### 3.3 确认 / 释放

- 新 run 成功确认：合并其全部预留（含继承部分）→ 一条 `consume` 记录，`change_amount = -总预留`，`balance_after = gross`；
- 新 run 取消/过期：连同继承的预留一起释放，不扣费。

### 3.4 Web 端

保持现有按 `project_id` 相等的校验，不做改动。

## 4. 后端改动

文件：`internal/server/workflow_run_state_service.go`

### 4.1 `loadWorkflowRunForReservationInheritance`

- PC 分支删除 `client_task_id` 相等要求，改为比较 `client_project_name`（`strings.TrimSpace` 后相等且双方非空）；
- web 分支不变。

### 4.2 `Create` 流程调整

- 加载继承源后，若源 run 为终态：置 `inheritedSource = nil`（忽略继承），不取消、不迁移、不触发 `AfterCancel`；
- 若源 run 为非终态且 active：保持现有取消逻辑（`sourceSuperseded = true`）；
- 修复复用路径：`loadActiveWorkflowRun` 命中复用 run 时，在返回前也执行迁移 + `AfterCancel`，避免“源 run 被取消但预留悬空”。即把迁移/`AfterCancel` 放到“目标 run 确定之后”的公共路径，而不是仅新建路径。

### 4.3 现有测试兼容性

- `TestWorkflowRunBillingRetryInheritsPendingReservations`：同 task id、同 project name，仍然通过；
- `TestWorkflowRunBillingRetryInheritanceRejectsInvalidScope`：跨 task 使用不同 project name，仍然拒绝；跨用户仍然 404。

### 4.4 复活已释放预留（v2.1.16 追加）

`Create` 确定目标 run 后，对 PC 端执行 `reviveReleasedTaskReservationsTx`：

- 匹配条件：同一用户、`client_type = pc`、同一 `client_task_id`、源 run `status = cancelled`、预留 `status = released` 且 `released_at` 在 24 小时内；
- 动作：把这些预留迁移到新 run，`status` 恢复为 `pending`、清空 `released_at`；
- 最终新 run 成功确认时与自身预留合并为一条总账；新 run 失败/取消则再次全部释放；
- 不传 `inherit_reservations_from_run_id` 也能命中（覆盖现有 PC 客户端先取消后重试的流程）。

## 5. PC 客户端改动

### 5.1 `src/renderer/desktop/taskQueueStore.ts`

新增纯函数：

```ts
findPreviousAttemptRunId(
  tasks: DesktopClipTask[],
  task: DesktopClipTask,
): string | undefined
```

规则：

- 排除当前 task；
- 按 `sourceFile.id` 匹配同一源文件（与 `createDesktopTaskId` 使用的 `file.id` 一致）；
- 候选 task 的 `status` 属于 `failed / interrupted / output_missing / source_missing`（非成功、非用户主动取消、非进行中）；
- 候选 task 的 `workflowRunId` 非空；
- 多个候选时取最近一个（tasks 数组中最后出现者），保证多次重试逐级链式累加。

### 5.2 `src/renderer/desktop/DesktopRealTaskExecutor.ts`

- `DesktopRealTaskExecutorOptions` 增加 `inheritReservationsFromRunId?: string`；
- 创建新 run 的调用（`workflowClient.create`）在字段非空时透传 `inheritReservationsFromRunId`（client 层已支持）。

### 5.3 `src/renderer/desktop/DesktopTaskRunner.tsx`

- `runRealTask` 执行前计算 `inheritReservationsFromRunId = findPreviousAttemptRunId(state.tasks, executionTask)`，传入 executor options。

## 6. 错误处理

- 源 run 不存在或不属于该用户：`workflow_run_not_found`（404，已有映射）；
- `client_type` / `client_project_name` 不匹配：`invalid_reservation_inheritance`（400，已有映射）；
- 源 run 终态：静默忽略继承，不阻断任务创建；
- 客户端网络异常：继承参数随创建请求一起失败/重试，错误透出给用户，不额外特殊处理。

## 7. 测试计划

### 后端（`internal/server/workflow_run_billing_test.go`）

1. 跨 task id、同 project name：继承成功；源 run 置 `cancelled`、预留迁移；新 run 自身再计费；complete 后确认全部预留、生成**一条** ledger 记录且金额为累计值；
2. 不同 project name：返回 `ErrWorkflowReservationInheritanceInvalid`；
3. 源 run 已终态（cancelled）：新 run 正常创建、无迁移、不报错；
4. HTTP 层：create 携带 `inherit_reservations_from_run_id`（跨 task id、同 project name）返回 201，预留迁移；
5. 现有 `111faa2` 相关测试全部保持通过。

### PC 客户端（vitest）

1. `findPreviousAttemptRunId` 单测：多任务、同文件、排除当前/成功/取消/进行中任务、取最近候选；
2. executor 单测：传入 `inheritReservationsFromRunId` 时，create 请求体包含 `inherit_reservations_from_run_id`；未传入时不含。

## 8. 发布计划

### 后端（NarratoAI-SaaS-B-Go）

1. commit → push main；
2. 创建并推送新 `v*` tag；
3. 等待 `.github/workflows/release-acr.yml` 构建并推送 ACR 镜像；
4. SSH 到生产，更新 `/root/narratoai-backend-go/.env` 的 `BACKEND_IMAGE` 为新 tag；
5. `docker compose pull api` + `docker compose up -d api`；
6. 验证 `curl -fsS http://127.0.0.1:8000/health`。

### PC 客户端（NarratoAI-PC）

commit + push；桌面新版本构建/分发按现有发布流程，另行确认由谁执行。

## 9. 历史数据处理

不动。`55bb7c0f` 的 357 `pending` 预留将于 2026-08-07 07:40:59 UTC（上海时间约 15:41）过期释放，余额回到 4294。

## 10. 不在范围

- 不改变“成功才扣费、失败/取消释放”的基本计费语义；
- 不改余额页对 `reserved_credits` 的展示（API 已返回该字段，前端展示另行处理）；
- 不改 web 端重试逻辑（保持按 `project_id` 校验）；
- 不做历史数据订正。
