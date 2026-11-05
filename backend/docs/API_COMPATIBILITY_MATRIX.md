# API Compatibility Matrix

源项目：`/Users/viccy/Desktop/project/NarratoAI-SaaS-B`  
Go 项目：`/Users/viccy/Desktop/project/NarratoAI-SaaS-B-Go`

本文件用于跟踪 Python FastAPI 后端到 Go Gin 后端的接口兼容性。状态说明：

- ✅ 已覆盖：Go 已提供同名接口，且初步看请求/响应方向一致。
- ⚠️ 需细测：Go 已提供接口，但业务逻辑、响应结构、状态码或权限可能有差异。
- ❌ 缺失：Python 有该接口，Go 当前没有同名兼容接口。
- 🔁 设计替换：Go 使用了新接口或新交互模型替代 Python 原接口。
- 🧪 占位/降级：Go 接口存在，但依赖配置或逻辑仍可能返回占位结果。

## 认证与用户

| Method | Path | Go 状态 | 风险 | 备注 |
|---|---|---:|---:|---|
| POST | `/api/v1/auth/register` | ✅ | P1 | `code` 为邮箱验证码，校验通过后创建用户并标记邮箱已验证。 |
| POST | `/api/v1/auth/request-email-code` | ✅ | P1 | 真实 SMTP 发送，含冷却、小时/日配额和哈希存储。 |
| POST | `/api/v1/auth/login` | ⚠️ | P1 | 基础登录可用，需对比 Python 响应字段、禁用用户、未验证用户策略。 |
| POST | `/api/v1/auth/refresh` | ⚠️ | P1 | 需对比刷新 token 轮换、过期、吊销策略。 |
| POST | `/api/v1/auth/logout` | ⚠️ | P1 | Go 当前返回成功，需确认是否实现 token blacklist/撤销。 |
| GET | `/api/v1/auth/me` | ⚠️ | P1 | 需对比 UserProfileResponse 字段。 |
| POST | `/api/v1/auth/send-verification-code` | ✅ | P1 | 登录后为当前用户邮箱发送 `verify_email` 验证码。 |
| POST | `/api/v1/auth/verify-email` | ✅ | P1 | 必须校验当前用户邮箱验证码。 |
| POST | `/api/v1/auth/forgot-password` | ✅ | P1 | 发送 `reset_password` 邮箱验证码。 |
| POST | `/api/v1/auth/reset-password` | ✅ | P1 | 必须校验 `reset_password` 验证码，成功后启用密码登录。 |
| GET | `/api/v1/auth/oauth/{provider}/start` | ✅ | P1 | GitHub/Google OAuth Authorization Code + PKCE 起点。 |
| GET | `/api/v1/auth/oauth/{provider}/callback` | ✅ | P1 | 后端换取 provider token，生成一次性登录 ticket。 |
| POST | `/api/v1/auth/oauth/exchange` | ✅ | P1 | 前端用 ticket 换项目 JWT；ticket 只能使用一次。 |
| GET | `/api/v1/auth/email-quota` | ✅ | P2 | 返回当前邮箱验证用途的小时配额使用情况。 |
| GET | `/api/v1/users/profile` | ⚠️ | P1 | 需对比响应 schema。 |
| PUT | `/api/v1/users/profile` | ⚠️ | P1 | 需对比可更新字段与校验。 |
| GET | `/api/v1/users/settings` | ⚠️ | P1 | 需对比默认设置结构。 |
| PUT | `/api/v1/users/settings` | ⚠️ | P1 | 需对比 settings schema。 |
| PUT | `/api/v1/users/password` | ⚠️ | P1 | 需要验证当前密码、密码策略与错误格式。 |

## 项目

