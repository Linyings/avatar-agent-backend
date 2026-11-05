# NarratoAI SaaS Backend Go

这是 `NarratoAI-SaaS-B` 后端的 Go 迁移版本，目标是保留现有 FastAPI 的 `/api/v1` 路由兼容面，并逐步把核心业务从 Python 迁移到 Go，以获得更低运行开销和更高并发性能。

## 技术栈

- Go 1.26+
- Gin 1.12+
- GORM 1.31+
- PostgreSQL
- JWT + bcrypt

依赖通过 `go get ...@latest` 拉取，保持当前最新稳定版本。

## 快速启动

### Docker 启动后端与数据库

```bash
cp env.example .env
docker compose up -d --build
curl http://localhost:8000/health
```

Docker Compose 默认将后端 API 暴露到宿主机 `8000`，PostgreSQL 暴露到宿主机 `5433`。容器内 API 使用 `postgres:5432` 连接数据库；如果要覆盖后端宿主机端口，可设置 `BACKEND_PORT`，例如 `BACKEND_PORT=8001 docker compose up -d --build`。

微信平台证书默认从 `./data/wechat_certs` 挂载到容器内 `/app/data/wechat_certs`。生产环境请通过 `.env` 或部署平台 Secret 注入 `SECRET_KEY`、SMTP、支付和 AI provider 配置。

### 本地 Go 启动

```bash
cp env.example .env
docker compose up -d postgres
export DATABASE_URL='postgres://narratoai:narratoai@localhost:5433/narratoai?sslmode=disable'
export SECRET_KEY='change-me-change-me-change-me-change-me'
go run ./cmd/api
```

默认监听 `0.0.0.0:8000`，接口前缀为 `/api/v1`。
如果本地 Go 调试需要避开端口占用，可在 `.env` 中临时改为 `PORT=8001`。
本地开发数据库由 Docker Compose 中的 PostgreSQL 提供，宿主机端口为 `5433`，容器内端口为 `5432`，数据保存在 `postgres_data` volume。

## CI/CD 发布到阿里云镜像仓库

仓库内置 GitHub Actions workflow：`.github/workflows/release-acr.yml`。推送 `v*` 版本标签、发布 GitHub Release，或手动运行 workflow 时会自动执行：

1. 使用 Go 1.26.2 下载依赖。
2. 执行 `go test ./...`。
3. 使用当前 `Dockerfile` 打包后端 API 镜像。
4. 登录阿里云容器镜像服务 ACR。
5. 推送 `${release_tag}`、`sha-${short_sha}` 和 `latest` 三个镜像标签。

需要先在 GitHub 仓库配置以下 Secrets：

| Secret | 示例 | 说明 |
| --- | --- | --- |
| `ALIYUN_REGISTRY` | `registry.cn-hangzhou.aliyuncs.com` | ACR 镜像仓库公网地址 |
| `ALIYUN_REGISTRY_NAMESPACE` | `narratoai` | ACR 命名空间 |
| `ALIYUN_REGISTRY_USERNAME` | `your-acr-user` | ACR 登录用户名 |
| `ALIYUN_REGISTRY_PASSWORD` | `******` | ACR 登录密码或可推送镜像的访问凭证 |

可选 GitHub Variables：

| Variable | 默认值 | 说明 |
| --- | --- | --- |
| `ALIYUN_IMAGE_REPOSITORY` | `narratoai-backend-go` | ACR 仓库名 |
| `DOCKER_PLATFORMS` | `linux/amd64` | Docker Buildx 目标平台，例如 `linux/amd64,linux/arm64` |

一键发布流程：

```bash
scripts/publish_release.sh 1.2.3
```

脚本会校验版本号、确认工作区干净、执行 `go test ./...`、创建 `v1.2.3` 标签，并推送当前分支与标签到 `origin`。标签推送后会触发测试、打包和推送镜像。若只想临时跳过本地测试，可使用 `scripts/publish_release.sh 1.2.3 --skip-tests`；若仍需要生成 GitHub Release，可使用 `scripts/publish_release.sh 1.2.3 --create-github-release`。也可以在 GitHub Actions 页面手动运行 `Release Backend Image`，并填写 `image_tag` 做临时发布。

## 生产部署

### 方式一：单独 API 容器连接阿里云 RDS

当前生产环境数据库使用阿里云 RDS 时，只运行 API 容器即可。先准备生产环境变量文件，例如 `.env.production`，其中包含 `SECRET_KEY`、`CORS_ORIGINS`、`FRONTEND_BASE_URL`、`PUBLIC_API_BASE_URL`、支付、SMTP 和 AI provider 配置。数据库连接可以写入 `.env.production`，也可以像下面一样在 `docker run` 中显式覆盖。

