# 电影模式解说稿专用模型（2026-10-26）

## 配置

本地后端 `.env` 的“电影模式：解说稿专用模型”块：

- `MOVIE_NARRATION_API_KEY`：专用 API 密钥。
- `MOVIE_NARRATION_API_BASE_URL`：OpenAI 兼容接口根地址（调用时追加 `/chat/completions`）。
- `MOVIE_NARRATION_MODEL`：专用模型 ID。
- `MOVIE_NARRATION_THINKING_TYPE`：可选 `enabled` / `disabled`，留空沿用原电影任务策略。
- `MOVIE_NARRATION_REASONING_EFFORT`：可选推理强度，具体取值按供应商协议填写；留空沿用原电影任务策略。

初始配置复制当前 PRO 的五项值，当前模型为 `glm-5.3-flashx`。这是一次性的初始值复制，运行时不读取 PRO 或 Flash 的配置作为回退。之后可以独立更换密钥、接口和模型；更换专用写作模型不会修改 PRO / Flash 配置。

KEY、BASE_URL、MODEL 三项缺一会判定未配置，不静默回退。新部署需补齐该配置。示例文件不含真实凭据。

使用 Docker Compose 时，修改 `.env` 后运行 `docker compose up -d --no-deps --force-recreate api`，单纯 `docker restart` 不会重新读取 env_file。直接启动程序时需在其启动环境中加载这些变量并重启进程。

## 路由范围

仅 `movie_prompt_version=1` 的 `script` / `script_draft`：

- `narration_segment`：章节写作、局部失败重试、指定段落重写。
- `narration_global_repair` / `narration_humanize` / `narration_compression`：文案修订、润色、压缩。
- 现有电影写作 purpose：完整初稿、用户重写、选段重写、复述修复、结尾修复、全文修订。

剧情理解、分镜规划、事实审校报告、画面匹配、聊天助手、格式修复及其它模式保留原有模型路由。内置模型管理选择自定义推理供应商时，PC 既有自定义接口直连逻辑保持不变；本配置作用于平台官方后端调用路径。

PC 现有请求已经携带操作名和电影版本标记，本次无需改动 PC 源码或重打包。模型与参数不是写死在 PC 程序中的。

计费使用实际返回的模型 ID；电影模式的在用模型列表新增“解说稿写作”，便于管理员配置对应价格。

## 验证

- Windows：`go test ./internal/config ./internal/providers` 通过，`go build ./cmd/api` 通过。
- Linux / CGO：计费模型列表及供应商相关的定向 server 测试通过；Windows 相同数据库测试因 CGO_ENABLED=0 不可运行，改用 WSL 执行。
- 专项测试验证独立配置加载、PRO/Flash/其它任务隔离、缺少配置不回退、真实本地 HTTP 流请求使用专用地址/密钥/模型/思考参数。
- 所有模型调用测试使用本地假 HTTP 服务，没有调用付费模型，不代表新模型实际写作质量。

本次仅更新本地开发后端；不提交、不推送、不发布生产版本。
