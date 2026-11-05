# 千问 TTS 模型配置迁移

本地 `.env` 的 `# DashScope / Qwen / FunASR` 分组增加 `QWEN_TTS_MODEL=qwen3-tts-flash`，与 `QWEN3_ASR_MODEL`、`FUNASR_MODEL`、`QWEN_VISION_MODEL` 集中维护。同步更新普通和生产配置示例。

请求模型、管理接口、配音能力接口、计费模型列表、请求指纹及计费记录读取同一配置。未配置时不退回代码中的旧模型。旧的 `qwen3-tts-instruct-flash:<音色>` 标识保留作为已保存项目的音色 ID，仅提取音色名发送给新模型，不用其前缀决定模型。

`qwen3-tts-flash` 不支持旧 Instruct 的自然语言指令控制，因此不发送 `instructions` 和 `optimize_instructions`；原来依靠此指令实现的情绪、语速控制不能视为仍有效。若以后选择 Instruct 系列模型，请求会继续带这些参数。历史旧模型的计费回退仍保留。

验证：

- 配置、TTS provider、千问配音、音色管理及受影响的计费模型测试通过（WSL Go，含 SQLite CGO）。
- 本地密钥调用 `qwen3-tts-flash` 返回 HTTP 200、音频 URL 和计费用量（24 字符）。没有据此声称所有音色或声音质量已验证。
- 本地 Docker API 重建并启动，容器确认 `QWEN_TTS_MODEL=qwen3-tts-flash`，容器内和 Windows 的 `/health` 均 healthy。
- 未改 PC/Web 源码，未提交、推送或部署生产。此次本地镜像包含工作区原有未提交后端改动。

以后修改模型后，在后端目录执行 `docker compose up -d --force-recreate api` 更新容器环境；只有代码变化才需要重新构建镜像。请确认替代模型兼容当前 HTTP 合成协议和音色，单独变更模型名不能自动兼容不同 API。