```bash
export IMAGE=registry.cn-hangzhou.aliyuncs.com/<namespace>/narratoai-backend-go:v1.2.3

docker pull "${IMAGE}"
docker rm -f narratoai-api 2>/dev/null || true

docker run -d \
  --name narratoai-api \
  --restart unless-stopped \
  -p 8000:8000 \
  --env-file .env.production \
  -e HOST=0.0.0.0 \
  -e PORT=8000 \
  -e ENVIRONMENT=production \
  -e DEBUG=false \
  -e DATABASE_URL='postgres://<rds_user>:<rds_password>@<rds_endpoint>:5432/<rds_db>?sslmode=require' \
  -e AUTO_MIGRATE=true \
  -e PUBLIC_API_BASE_URL='https://api.example.com' \
  -e WECHAT_CERT_DIR=/app/data/wechat_certs \
  -v "$(pwd)/data/wechat_certs:/app/data/wechat_certs:ro" \
  "${IMAGE}"

curl -f http://127.0.0.1:8000/health
```

注意事项：

- 如果 RDS 未开启 SSL，将 `sslmode=require` 改为 `sslmode=disable`；如果开启了证书校验，按 RDS 证书配置使用 `verify-full`。
- `DATABASE_URL` 中密码包含 `@`、`:`、`/`、`#` 等特殊字符时需要 URL encode。
- 如果生产迁移由独立变更窗口执行，可设置 `AUTO_MIGRATE=false`，并在发布前手动执行迁移检查。

### 方式二：API 与 PostgreSQL 均使用 Docker

后期如果数据库也迁移到 Docker，可继续使用现有 `docker-compose.yml`。默认会启动 API 与 PostgreSQL，PostgreSQL 数据持久化在 `postgres_data` volume。

```bash
cp env.example .env
```

至少修改 `.env` 中这些生产值：

```bash
SECRET_KEY=<strong-random-secret>
POSTGRES_PASSWORD=<strong-postgres-password>
DOCKER_DATABASE_URL=postgres://narratoai:<strong-postgres-password>@postgres:5432/narratoai?sslmode=disable
BACKEND_IMAGE=registry.cn-hangzhou.aliyuncs.com/<namespace>/narratoai-backend-go:v1.2.3
ENVIRONMENT=production
DEBUG=false
PUBLIC_API_BASE_URL=https://api.example.com
```

使用 CI 推送的镜像部署：

```bash
docker compose pull api
docker compose up -d
curl -f http://127.0.0.1:8000/health
```

本地或临时环境需要从源码构建时仍可使用：

```bash
docker compose up -d --build
```

## 初始化管理员

项目提供管理员用户设置脚本，可创建管理员、列出用户、提升或撤销管理员权限：

```bash
go run ./scripts/setup_admin.go
```

也可以用参数直接创建首个管理员：

```bash
go run ./scripts/setup_admin.go --create \
  --username admin \
  --email admin@example.com
```

脚本会提示输入密码；非交互环境可通过 `ADMIN_PASSWORD` 环境变量传入。

## 当前迁移状态

已落地：

- 健康检查与 API v1 测试接口
- 注册、登录、刷新、当前用户、用户资料/设置、密码修改
- 项目 CRUD 与项目 JSON 数据读取接口
- Token 套餐、余额、充值订单占位、用量/充值记录
- 系统配置、通知、邀请系统
- 微信支付 Native 下单、API v3 回调 AES-GCM 解密、支付成功后 Token 入账
- Redis 异步任务状态存储，未配置 Redis 时自动降级到内存任务存储
- DashScope Qwen3/FunASR 提交与查询 HTTP 客户端边界，未配置时返回开发占位结果
- SiliconFlow VLM 图片分析与 OCR HTTP 客户端边界，支持 OpenAI 兼容视觉消息格式
- SiliconFlow TTS `/audio/speech` 生成语音与参考音频上传边界
- OpenAI 兼容 `/chat/completions` 流式客户端，脚本/解说/画面匹配 SSE 接口已接入；DashScope Qwen Vision 时间线图分析也使用端到端流式转发
- Python 版 `prompt/fast/*.json` 生成脚本模板已完整复制并接入 Go PromptTemplateService / PromptBuilder
- `/api/v1/tools/prompt-templates` 返回真实模板列表
- OpenAI 兼容生成脚本会根据 `video_config.style_model` / `style_mode` 选择模板，渲染变量并追加运行时输入块
- FishAudio premium TTS HTTP 边界，按 UTF-8 字节计费
- FishAudio premium 参考音频上传创建持久音色模型，删除音色模型
- 微信支付平台证书验签，证书目录由 `WECHAT_CERT_DIR` 配置
- DashScope Qwen3/FunASR 成功结果下载、归一化和 Token 计费
- 管理端统计聚合：概览、用户排行、Token 趋势、模型/API 使用量
- 管理员用户与统计接口
- 外部服务未配置时保留开发降级响应

