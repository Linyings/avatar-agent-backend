# 剧情理解 SSE 保活设计

## 背景与根因

PC 对 5 分钟视频执行逐帧工作流时，`POST /api/v1/tools/frame-workflow/narrative-analyze`
连续两次在 120 秒后失败。生产日志显示两次请求均精确耗时 `2m0s` 并返回 408，期间 API、
PostgreSQL 和 workflow run 心跳保持健康。

带 workflow identity 的剧情理解请求会先把 Provider 流保存在 `bufferedToolEmitter` 中，直到
Provider 成功结果、usage、Provider request ID 和 Credits 状态持久化后才建立并回放客户端 SSE。
这使反向代理在前 120 秒看不到任何响应字节，触发空闲连接超时。客户端收到的是传输层断开，
因此只能显示 `Failed to fetch`。

## 目标

- 剧情理解执行期间每 15 秒向客户端发送 SSE 注释心跳，避免反向代理空闲超时。
- workflow identity 请求继续缓存模型思考和答案，不在计费成功确认前暴露 Provider 结果。
- Provider 成功后仍先完成幂等计费持久化，再回放缓存事件和 `completed`。
- Provider、计费或协议失败时，在已建立的 SSE 中返回结构化 `error` 事件。
- Web 与 PC 使用同一个后端端点，均自动获得修复，无需修改代理超时。

## 设计

`frameWorkflowNarrativeAnalyze` 在完成身份、余额和执行租约校验后立即调用
`beginSerializedToolEventStream`，并使用 `startToolStreamHeartbeat` 每 15 秒写入
`: heartbeat` 注释。该注释不包含字幕、剧情、Token 或用户数据。

Provider 事件处理沿用现有安全边界：

- 无 workflow identity：思考、答案和完成事件可实时发送。
- 有 workflow identity：思考和答案继续写入 `bufferedToolEmitter`；Provider 的 `completed`
  只保存在内存中。
- Provider 成功后校验 usage 和 Provider request ID，执行 `finalizeProviderSuccess`；成功后才
  回放缓存事件并发送 `completed`。
- Provider 或 finalization 失败时，调用 `emitWorkflowToolStreamError`，在 SSE 中返回统一的
  `code`、`message`、`retryable` 和 `request_id`。

请求结束、取消或客户端断开时必须停止心跳 goroutine。现有 workflow run 的 30 秒业务心跳
继续负责租约；SSE 注释心跳只负责当前 HTTP 连接，两者职责不合并。

## 计费与恢复

本次不改变 workflow run、execution key 或 CreditReservation 逻辑。同一
`workflow_run_id + execution_key` 的恢复仍复用原步骤身份，最终只确认一份用户积分。
失败请求没有最终 Provider 成功记录时保持可恢复状态；用户点击继续后从剧情理解步骤恢复，
不会重跑已经保存的前置步骤。

## 测试

- 新增失败测试，证明 workflow identity 的剧情理解在 Provider 阻塞时会输出 SSE 注释心跳。
- 验证心跳不会提前回放 `thinking_delta`、`answer_delta` 或 `completed`。
- 验证 Provider 成功和计费持久化后才回放缓存事件。
- 验证 Provider 错误通过 SSE `error` 返回，并停止心跳 goroutine。
- 回归运行后端目标测试和 `go test ./... -count=1`。
- 部署后检查生产健康状态，并用不触发真实付费的测试请求验证 CORS/接口可达性；实际 5 分钟
  视频由用户使用原任务点击继续完成验收。

## 发布与回滚

后端按现有 GitHub tag、ACR 镜像、SSH Docker Compose 流程发布新 patch 版本。部署前备份
生产 `.env`。若健康检查或 SSE 行为异常，将 `BACKEND_IMAGE` 恢复到当前 `v2.1.11` 并重启
API。该改动不包含数据库迁移，回滚无需处理表结构。
