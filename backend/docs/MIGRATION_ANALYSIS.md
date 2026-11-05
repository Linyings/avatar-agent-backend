# FastAPI 到 Go 迁移盘点

## 原后端模块

原项目是 FastAPI + SQLModel/SQLAlchemy 异步后端：

- 入口：`app/main.py`
- 路由聚合：`app/api/v1/__init__.py`
- 路由实现：`app/api/v1/endpoints`
- 业务服务：`app/services`
- 数据模型：`app/models`
- 请求/响应模型：`app/schemas`
- 迁移：`alembic/versions`

核心功能域：

- 认证与用户：注册、登录、JWT、真实邮箱验证码、GitHub/Google OAuth、用户设置、密码
- 项目：项目 CRUD、字幕、视频上下文、视频配置、AI 脚本、预览图、解说音频
- Token 账户：余额、套餐、充值、微信支付、用量记录
- 系统：系统配置、通知
- 邀请入口：已下线，历史表由显式迁移删除
- 存储：预签名 URL、上传记录、上传完成、取消上传
- AI 工具：图片分析、OCR、TTS、ASR、脚本生成、解说生成、画面匹配
- 管理后台：用户管理、Token 调整、统计概览

## 路由兼容清单

Go 版本已注册以下兼容路由：

- `GET /health`
- `GET /api/v1/test`
- `POST /api/v1/auth/register`
- `POST /api/v1/auth/request-email-code`
- `POST /api/v1/auth/login`
- `POST /api/v1/auth/refresh`
- `POST /api/v1/auth/logout`
- `GET /api/v1/auth/me`
- `POST /api/v1/auth/send-verification-code`
- `POST /api/v1/auth/verify-email`
- `POST /api/v1/auth/forgot-password`
- `POST /api/v1/auth/reset-password`
- `GET /api/v1/auth/email-quota`
- `GET /api/v1/auth/oauth/{provider}/start`
- `GET /api/v1/auth/oauth/{provider}/callback`
- `POST /api/v1/auth/oauth/exchange`
- `GET /api/v1/users/profile`
- `PUT /api/v1/users/profile`
- `GET /api/v1/users/settings`
- `PUT /api/v1/users/settings`
- `PUT /api/v1/users/password`
- `POST /api/v1/projects/`
- `GET /api/v1/projects/`
- `GET /api/v1/projects/{project_id}`
- `PUT /api/v1/projects/{project_id}`
- `DELETE /api/v1/projects/{project_id}`
- ~~`POST /api/v1/projects/{project_id}/preview/upload`~~（已移除；当前前端预览图主路径为浏览器本地缓存）
- ~~`POST /api/v1/projects/{project_id}/narrations/upload`~~（已移除，配音片段改为浏览器本地缓存）
- `GET /api/v1/projects/{project_id}/subtitle`
- `GET /api/v1/projects/{project_id}/video_context`
- `GET /api/v1/projects/{project_id}/video_config`
- `GET /api/v1/projects/{project_id}/ai-script`
- `GET /api/v1/token-accounts/balance`
- `POST /api/v1/token-accounts/recharge`
- `GET /api/v1/token-accounts/recharge/{out_trade_no}/status`
- `POST /api/v1/token-accounts/webhook`
- `GET /api/v1/token-accounts/usage/stats`
- `GET /api/v1/token-accounts/usage/records`
- `GET /api/v1/token-accounts/recharge/records`
- `GET /api/v1/token-packages/packages`
- `GET /api/v1/token-packages/packages/{package_id}`
- `POST /api/v1/token-packages/packages`
- `PUT /api/v1/token-packages/packages/{package_id}`
- `DELETE /api/v1/token-packages/packages/{package_id}`
- `POST /api/v1/subscriptions/payments/wechat/webhook`
- `GET /api/v1/system/settings/public`
- `POST /api/v1/system/settings`
- `GET /api/v1/system/settings`
- `GET /api/v1/system/settings/{key}`
- `PUT /api/v1/system/settings/{key}`
- `DELETE /api/v1/system/settings/{key}`
- `POST /api/v1/system/notifications`
- `GET /api/v1/system/notifications`
- `PUT /api/v1/system/notifications/read-all`
- `GET /api/v1/system/notifications/stats`
- `PUT /api/v1/system/notifications/{notification_id}/read`
- `DELETE /api/v1/system/notifications/{notification_id}`
- ~~`/api/v1/invitations/*`~~（已下线；注册改为真实邮箱验证码，第三方入口改为 GitHub/Google OAuth）
- `POST /api/v1/tools/analyze-images`
- `POST /api/v1/tools/ocr-images`
- `GET /api/v1/tools/prompt-templates`
- `POST /api/v1/tools/upload-reference-voice`
- `POST /api/v1/tools/text-to-speech-with-uri`
- `DELETE /api/v1/tools/voice/{voice_uri}`
- `POST /api/v1/tools/generate-script-stream`
- `POST /api/v1/tools/generate-narration-stream`
- `POST /api/v1/tools/match-scenes-stream`
- `POST /api/v1/tools/qwen3/asr/submit`
- `GET /api/v1/tools/qwen3/asr/query/{task_id}`
- `POST /api/v1/tools/dashscope/temp-files/upload-policy`
- `POST /api/v1/tools/funasr/asr/submit`
- `GET /api/v1/tools/funasr/asr/query/{task_id}`
- `GET /api/v1/admin/users`
- `GET /api/v1/admin/users/{user_id}`
- `POST /api/v1/admin/users/{user_id}/tokens`
- `PUT /api/v1/admin/users/{user_id}/status`
- `PUT /api/v1/admin/users/{user_id}/role`
- `GET /api/v1/admin/statistics/overview`
- `GET /api/v1/admin/statistics/users/ranking`
- `GET /api/v1/admin/statistics/tokens/trends`
- `GET /api/v1/admin/statistics/models/usage`
- `GET /api/v1/admin/statistics/apis/usage`