待继续接入：

- OpenAI 兼容多阶段编排进一步复刻 Python 的事实抽取、上下文压缩和场景召回策略
- Redis worker 独立进程、重试和死信队列

## API 文档

开发环境默认提供：

- `GET /api/v1/openapi.json`
- `GET /docs`
- `GET /redoc`

OpenAPI schema 会从 Gin 已注册路由自动生成，并包含 Bearer JWT 安全方案、路径参数、基础请求体/响应结构和常用组件 schema。

## 关键环境变量

支付：

```bash
WECHAT_APP_ID=
WECHAT_MCH_ID=
WECHAT_API_V3_KEY=
WECHAT_PRIVATE_KEY_FILE=
WECHAT_CERT_SERIAL_NO=
WECHAT_NOTIFY_URL=
WECHAT_CERT_DIR=./data/wechat_certs
WECHAT_VERIFY_SIGNATURE=true

CREEM_API_KEY=
CREEM_WEBHOOK_SECRET=
CREEM_TEST_MODE=true
CREEM_SUCCESS_URL=
CREEM_PORTAL_RETURN_URL=
```

### Creem 测试产品

以下产品已在 Creem test 环境创建，可用于本地/测试环境管理端配置。不要把这些 test `prod_*` ID 和 live API key 混用；生产环境需要在 Creem live 环境重新创建产品并替换 ID。

订阅产品：

| 计划 | 月付 Product ID | 月付价格 | 年付 Product ID | 年付价格 |
| --- | --- | ---: | --- | ---: |
| Basic | `prod_1N2TyWaPTE1YN5zsPpAfvE` | `$9.90` | `prod_5XSeHaP8qk1YUeuB7Gb3M5` | `$95.04` |
| PRO | `prod_7Dtn6rlLajN7jYfCRj1h6s` | `$29.90` | `prod_38yS8F6f2opIwuGAyb8pu6` | `$287.04` |
| PRO MAX | `prod_3EBuE01P4DKJmKK2f8akcc` | `$69.90` | `prod_6yMEtecygDtFMX1gYTdN5x` | `$671.04` |

一次性 Credits 产品：

| Credits | Product ID | 价格 |
| ---: | --- | ---: |
| 2,500 | `prod_3SKRQi51wDznkvhvEn0qza` | `$4.90` |
| 7,500 | `prod_hwN6fjQiT1yArNTWJFjti` | `$14.90` |
| 15,000 | `prod_4yplqC5GCdLCjnbjI0JXgm` | `$29.90` |
| 30,000 | `prod_4OKwqOSnWg1zEXvv1Tb36L` | `$60.00` |

配置流程：

1. 在后端 `.env` 配置 Creem test API key、`CREEM_TEST_MODE=true`、webhook secret 和回跳 URL。
2. 在 Creem Dashboard 配置 webhook：`/api/v1/billing/creem/webhook`。
3. 在管理端“订阅计划管理”创建 Basic / PRO / PRO MAX，并填入对应月付、年付 Product ID。
4. 在管理端“Credits 套餐管理”创建 4 个一次性套餐，开启 Creem 支付并填入对应 Product ID。

任务与 AI 服务：

```bash
DASHSCOPE_API_KEY=
DASHSCOPE_REQUEST_TIMEOUT_SECONDS=60
QWEN_VISION_MODEL=qwen3.6-plus
QWEN_VISION_BASE_URL=https://dashscope.aliyuncs.com/compatible-mode/v1
SILICONFLOW_API_KEY=
OPENAI_PRO_API_KEY=
OPENAI_PRO_API_BASE_URL=https://api.openai.com/v1
OPENAI_PRO_MODEL=gpt-4.1
OPENAI_PRO_THINKING_TYPE=enabled
OPENAI_PRO_REASONING_EFFORT=high
OPENAI_FLASH_API_KEY=
OPENAI_FLASH_API_BASE_URL=https://api.openai.com/v1
OPENAI_FLASH_MODEL=gpt-4.1-mini
OPENAI_FLASH_THINKING_TYPE=
OPENAI_FLASH_REASONING_EFFORT=
LLM_PROMPT_CACHE_MODE=auto
OPENAI_PROMPT_CACHE_KEY_ENABLED=true
OPENAI_PROMPT_CACHE_RETENTION=
DASHSCOPE_EXPLICIT_CACHE_ENABLED=true
FISH_AUDIO_API_KEY=
FISH_AUDIO_BASE_URL=https://api.fish.audio
FISH_AUDIO_MODEL=s1
ELEVENLABS_API_KEY=
ELEVENLABS_BASE_URL=https://api.elevenlabs.io
ELEVENLABS_SCRIBE_MODEL=scribe_v2
```

