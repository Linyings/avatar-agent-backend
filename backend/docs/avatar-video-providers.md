# 数字人口播视频供应商配置

视频供应商、协议、模型 ID、地址及 Key 独立配置，不再限定 RunningHub。当前原生适配 RunningHub 多模态视频和 MiniMax H3 V2；其他厂商可新增 `avatar.Provider` 实现，或通过下述通用 HTTP 网关接入。各厂商协议不同，不能保证任意 Key 或任意图生视频 API 直接兼容。

配置保存在 WSL `/opt/aicat/avatar-mvp/.env`，不进入浏览器或 Git。在该目录执行 `docker compose up -d --wait` 应用配置，保留数据库与任务卷。页面显示当前供应商及模型；查看旧任务时显示该任务原模型。

## RunningHub

```env
VIDEO_PROVIDER=runninghub
VIDEO_API_PROTOCOL=runninghub
VIDEO_API_BASE_URL=https://www.runninghub.cn
VIDEO_MODEL=seedance-2.5-token
VIDEO_RESOLUTION=720p
VIDEO_API_KEY=在本机填写
```

旧的 `RUNNINGHUB_API_KEY` 和 `RUNNINGHUB_BASE_URL` 继续兼容，但仅在选择 runninghub 时回退使用。切换 RunningHub 上其他模型需同时设置匹配的 `VIDEO_GENERATION_PATH`，且该接口必须采用相同的图片和音频参考协议。普通仅图片输入的接口不适用当前固定配音路线。

## MiniMax H3

```env
VIDEO_PROVIDER=minimax
VIDEO_API_PROTOCOL=minimax
VIDEO_API_BASE_URL=https://api.minimax.cn
VIDEO_MODEL=MiniMax-H3
VIDEO_RESOLUTION=768P
VIDEO_API_KEY=在本机填写
VIDEO_GENERATION_PATH=
VIDEO_UPLOAD_PATH=
VIDEO_QUERY_PATH=
AVATAR_MVP_BASELINE_APPROVED=false
```

`MINIMAX_API_KEY` 可作为该供应商的 Key 回退。`VIDEO_API_KEY` 优先；不会拿 RunningHub Key 调用 MiniMax。

原生协议按官方文档实现：

- 上传：`POST /v1/files/upload`，multipart `purpose=video_generation_input`，保留精确文件 ID，使用 `mm_file://{file_id}` 引用。人物照片正确补全文件扩展名，音频为 WAV。
- 提交：`POST /v2/video_generation`，`content` 包含 `text`、`image_url`（`role=reference_image`）、`audio_url`（`role=reference_audio`）。官方禁止把 `first_frame` 与音频参考混用，因此这里使用以人物照片为来源的多模态参考模式。
- 查单：`GET /v2/query/video_generation/{task_id}`，读取 `task.status`、`task.content.url`、`task.usage`，归一为平台任务状态。视频下载后进入同一套素材检查与 talkcraft 渲染。
- 支持 `MiniMax-H3`、`MiniMax-H3-Highspeed`、`MiniMax-H3-Lite`、`MiniMax-H3-Max`。默认 768P；H3 可用 2K，其他三个可用 480P。最终仍渲染为 MVP 的 720×1280。
- H3/Highspeed/Lite 请求视频时长至少 4 秒，Max 至少 5 秒，最多 15 秒。短于下限的配音片段会请求合法视频时长，再按真实配音映射取画面；不拉伸声音或循环人物。音频片段仍必须为 2–15 秒。
- 文件和查单记录按当前官方文档有效期 7 天。不能查询过期订单时明确报错，不自动重新付费生成。

支持参考音频并不保证精确复现整段配音；现有音频/读稿检查、失败停止后续生成和人工嘴型评审继续生效。尚无真实 Key，当前只有协议和流程测试，未宣称 H3 样片验收通过。

官方文档：[创建任务](https://platform.minimax.cn/docs/api-reference/video-generation-v2-create)、[查询任务](https://platform.minimax.cn/docs/api-reference/video-generation-v2-query)、[上传素材](https://platform.minimax.cn/docs/api-reference/file-management-upload)。核对日期：2026-11-03。

## 其他供应商：通用 HTTP 网关协议

此协议是本项目约定，不是所有厂商共同采用的标准。已有网关符合协议时只需配置；其他原生 API 需要在网关中转换，或新增 Go 适配器。网关必须将图片和配音真正传给目标模型，原始供应商订单 ID 及用量应保留。

```env
VIDEO_PROVIDER=你的供应商标识
VIDEO_API_PROTOCOL=http
VIDEO_API_BASE_URL=https://你的网关地址
VIDEO_API_KEY=在本机填写
VIDEO_MODEL=实际模型ID
VIDEO_RESOLUTION=720p
VIDEO_REFERENCE_AUDIO_SUPPORTED=true
VIDEO_UPLOAD_PATH=/media
VIDEO_GENERATION_PATH=/videos
VIDEO_QUERY_PATH=/videos/{id}
AVATAR_MVP_BASELINE_APPROVED=false
```

全部请求以 `Authorization: Bearer <VIDEO_API_KEY>` 认证，同一服务地址提供以下接口：

1. `POST /media`：multipart 字段 `file`；返回 `{"url":"https://可访问的素材地址"}`。
2. `POST /videos`：JSON 请求包含 `model`、`prompt`、`imageUrls`、`audioUrls`、字符串秒数 `duration`、`resolution`、`ratio`、`generateAudio`、`realPersonMode` 和 `outputFormat`；返回 `{"id":"供应商订单ID"}`。不得由网关静默重试付费提交。
3. `GET /videos/{id}`：返回 `{"id":"供应商订单ID","status":"running","url":"https://成功时的视频地址","usage":{}}`。状态支持 queued/pending、running/processing、succeeded/success/completed、failed/failure、cancelled/canceled，成功必须有 HTTPS 视频 URL。

未明确声明支持配音参考时，通用模式禁止生成。原生供应商若仅支持图片、不支持指定配音，需要另一条声音/口型路线，不能在当前流程里静默丢掉音频。

## 切换与恢复

新任务保存供应商、协议、模型和非敏感配置指纹。模型、地址或协议切换后，旧任务仍可查看和下载，但必须恢复原配置才能查单重试，避免跨厂商错单。仅轮换 Key 不影响恢复。

每个配置有独立的首条基准片门禁；切换模型时将 `AVATAR_MVP_BASELINE_APPROVED=false`，人工验证身份、机位、声音和嘴型后再设为 true。当前 MVP 一次使用一套服务端视频配置，不在用户页面填写密钥，也不自动切换厂商。
