# Production Gap Analysis

本文档记录 Go 迁移版本相对 Python 生产后端的主要差距。优先级说明：

- P0：上线阻断，必须修复或明确关闭功能。
- P1：上线高风险，灰度前必须验证并补测试。
- P2：可后续优化，但需要记录。

## P0 上线阻断项

### 1. 注册验证码校验

状态：已修复。Go `POST /api/v1/auth/register` 现在要求 `code` 为真实邮箱验证码，校验通过后才创建用户并标记邮箱已验证。

邀请码注册链路已整套下线，包括 `/api/v1/invitations/*`、模型和前端后台入口。历史数据删除由显式 SQL migration 执行。

保留验收：

- 增加测试：无 code、错误 code、已使用 code、过期邮箱验证码、正确 code。
- 生产环境必须配置 SMTP，否则验证码发送接口返回 503。

### 2. 邮件验证码接口

状态：已修复。Go `request-email-code`、`send-verification-code`、`forgot-password` 会生成哈希验证码记录并通过 SMTP 发送。

当前策略：6 位数字，10 分钟有效，60 秒冷却，每邮箱每小时 5 次、每天 20 次，最多 5 次校验失败。

保留验收：

- SMTP 配置缺失时返回明确 503。
- 测试环境使用 fake mailer，不依赖真实 SMTP。

### 3. 重置密码验证码

Python 使用：

```python
reset_password_by_code(email, code, new_password)
```

状态：已修复。Go `reset-password` 必须校验 `reset_password` 邮箱验证码，成功后启用密码登录。

### 4. 邮箱验证验证码

状态：已修复。Go `verifyEmail` 必须校验当前用户邮箱的 `verify_email` 验证码后才标记邮箱已验证。

### 5. AI 任务接口不兼容

Python 有：

```text
POST /api/v1/tools/generate-narration-async
POST /api/v1/tools/match-scenes-async
GET  /api/v1/tools/ai-task/{task_id}
DELETE /api/v1/tools/ai-task/{task_id}
```

Go 改成 SSE stream：

```text
POST /api/v1/tools/generate-script-stream
POST /api/v1/tools/generate-narration-stream
POST /api/v1/tools/match-scenes-stream
```

风险：旧前端、移动端、任务恢复、断线重连、后台查询不兼容。

建议二选一：

1. 保留 Python 兼容异步任务接口；或
2. 明确要求前端改造，并将该变更写入迁移说明。

### 6. 支付回调和扣费必须完成幂等验证

微信支付回调、token 入账、AI 扣费必须保证幂等和事务一致。

建议补测试：

- 同一微信回调重复 10 次，只入账一次。
- 金额不匹配拒绝。
- 套餐不存在拒绝。
- 支付成功但更新余额失败时可重试。
- AI 请求重试不重复扣费。

## P1 高风险项

### 1. Token 扣费并发风险

Go 当前扣费在事务中完成，但使用读取账户、内存加法、保存账户的模式。建议改为数据库原子更新：

```sql
UPDATE user_token_accounts
SET used_tokens = used_tokens + ?
WHERE user_id = ?
  AND total_tokens - used_tokens >= ?
```

然后根据 affected rows 判断余额是否充足。

### 2. 响应结构未系统对齐

Go 多处使用 `gin.H` 或直接返回 GORM model。Python 使用 Pydantic response_model。风险包括：

- 字段缺失或多余。
- 时间格式不同。
- 错误格式不同。
- 嵌套 JSON 字段包装不同。
- null/空对象/空数组不一致。

建议：为每个核心接口定义 Go response DTO，不直接暴露 GORM model。

### 3. 错误格式不一致

Python 经常返回：

```json
{
  "detail": {
    "error_code": "...",
    "error_message": "..."
  }
}
```

Go 当前常见格式：

```json
{
  "error": "Bad Request",
  "message": "...",
  "success": false
}
```

建议：如果前端依赖 Python 错误结构，应统一兼容。

### 4. 外部 provider 未配置时的行为需统一

Go 中部分 provider 未配置时返回 503，部分返回 placeholder 或开发占位。生产环境建议：

- 未配置真实 provider 时返回明确 503。
- 不要返回假成功。
- 健康检查或启动检查暴露配置缺口。

### 5. ASR 任务状态存储内存降级需谨慎

Go Redis 不可用时会降级到内存任务存储。生产环境风险：

- 多实例不可见。
- 重启丢任务。
- 负载均衡后查询不到任务。

建议：生产禁用内存降级，Redis 不可用直接 fail fast。

### 6. SSE 流式接口需要专项压测

Go 新增 stream 设计，需要验证：

- 客户端断开后是否取消上游请求。
- 并发长连接下内存是否稳定。
- Cloudflare/Nginx 是否缓冲。
- 心跳和超时策略。
- token 扣费是否只在 completed 后执行。

### 7. 测试覆盖不足

当前 Go 项目测试文件较少，主要集中在 providers/prompts/server 基础测试。建议补：

- auth 集成测试。
- project 权限测试。
- token 扣费并发测试。
- payment webhook 幂等测试。
- storage 上传完成测试。
- AI task/stream 测试。

## P2 优化项

### 1. 可观测性不足

建议加入：

- Prometheus metrics。
- pprof，仅内网启用。
- OpenTelemetry trace。
- SQL 慢查询日志。
- request_id。
- provider latency/error metrics。

### 2. 健康检查需要拆分

当前 `/health` 只表示服务进程可响应。建议新增：

```text
/health/live
/health/ready
/health/deps
```

`ready` 至少检查：

- DB。
- Redis。
- 必需外部配置。

### 3. 数据库索引审计

重点检查：

- `users.email`
- `projects.owner_id, created_at, status`
- `token_usage_records.user_id, billing_month, recorded_at, api_endpoint, model_name`
- `wechat_payments.out_trade_no, transaction_id, status`
- `upload_records.user_id, status, created_at`
- `notifications.user_id, is_read, created_at`

### 4. DTO 与 service 分层

当前 Go handlers 中业务逻辑较多。建议逐步拆分：

```text
internal/server      HTTP handler
internal/services    业务逻辑
internal/repositories 数据访问
internal/providers   外部服务适配
```

## 推荐修复顺序

1. 修复密码重置、邮箱验证、注册 code 校验。
2. 明确 AI 异步任务兼容策略。
3. 修复 token 扣费原子性。
4. 补支付回调幂等测试。
5. 统一错误格式和 response DTO。
6. 建立压测和观测基线。