| Method | Path | Go 状态 | 风险 | 备注 |
|---|---|---:|---:|---|
| POST | `/api/v1/projects/` | ⚠️ | P1 | 需对比 ProjectResponse 字段、默认状态、JSON 字段。 |
| GET | `/api/v1/projects/` | ⚠️ | P1 | 需对比分页参数、排序、搜索、响应字段。 |
| GET | `/api/v1/projects/{project_id}` | ⚠️ | P1 | 已做 owner 校验，需对比 detail/lite schema。 |
| PUT | `/api/v1/projects/{project_id}` | ⚠️ | P1 | 需对比允许更新字段与校验。 |
| DELETE | `/api/v1/projects/{project_id}` | ⚠️ | P1 | 需对比硬删除/软删除行为。 |
| POST | `/api/v1/projects/{project_id}/preview/upload` | ❌ | - | 已移除；前端主路径使用浏览器本地缓存。 |
| POST | `/api/v1/projects/{project_id}/narrations/upload` | ❌ | - | 已移除；配音片段保存到浏览器本地缓存，后端仅保存脚本中的本地音频元数据。 |
| GET | `/api/v1/projects/{project_id}/subtitle` | ✅ | P2 | 需细测字段包装格式。 |
| GET | `/api/v1/projects/{project_id}/video_context` | ✅ | P2 | 需细测字段包装格式。 |
| GET | `/api/v1/projects/{project_id}/video_config` | ✅ | P2 | 需细测字段包装格式。 |
| GET | `/api/v1/projects/{project_id}/ai-script` | ✅ | P2 | 需细测字段包装格式。 |

## Token、套餐与支付

| Method | Path | Go 状态 | 风险 | 备注 |
|---|---|---:|---:|---|
| GET | `/api/v1/token-accounts/balance` | ⚠️ | P1 | 需对比账户不存在时是否自动创建、响应字段。 |
| POST | `/api/v1/token-accounts/recharge` | ⚠️ | P0 | 需对比微信支付下单、金额、套餐、过期时间。 |
| GET | `/api/v1/token-accounts/recharge/{out_trade_no}/status` | ⚠️ | P0 | 需对比订单状态流转。 |
| POST | `/api/v1/token-accounts/webhook` | ⚠️ | P0 | 必须验证验签、解密、金额、幂等、事务。 |
| GET | `/api/v1/token-accounts/usage/stats` | ⚠️ | P1 | 需对比统计口径。 |
| GET | `/api/v1/token-accounts/usage/records` | ⚠️ | P1 | 需对比分页和筛选。 |
| GET | `/api/v1/token-accounts/recharge/records` | ⚠️ | P1 | 需对比分页和筛选。 |
| GET | `/api/v1/token-packages/packages` | ⚠️ | P1 | Go 有实现，需对比公开/管理员字段。 |
| GET | `/api/v1/token-packages/packages/{package_id}` | ⚠️ | P1 | 需对比权限。 |
| POST | `/api/v1/token-packages/packages` | ⚠️ | P1 | 管理员接口，需细测。 |
| PUT | `/api/v1/token-packages/packages/{package_id}` | ⚠️ | P1 | 管理员接口，需细测。 |
| DELETE | `/api/v1/token-packages/packages/{package_id}` | ⚠️ | P1 | 管理员接口，需细测。 |
| POST | `/api/v1/subscriptions/payments/wechat/webhook` | ⚠️ | P0 | Go 复用微信回调，需确认与 Python 行为一致。 |

## 系统与通知

| Domain | Go 状态 | 风险 | 备注 |
|---|---:|---:|---|
| `/api/v1/system/settings*` | ⚠️ | P1 | 需对比公开配置过滤、管理员权限、value JSON 格式。 |
| `/api/v1/system/notifications*` | ⚠️ | P1 | 需对比用户隔离、分页、统计。 |
| `/api/v1/invitations*` | ❌ | - | 已下线；注册改为邮箱验证码，第三方入口改为 GitHub/Google OAuth。 |

## AI 工具

