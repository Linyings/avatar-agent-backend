# Frontend × Go Backend Compatibility Review

前端项目：`/Users/viccy/Desktop/project/NarratoAI-SaaS-F`  
Go 后端项目：`/Users/viccy/Desktop/project/NarratoAI-SaaS-B-Go`  
Python 后端项目：`/Users/viccy/Desktop/project/NarratoAI-SaaS-B`

本评审基于前端 `src/` 中实际 API 调用，对前一版迁移评估方案进行补充。

## 总体结论

1. 前端不是纯 Python 旧接口消费者；它已经使用 Go 新增的：
   - `/api/v1/tools/generate-script-stream`
   - `/api/v1/tools/dashscope/temp-files/upload-policy`
   - `/api/v1/tools/funasr/*`
2. 因此前一版方案里“Go 缺少 Python 的 `generate-narration-async` / `match-scenes-async`”对当前前端不是最高优先级。
3. 真正阻塞当前前端联调的是：
   - 前端订阅相关服务仍调用 `/api/v1/subscriptions/*` 多个接口，Go 目前只保留了微信 webhook，不支持 create/status/plans/current 等。
   - 系统上传旧入口已清理，当前逐帧推理主链路使用浏览器本地 OPFS 与 DashScope 临时文件上传。

## P0：当前前端高概率会失败的接口

### 1. 订阅支付接口缺失

前端调用位置：

- `src/features/subscriptions/services/paymentService.ts`
- `src/features/subscriptions/services/subscriptionService.ts`
- `src/features/subscriptions/hooks/usePayment.ts`
- `src/features/subscriptions/hooks/useSubscription.ts`

前端调用但 Go 未实现：

```text
POST /api/v1/subscriptions/payments/wechat/create
GET  /api/v1/subscriptions/payments/wechat/{outTradeNo}/status
POST /api/v1/subscriptions/payments/{outTradeNo}/refund
POST /api/v1/subscriptions/payments/{outTradeNo}/close
GET  /api/v1/subscriptions/plans
GET  /api/v1/subscriptions/plans/{planName}
POST /api/v1/subscriptions/plans
PUT  /api/v1/subscriptions/plans/{planId}
DELETE /api/v1/subscriptions/plans/{planId}
GET  /api/v1/subscriptions/current
POST /api/v1/subscriptions/initialize
POST /api/v1/subscriptions/upgrade
POST /api/v1/subscriptions/cancel
GET  /api/v1/subscriptions/stats
POST /api/v1/subscriptions/tasks/reset-tokens
POST /api/v1/subscriptions/tasks/handle-expired
GET  /api/v1/subscriptions/health
GET  /api/v1/subscriptions/debug/user/{userId}
```

Go 已有的是 Token 账户体系：

```text
POST /api/v1/token-accounts/recharge
GET  /api/v1/token-accounts/recharge/{outTradeNo}/status
GET  /api/v1/token-packages/packages
```

当前 `SubscriptionPage` 主要使用 token hooks，风险相对可控；但 `useSubscription` 和管理员订阅计划页面仍可能触发旧订阅接口。

建议：

- 如果产品已切换为 Token 套餐制：前端删除或禁用 subscriptionService 相关入口，把管理员“订阅计划”页面改为 Token 套餐管理。
- 如果仍保留订阅制：Go 必须补齐 `/api/v1/subscriptions/*`。

## P1：已对齐但需要联调验证的接口

### 1. Go stream 接口与前端解析基本匹配

前端 `scriptGenerationServiceV2.ts` 期望 SSE event：

```text
stage
thinking_delta
answer_delta
usage
completed
error
```

Go `streamTool` 当前也使用这些事件名，方向是匹配的。

仍需验证：

- `completed` payload 是否包含前端期望字段：
  - 脚本/画面匹配：`items`, `usage`, `model`, `request_id`, `metadata`
  - 解说文案：`narration_text`, `usage`, `model`, `request_id`, `metadata`
- `usage` 是否是扁平字段：`prompt_tokens/completion_tokens/total_tokens`。
- 错误事件格式是否包含 `message`。
- 客户端 `AbortController` 取消后，Go 是否取消上游 provider 请求。

### 2. 项目 JSON 字段接口基本对齐

前端期望：

