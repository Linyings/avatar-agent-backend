# V2.2 本机操作和排错

2026-11-05 实测页面：<http://127.0.0.1:8080/avatar-mvp>，WSL 地址 <http://172.17.234.187:8080/avatar-mvp>。IP 在 WSL 重启后可能改变。API 仅绑定本机 18000；数据库不公开端口。

## 正常制作

1. 使用本机账号登录，填写七项输入。主题/原稿至少一项；使用数字人时上传照片；源视频可选。默认正面上半身。
2. 先看风格动态示例，选比例、声音。有明确多机位要求才启用多角度。目录试听没准备好时不要把它当作已验证音色。
3. 保存草稿并新建运行。有视频先视觉理解，再写稿/拆解。检查和编辑拆解结果后确认；此时还不生成画面。
4. 逐段配音完成后试听，检查实际时长，再确认声音。之后输出素材需求并依次准备人物、动画、真实源区间。
5. talkcraft 接入实际素材、语义拆镜、有声预览、渲染。完成后可播放、下载 MP4/SRT/封面/工程/映射/报告。

地址包含 project_id 和 run_id；复制仅含 run_id 的地址也能恢复项目。后台每两秒查询，关闭浏览器不停止任务。质检提醒不需要点击，不会自动停工或重生成。波形相关度是音轨诊断，不是嘴型合格分数。

## 局部修改与错误

已结束运行可修改段落、素材需求、模板、提示词、分镜、字幕颜色/字号/同字数更正和人物位置；保存后创建新版本，保留原片。修改时复用依赖未变的已确认声音和人物，必要下游重新处理；改读稿内容需要重新配音，不能只改显示字幕假装声音同步更新。

模型鉴权、额度、网络及5xx显示具体类型；必要产物缺失需要恢复，独立批次或素材继续。点击“查单恢复/重试”先查询原订单；无 Task ID 的提交待确认禁止盲重交。供应商失败订单不会自动退款或取消。

“停止任务”持久化停止意图，之后不再准入新的模型提交；已准入/已提交订单可能继续在供应商执行，其费用和订单记录保留。本地命令退出后才释放冲突租约。新流程没有质检等待门禁；旧任务继续保留旧版本记录。

## 配置与更新

密钥在 WSL `/opt/aicat/avatar-mvp/.env`，浏览器不接触密钥。当前使用 RunningHub 视频及豆包配音；Flash 文本配置用于写稿、需求和分镜，显式 `AVATAR_VISION_USE_FLASH=true` 时复用其图像理解能力。独立视觉服务仍可配置，实际能力必须按厂商协议验证。

`VIDEO_PROVIDER`、`VIDEO_API_PROTOCOL`、`VIDEO_API_BASE_URL`、`VIDEO_API_KEY`、`VIDEO_MODEL` 等用于切换视频服务；不同协议必须有真实适配。`TTS_MODEL`、`TTS_SPEAKER`、`AVATAR_TTS_VOICES` 控制配音及可选音色，不能因为列在目录就假称供应商已支持。`AVATAR_CONTENT_TIMEOUT_SECONDS` 设置节点模型请求时限。

Windows PowerShell 更新：

```powershell
cd D:\pro\ai_cat\avatar-agent
.\deploy\wsl2\deploy.ps1
```

脚本先备份现有数据库和配置到 WSL `backups/`，再更新本项目源码和容器；保留账号、卷和缓存。登录信息文件是 `%LOCALAPPDATA%\AiCat\AvatarMVP\LOCAL_ACCESS.txt`。不要删除持久卷。

## 日志与证据

以下从 Windows PowerShell 执行，不会打印密钥：

```powershell
wsl -d Ubuntu-24.04 -u root -- bash -lc 'cd /opt/aicat/avatar-mvp && docker compose ps'
wsl -d Ubuntu-24.04 -u root -- bash -lc 'cd /opt/aicat/avatar-mvp && docker compose logs --tail=100 api web'
```

节点具体状态、耗时、尝试、费用和错误以网页运行记录及成本 JSON 为准。API 容器内路径：

文本模型失败的新增日志保留 `error_code`、`transport_phase`、可获取的 `http_status` 与安全请求ID；例如 network_dns、network_timeout、connection_interrupted、response_invalid_json、response_no_choices、model_invalid_json。历史失败不会被倒填为新的推测原因。HTTP200但响应无效也不能视作成功或确认未收费，未知提交保护仍有效。

```text
/app/data/avatar-mvp/workflow-v2/runs/<run_id>/
  workflow.json / report.json / operations.jsonl
  frames.log / audio.log / material.log / render.log
  p1/audio.wav / p1/timestamps.json
  materials/ / pieces/ / qa/ / preview/ / segments/
  SHOTBOOK.md / workflow-manifest.json / output.mp4 / project.zip
```

各媒体步骤同名日志会随再次执行更新；模型结果和逐尝试订单另有持久记录。数据库为运行状态权威来源，`workflow.json`是媒体步骤的输入快照，不应通过手改 JSON 推动运行。生成的工程只包含受控素材引用，不含 `.env`、登录令牌或临时签名链接。

目录试听订单独立放在 `workflow-v2/catalog/`。目录工具只供显式操作员调用，页面浏览不发起付费生成。提交状态不明时核对供应商控制台，不删除订单文件绕过保护。

可读成本报告可从已导出 state.json 生成，不会调用模型：

```powershell
python deploy/wsl2/summarize-workflow.py docs/evidence/v22/case-a/state.json docs/evidence/v22/case-a/成本耗时报告.md
```

工程复现检查工具：`backend/scripts/verify_workflow_export.py`。真实验收脚本：`deploy/wsl2/verify-workflow-live.py`（需显式 `--paid`，支持 `--pause-at script/audio`，不会自动重交失败或未知订单）。旧表单 UI 及其浏览器验收脚本已于 2026-11-06 删除；当前新版首页和 Agent 工作台使用 `deploy/wsl2/verify-current-browser.cjs` 验收。

`prepare-local-acceptance-revision.py` 是操作员的风格/比例修订工具：先检查既有人物缓存、配音和对齐文件，复用原需求/分镜并保存幂等准入。它不会重试原来状态不明的请求。缓存预检不能替代记账，完成后仍检查新运行的订单和 billing；草稿已有其他修改时拒绝覆盖。修订仍沿用原需求/分镜的来源，人工分镜不会因此变成自动规划验收。

`backend/scripts/inspect_workflow_media.py <任务目录>` 离线核对成片帧数、音轨、时间线与源区间，并导出逐镜静帧。静帧和波形不能代替完整嘴型、声音及身份审片。换风格/比例复用人物时，服务会保留未变化素材的历史质检提醒，重新生成的素材不沿用旧检查。