| Method | Python Path | Go 状态 | 风险 | 备注 |
|---|---|---:|---:|---|
| POST | `/api/v1/tools/analyze-images` | ⚠️ | P1 | Go 使用 SiliconFlow 边界，需对比 Python Gemini/SiliconFlow 回退、计费倍率。 |
| POST | `/api/v1/tools/ocr-images` | ⚠️ | P1 | 需对比 OCR 输出结构与错误格式。 |
| GET | `/api/v1/tools/prompt-templates` | ✅ | P2 | Go 已扫描模板，需对比 include_detail 逻辑。 |
| POST | `/api/v1/tools/upload-reference-voice` | ⚠️ | P1 | 需对比 ASR+TTS provider、音色持久化。 |
| POST | `/api/v1/tools/text-to-speech-with-uri` | ⚠️ | P1 | Go 返回音频，需对比 header、文件名、计费。 |
| DELETE | `/api/v1/tools/voice/{voice_uri}` | ⚠️ | P1 | Gin wildcard 路由需确认 URL 编码兼容。 |
| POST | `/api/v1/tools/generate-narration` | ❌ | P1 | Python 同步废弃接口；Go 未保留兼容路径。 |
| POST | `/api/v1/tools/generate-narration-async` | ❌ | P1 | Python 异步接口；Go 改为 stream。 |
| POST | `/api/v1/tools/match-scenes` | ❌ | P1 | Python 同步废弃接口；Go 未保留兼容路径。 |
| POST | `/api/v1/tools/match-scenes-async` | ❌ | P1 | Python 异步接口；Go 改为 stream。 |
| POST | `/api/v1/tools/deep-inference-generate-script` | ❌ | P1 | Python 深度推理接口；Go 未保留同名兼容路径。 |
| GET | `/api/v1/tools/ai-task/{task_id}` | ❌ | P1 | Python AI 异步任务查询；Go 未提供。 |
| DELETE | `/api/v1/tools/ai-task/{task_id}` | ❌ | P1 | Python AI 异步任务取消；Go 未提供。 |
| POST | `/api/v1/tools/generate-script-stream` | 🔁 | P1 | Go 新增流式脚本生成，不是 Python 同名接口。 |
| POST | `/api/v1/tools/generate-narration-stream` | 🔁 | P1 | Go 新增流式解说生成，不是 Python 同名接口。 |
| POST | `/api/v1/tools/match-scenes-stream` | 🔁 | P1 | Go 新增流式场景匹配，不是 Python 同名接口。 |

## Qwen3 / FunASR / DashScope

| Method | Path | Go 状态 | 风险 | 备注 |
|---|---|---:|---:|---|
| POST | `/api/v1/tools/qwen3/asr/submit` | ⚠️ | P1 | 需对比 DashScope 请求、返回 task_id、计费。 |
| GET | `/api/v1/tools/qwen3/asr/query/{task_id}` | ⚠️ | P1 | 需对比转录结果归一化。 |
| POST | `/api/v1/tools/dashscope/temp-files/upload-policy` | ➕ | P2 | Go 额外提供；确认前端是否使用。 |
| POST | `/api/v1/tools/funasr/asr/submit` | ➕ | P2 | Go 额外提供；确认是否为新需求。 |
| GET | `/api/v1/tools/funasr/asr/query/{task_id}` | ➕ | P2 | Go 额外提供；确认是否为新需求。 |

## 管理后台

| Domain | Go 状态 | 风险 | 备注 |
|---|---:|---:|---|
| `/api/v1/admin/users*` | ⚠️ | P1 | 需对比筛选、分页、禁用、角色更新、token 调整。 |
| `/api/v1/admin/statistics*` | ⚠️ | P1 | Go 已从空占位改聚合查询，但需对比 Python 统计口径。 |

## 建议的兼容性验收顺序

1. 认证和密码重置。
2. 项目 CRUD 和用户隔离。
3. Token 余额、扣费、支付回调。
4. AI 工具响应结构和计费。
5. 管理后台统计口径。
6. 存储上传链路。
