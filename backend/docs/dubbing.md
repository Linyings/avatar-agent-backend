# PC 搞笑配音接口

专用入口 `/api/v1/tools/dubbing`，需登录。ASR 与人声分离提交/查询必须携带完整工作流标识，复用工作流的预留、完成结算和幂等机制。

## 配置

密钥只放后端环境；不要提交真实值或加入 PC 安装包。

```dotenv
DUBBING_ASR_MODEL=paraformer-v2
DUBBING_ACCESS_KEY_ID=
DUBBING_ACCESS_KEY_SECRET=
DUBBING_OSS_BUCKET=narratoai-dubbing-audio-shanghai
DUBBING_ASR_CREDITS_PER_MINUTE=2
DUBBING_SEPARATION_CREDITS_PER_MINUTE=34
DUBBING_TTS_CREDITS_MULTIPLIER=1.7
```

ASR/分离费率缺省为 0（不自动开放服务），TTS 倍率缺省为 1。上述配置按 29.9 元/3000 积分、模型毛利约 70% 制定。ASR 服务类型为 `dubbing_asr`；分离为 `audio_separation`，提供方 `aliyun-mps`、模型 `MusicDemix`。按实际秒数折算分钟，每次独立操作向上取整到积分。成本记录分别仍为 0.0048 元/分钟、0.1 元/分钟。

仅服务端已解析的 PC `dubbing-tts-` 工作流步骤将原有 TTS 积分费率乘以 1.7：当前 CosyVoice 为 17 积分/千字，FishAudio 为 34.646 积分/千字。供应商成本不乘倍率。普通剪辑及网页 TTS 费率不变，预检查与最终预留使用同一倍率。毛利不含 OSS、服务器和支付手续费；逐操作积分取整使实际毛利浮动，不保证精确 70%。已成功预留的步骤保持原金额。

## 接口

- GET `/capabilities`：就绪状态、模型和独立费率。
- POST `/asr/submit`、GET `/asr/query/:task_id`：专用 paraformer 入口，结构同现有 ASR；完整保留 `raw_result.transcripts[].sentences` 的说话人和时间。
- POST `/separation/submit`：JSON `audio_base64`（16 位 PCM WAV，最多 5 分钟、30MiB）加工作流标识。后端从字节验证时长，自行生成属于用户的 OSS 对象键，不接收任意对象路径。
- GET `/separation/query/:task_id`：工作流身份验证及任务归属核验后返回状态。成功且计费预留持久化后才返回短时伴奏下载链接。
- 上传 ASR 的临时文件继续使用 `/tools/dashscope/temp-files/upload-policy`，新增 provider `paraformer`。

服务地域固定上海。OSS 需保持私有读；任务输出模板为 `output-{resultType}`。建议为 `dubbing/` 前缀设置适当生命周期，当前最小 RAM 权限不包括删除对象。

MPS 提交没有幂等令牌，网络中断且无法确认云任务时不能自动重新提交。系统保留提交占用，需要管理员核对云端作业；不能通过清除占用自动重跑。

## 验证

`go test ./...`。`TestDubbingLive` 缺省跳过，必须显式设置 DUBBING_LIVE_TEST、凭据文件、音频文件及输出目录才调用收费云服务。普通测试不会调用外部模型。