```text
GET /projects/{id}/video_config  -> response.video_config
GET /projects/{id}/video_context -> response.video_context 或 response 本身
GET /projects/{id}/ai-script     -> result.items
```

Go 需要确保字段包装与前端一致。尤其 `ai-script` 如果直接返回 JSON 内容而不是 `{ ai_analysis_script: ... }`，前端更容易消费。

### 3. 配音片段改为本地缓存

前端不再上传生成的配音片段到后端：

```text
POST /api/v1/projects/{projectId}/narrations/upload 已移除
```

后端只保存脚本 JSON 中的 `local_audio_key`、`audio_storage`、`local_audio_missing`、`duration` 等元数据，音频 Blob 存放在浏览器本地缓存。

前端生成项目预览图后不再上传图片文件到后端，而是保存到浏览器本地缓存，并通过项目字段保存索引：

```text
local_preview_image_key
preview_image_storage = "local"
local_preview_image_missing
```

`POST /api/v1/projects/{projectId}/preview/upload` 已移除，当前前端主路径使用本地缓存。

### 4. TTS 主链路基本对齐，但存在一个旧接口

前端实际项目配音主要使用：

```text
POST   /api/v1/tools/upload-reference-voice
POST   /api/v1/tools/text-to-speech-with-uri
DELETE /api/v1/tools/voice/{voiceUri}
```

Go 已有这些接口。

`ttsService.ts` 已清理旧的普通 TTS 兼容封装，仅保留 `text-to-speech-with-uri` 调用。

## P1：安全与业务正确性仍然成立

结合前端后，前一版 P0/P1 安全问题仍成立：

1. 注册 `code` 未校验，会影响 `RegisterPage` 的真实安全语义。
2. 邮件验证码接口占位，会影响注册、邮箱验证、忘记密码。
3. 重置密码未校验验证码，是严重安全风险。
4. 邮箱验证可直接成功，是安全风险。
5. Token 扣费并发需要原子更新。
6. 支付回调必须幂等。

这些不是前端能兜底的问题，必须后端修。

## 建议修订前一版迁移计划

### 调整后的联调优先级

#### 第 1 优先级：让当前前端主路径不 404

1. 项目预览图：确认本地缓存 metadata 随项目列表返回。
2. `/api/v1/projects/{id}/narrations/upload`：已移除，确认前端不再调用。
3. `/api/v1/tools/generate-script-stream`：用真实前端请求联调 SSE payload。
4. `/api/v1/token-accounts/*` 与 `/api/v1/token-packages/*`：联调订阅页/价格页。

#### 第 2 优先级：清理前端旧服务或补后端兼容

1. subscriptionService：如果不用订阅制，前端移除入口；如果保留，后端补接口。

#### 第 3 优先级：生产安全修复

1. 注册 code 校验。
2. 邮件验证码真实实现。
3. 密码重置验证码校验。
4. 邮箱验证验证码校验。
5. Token 扣费原子更新。
6. 支付幂等测试。

## 建议新增验收清单

### 前端页面级 smoke test

| 页面/流程 | 必测接口 | 当前风险 |
|---|---|---|
| 注册页 | `/auth/request-email-code`, `/auth/register` | P0：验证码占位/code 未校验 |
| 登录页 | `/auth/login`, `/auth/me` | P1：响应字段需确认 |
| 项目列表 | `/projects/` | P1：分页/字段需确认 |
| 素材配置页 | `/tools/generate-script-stream` | P1：SSE payload 需联调 |
| 配音页 | TTS + narration upload | P1：需确认返回字段/multipart |
| 订阅页 | `/token-accounts/*`, `/token-packages/*` | P1：字段和支付状态需确认 |
| 管理用户 | `/admin/users*` | P1：字段/分页需确认 |
| 管理统计 | `/admin/statistics/*` | P1：统计口径需确认 |
| 系统设置 | `/system/settings*` | P1：value JSON 格式需确认 |
| 第三方登录 | `/auth/oauth/*` | P1：OAuth ticket 成功/失败态需联调 |

### 前端 API 静态检查建议

建议后续加一个脚本，自动从前端 `src/` 抽取 `/api/v1` 字符串，并和 Go `server.go` 路由表比较，生成：

```text
docs/FRONTEND_API_USAGE_MATRIX.md
```

这样每次前端新增接口时，都能及时发现 Go 后端未实现的路由。