## 迁移策略

Go 版本先保证“路由可用、核心数据可写、主链路可测”，外部供应商使用兼容占位响应。这样前端可以先切到 Go 服务验证基础链路，然后逐步把 Python 服务中的供应商客户端迁移成 Go adapter。

推荐后续顺序：

1. OpenAI 兼容多阶段编排进一步复刻 Python 的事实抽取、上下文压缩和场景召回策略
2. Redis worker 独立进程化与重试/死信队列

## 第二阶段新增

- 微信支付配置完整时调用 v3 Native 下单；回调使用 API v3 key 解密 resource，并在支付成功后更新 `wechat_payments`、`user_token_accounts`、`token_recharge_records`。
- 异步任务状态迁移为 Redis 优先、内存降级，接口返回真实任务结构。
- `/api/v1/tools/qwen3/*` 和 `/api/v1/tools/funasr/*` 已接入 DashScope 提交/查询 HTTP 客户端边界。
- `/api/v1/tools/analyze-images` 与 `/api/v1/tools/ocr-images` 已接入 SiliconFlow OpenAI 兼容视觉接口。
- `/api/v1/tools/text-to-speech-with-uri` 与 `/api/v1/tools/upload-reference-voice` 已接入 SiliconFlow TTS/音色上传边界。
- `/api/v1/tools/generate-script-stream`、`generate-narration-stream`、`match-scenes-stream` 已接入 OpenAI 兼容 SSE 流式响应。
- Python 版 `prompt/fast/*.json` 已完整复制到 Go 项目，并实现模板扫描、元数据列表、变量渲染、模板选择和运行时 prompt 构造。
- `/api/v1/tools/prompt-templates` 已从占位改为真实模板列表。
- 微信回调已接入平台证书目录验签。
- DashScope Qwen3/FunASR 成功查询会下载转录结果 JSON，归一化为 `content/text_content/segments_count/tokens_consumed/duration`。
- FishAudio premium TTS 已接入 HTTP `/v1/tts` JSON 请求边界。
- FishAudio premium 上传参考音频已接入 `POST /model` multipart 创建持久音色模型，删除音色已接入 `DELETE /model/{id}`。
- 管理统计接口已从空占位改为实际聚合查询。
- OpenAPI 3.0 schema 已接入 `/api/v1/openapi.json`，并提供 `/docs` 与 `/redoc`。

仍需注意：

- OpenAI 兼容当前已使用原始 prompt 模板和运行时输入块；事实抽取、上下文压缩、场景召回、多阶段重试按当前需求不迁移。
- FishAudio 官方文档同时展示 JSON 与 msgpack 直连方式；Go 端当前 TTS 采用 JSON 请求，若账号要求 msgpack，可在 provider 层替换编码。