DashScope 原生 API 固定使用 `https://dashscope.aliyuncs.com/api/v1`，包括 ASR 任务提交/查询和临时文件上传策略；视觉模型仍使用 `QWEN_VISION_BASE_URL` 的 OpenAI 兼容地址。

OpenAI 兼容文本模型分为两套 profile：`OPENAI_PRO_*` 用于剧情理解和生成解说文案；`OPENAI_FLASH_*` 用于 timeline 决策等其它轻量步骤。两套 profile 都支持独立配置 `*_THINKING_TYPE` 和 `*_REASONING_EFFORT`；百炼 DashScope 兼容地址会把 `enabled`/`disabled` 映射为 `enable_thinking`。Prompt 缓存默认针对 DashScope/百炼优化：`LLM_PROMPT_CACHE_MODE=auto` 且 `DASHSCOPE_EXPLICIT_CACHE_ENABLED=true` 时，DashScope 兼容地址默认使用显式缓存标记。

## AI 接口响应契约

前端可实时展示的 AI 接口必须是端到端流式：浏览器读取 `text/event-stream` 时，后端也必须用上游模型的流式能力请求并逐块转发，不允许出现“后端非流式等待完整模型响应，再用 SSE 包装给前端”的接口形态。

允许的两类形态：

- 端到端流式：上游模型请求启用 `stream: true`，后端转发 `stage`、`thinking_delta`、`answer_delta`、`completed` 等 SSE 事件。当前包括 `/api/v1/tools/generate-script-stream`、`/api/v1/tools/timeline-view/decide`、`/api/v1/tools/timeline-view/analyze`、`/api/v1/tools/frame-workflow/narrative-analyze`。
- 普通 JSON 或异步轮询：后端等待完整响应后返回 JSON、音频二进制，或先提交任务再由前端轮询任务状态。当前包括图片分析/OCR、TTS、ASR 提交与查询等接口。

新增 AI 接口时，如果前端协议是 SSE，必须先确认对应供应商支持流式输出；若供应商只支持非流式或长任务，应使用普通 JSON 或提交任务 + 轮询。

## 验证

```bash
go test ./...
```

## One-Time Credit Payments

The operational configuration, callback investigation, refund, rollback, and
local acceptance gate for CNY one-time Credit QR payments are documented in
[`docs/operations/one-time-credit-payments.md`](docs/operations/one-time-credit-payments.md).
That runbook contains no credential material and does not authorize a production
deployment.

当前测试覆盖健康检查、注册、登录、鉴权、项目创建/列表、ASR 任务生命周期、微信开发模式回调入账、SiliconFlow Chat/TTS 协议、OpenAI 兼容 SSE 流解析、DashScope 归一化、DashScope 临时图片上传与 Qwen Vision 时间线端到端流式分析、ElevenLabs Scribe 词级转写打包、FishAudio TTS、FishAudio 创建/删除模型、Prompt 模板加载/渲染/接口、管理统计聚合、OpenAPI schema。

## PC 在线升级文件

生产环境通过 `https://api.narratoai.cn/desktop-updates/` 提供 PC 在线升级文件。升级目录必须以只读方式挂载到 API 容器：

```dotenv
DESKTOP_UPDATE_HOST_DIR=/srv/narratoai-updates
DESKTOP_UPDATE_CONTAINER_ROOT=/app/data/desktop-updates
```

`docker-compose.yml` 会把宿主机目录挂载到容器并设置 `DESKTOP_UPDATE_ROOT`。服务只允许读取 `win32/x64`、`darwin/x64` 和 `darwin/arm64` 下经过校验的元数据与安装包文件；`latest.json` 和 `RELEASES` 禁止缓存，带版本号的安装包使用长期不可变缓存。未配置 `DESKTOP_UPDATE_ROOT` 时，该路由不会注册。
