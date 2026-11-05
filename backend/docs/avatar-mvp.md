# 数字人口播 Agent MVP

实现位置：Go 服务、`avatar-runtime` 独立媒体工程，以及同级前端的 `/avatar-mvp` 登录页面。

源码现独立在 `D:\pro\ai_cat\avatar-agent`：后端 `backend/`，前端 `frontend/`，部署 `deploy/wsl2/`。上级 NarratoAI 仓库仅为参考，不再直接修改或参与构建。

人物照片 + 中文稿件走 video-talkcraft 的口播流程：口播稿 → 整稿配音与字级时间戳 → 语义标注 → 分镜。和原 skill 的差别只在素材一步：人物画面不是现成输入，而是用 Seedance 2.5 按照片和分段配音生成。生成后再做素材检查、SHOTBOOK、选卡、节拍校验和 Remotion 成片。

视频模型现支持 RunningHub、MiniMax H3 V2 和通用 HTTP 网关适配，配置与协议见 [视频供应商说明](avatar-video-providers.md)。服务端统一使用 `VIDEO_PROVIDER`、`VIDEO_API_BASE_URL`、`VIDEO_API_KEY`、`VIDEO_MODEL` 等配置；旧 RunningHub 配置仍兼容。新增供应商不改变认证、任务或渲染流程。

## 当前交付边界

- 已实现固定三镜基准流程、通过基准后的一次 LLM 分镜规划、服务端任务持久化、租约、查单、重试、鉴权视频接口与制作页。
- 已通过模拟供应商的流程测试和真正的 Remotion 合成测试。`data/avatar-render-smoke` 是几何画面与正弦音测试素材，**不是数字人样片**。
- 前端、Go API 与 PostgreSQL 已部署到本机 WSL2 Ubuntu-24.04 Docker，登录及容器内 Remotion 测试已通过。尚未配置 RunningHub、DashScope、Flash LLM 的真实凭据，也未提交真实付费生成。人物一致性、机位、原音色保留及嘴型同步尚未验收。
- InfiniteTalk 未接入。支付、积分预留、素材库和编辑器未增加。

## 1. 本地准备

默认使用工作区 `deploy/wsl2/deploy.ps1` 更新部署；仅恢复已构建服务使用 `deploy/wsl2/start.ps1`。入口为 `http://localhost:8080/avatar-mvp`，本次实测 WSL IP 为 `172.17.234.187:8080`。数据库、任务素材和模型缓存使用持久卷，禁止 `docker compose down -v`。服务端密钥在 WSL `/opt/aicat/avatar-mvp/.env` 配置；随机管理员信息保存在该目录 `LOCAL_ACCESS.txt` 及 Windows `%LOCALAPPDATA%\AiCat\AvatarMVP\LOCAL_ACCESS.txt`。

以下是独立开发环境的手动准备方式；后续日常调试以 WSL2 Docker 为准。

沿用原后端 PostgreSQL、认证与迁移配置。先按原 README 配置 `DATABASE_URL`、`SECRET_KEY`、`AUTO_MIGRATE` 并创建可登录用户；前端继续使用原注册/登录流程。

从后端根目录运行，要求 Go、Node、Python 3.10+（本机使用 3.12）、完整 FFmpeg/FFprobe：

```powershell
python -m venv .venv-avatar
.\.venv-avatar\Scripts\python.exe -m pip install -r avatar-runtime/requirements.txt
npm ci --prefix avatar-runtime
```

Linux 对应 Python 路径为 `.venv-avatar/bin/python`。前端独立执行 `npm ci`；不要合并两个 Node 工程的依赖或锁文件。

`env.example` 复制为 `.env` 后填写：

```dotenv
AVATAR_MVP_ENABLED=true
AVATAR_MVP_BASELINE_APPROVED=false
# 仅在明确授权新增一次基准测试时设置，值须与该次 client_request_id 相同。
# 创建成功后在持久任务状态中消耗；不批准基准，不关闭逐镜质量检查。
AVATAR_MVP_BASELINE_REQUEST_ID=
VIDEO_PROVIDER=runninghub
VIDEO_API_KEY=<本机服务端填写>
DASHSCOPE_API_KEY=<本机服务端填写>
QWEN_TTS_MODEL=qwen3-tts-flash
AVATAR_MVP_PYTHON=.venv-avatar/Scripts/python.exe
AVATAR_MVP_FFMPEG=<完整 ffmpeg 可执行文件路径>
AVATAR_MVP_FFPROBE=<完整 ffprobe 可执行文件路径>
```

基准片通过后，还需配置 `OPENAI_FLASH_API_KEY`、`OPENAI_FLASH_API_BASE_URL`、`OPENAI_FLASH_MODEL`，再启用 `AVATAR_MVP_BASELINE_APPROVED=true` 并重启。语音固定 `Cherry`，底层复用原 DashScope 客户端；模型可由已有 `QWEN_TTS_MODEL` 配置确定。

显式相对路径均按启动 Go 服务时的后端目录解析，媒体子进程切换目录后不受影响。只有 `python`、`node`、`ffmpeg` 等无目录的可执行文件名使用 PATH。密钥只留在服务端 `.env`，不填写任何前端 `VITE_*` 密钥。

可提前下载 CPU 识别权重，避免第一条任务等待下载：

```powershell
.\.venv-avatar\Scripts\python.exe -c "from faster_whisper import WhisperModel; WhisperModel('small', device='cpu', compute_type='int8')"
.\.venv-avatar\Scripts\python.exe -c "import sys; sys.path.insert(0, 'avatar-runtime/talkcraft'); import face_bbox; face_bbox.get_model()"
.\.venv-avatar\Scripts\python.exe avatar-runtime/pipeline.py doctor --ffmpeg <完整ffmpeg路径> --ffprobe <完整ffprobe路径>
```

离线时使用本地 faster-whisper 模型目录设置 `AVATAR_MVP_ASR_MODEL_DIR`。可选 FireRed 后端还需自行安装 sherpa-onnx、soundfile 及对应权重，首版默认 Whisper。

启动：后端 `go run ./cmd/api`；前端在 `frontend` 执行 `npm run dev`，按原项目方式配置 `VITE_API_BASE_URL=http://localhost:8000`，登录后打开 `/avatar-mvp`。

本机已准备 Python 虚拟环境与独立渲染依赖；完整媒体工具位于 `data/tools/ffmpeg-9.0.2-essentials_build/bin`。它们是本地未跟踪依赖，不随 Git 分发。当前没有填入真实密钥。

## 2. 首条基准片

1. 上传单人 JPG/PNG（8MB 内，宽高 256–4096），填写至少三句中文稿件、可选标题。先用约 15 秒稿件；配音总长限制 6–30 秒，每镜 2–15 秒。
2. 固定使用正面中景、左前方 20° 半身、右前方 20° 近景，同一张照片及外观提示。固定稿件按顺序分组；实际边界取相邻语句的停顿中点。
3. 依次上传原图、当前分段配音，再提交生成。使用上传返回的 `download_url`，不把内部文件名当 URL；每次新提交重传原文件，避免过期链接。
4. 检查返回视频的帧率、竖屏尺寸、音轨、原音频相关度、实际识别台词、可覆盖时长及实测人脸安全区。一镜失败即停止后续提交，保留报告。波形相关度阈值是工程检查，不能证明嘴型正确，也不能证明人物身份或视角合格。
5. 成片只混入一次原始整稿配音，逐镜素材静音并按音画偏移定位，不循环人物、不任意变速补齐。人物保留生成背景，卡片和字幕区域与人物分开；口播镜头硬切，转场只作用于文字卡片。
6. 人工检查 `shot-N/qa-first.jpg`、`qa-middle.jpg`、`qa-last.jpg` 和视频：身份/声音一致、至少两机位有真实透视变化、嘴型同步、无漏读重读。通过前不能创建第二条新任务，可以重试原任务。

开启基准通过开关只表示操作者已完成上述人工评审，不是自动质量判定。若音频参考不能满足固定发音与嘴型目标，保留样片和报告，停止实验；不自动换声音、换模型或启动 InfiniteTalk。

## 3. API 与数据

所有接口沿用登录 Bearer JWT：

| 方法 | 路径 | 输入 / 输出 |
|---|---|---|
| GET | `/api/v1/avatar-mvp/config` | 启用、必要凭据、基准通过状态；不返回密钥 |
| POST | `/api/v1/avatar-mvp/tasks` | multipart：`photo`、`script`、`title`、UUID `client_request_id`；202 返回任务 |
| GET | `/api/v1/avatar-mvp/tasks/{id}` | 整体/逐镜状态、错误、耗时、素材元信息、供应商 ID |
| POST | `/api/v1/avatar-mvp/tasks/{id}/retry` | 202，人工恢复失败或中断步骤 |
| GET | `/api/v1/avatar-mvp/tasks/{id}/video` | 所有权检查后返回 MP4，支持 Range |

同用户同 `client_request_id` 返回原任务，浏览器 POST 失败不自动发起新的付费重试。全局只容纳一个活动任务，忙时 409；配置缺失返回 503。其他用户不能读取、下载或重试任务。

复用 `projects.project_metadata` 记录输入与模型配置，`workflow_runs` 记录工作流，`workflow_step_executions` 的 `avatar.task` 保存可恢复任务快照，`avatar.task.shot-N` 记录逐镜步骤与供应商 ID。用量保存为任务报告中的 TTS 字符数、LLM usage、视频供应商返回的原始 usage（如有）；未返回的费用不估算为实扣，也不占用现有用户积分。

每 20 秒服务端续租，浏览器关闭不影响执行；每 30 秒检查失效租约。服务重启后失效任务标记为 interrupted，等待人工重试。单实例执行器与 PostgreSQL advisory transaction lock 约束新任务和重试并发。MVP 请运行一个 Go 实例。

## 4. 恢复与排查

- 查询超时：已有供应商任务 ID 保持不变，重试继续查询原单。
- 网络断在提交阶段且无法确认任务 ID：记录 submitting/unknown 并禁止重新提交。需在原视频供应商核对；目前没有自动猜测订单或后台清除状态的入口。必要时管理员核实后修正数据库快照，操作前备份。
- 供应商明确失败：仅人工重试才新建对应镜头请求。
- 素材验证失败：停止后续生成。点击重试失败步骤会将该镜头被拒绝的视频及检查记录移入 `shot-N/attempt-N`，重新生成该镜头，**可能再次产生供应商费用**；成功镜头直接复用。先查日志再重试，避免反复对不适配的模型付费。
- 生成成功但下载失败：继续查原任务并重新下载；每次结果查询取得当前结果 URL。
- 合成失败：复用已验证镜头、配音及时间映射，仅重新渲染。
- Python、FFmpeg、ASR 权重等运行依赖导致检查失败：保留已下载源片，重试先重新检查，不新建该镜头的付费任务。

任务文件在 `AVATAR_MVP_DATA_ROOT/{task_id}`：`photo`、`voice.wav`、`request.json`、`timestamps.json`、`timing.json`、`clips.json`、`shot-N/{audio.wav,source.mp4,verified.json,*check.json,face-zone.json,qa-*.jpg}`、`manifest.json`、`task.json`、`report.json`、`output.mp4`、`talkcraft-project`。失败日志为 `prepare.log`/`verify.log`/`render.log`；环境检查日志在 `avatar-runtime/.cache/doctor.log`。这些目录含用户素材，不作为静态 Web 目录公开。

`talkcraft-project` 保存改编后的源文件、manifest 和 SHOTBOOK；复现渲染还需同一任务的原配音/镜头文件，及 `avatar-runtime/render.mjs`。从后端执行 `node avatar-runtime/render.mjs <任务绝对路径>` 即可重新渲染。

## 5. 测试与许可

```powershell
go test ./internal/avatar
go test ./internal/server -run TestAvatar
.\.venv-avatar\Scripts\python.exe -m unittest discover -s avatar-runtime -p test_pipeline.py
npm test --prefix avatar-runtime
npm run check --prefix avatar-runtime
.\.venv-avatar\Scripts\python.exe scripts/avatar_render_smoke.py --ffmpeg <完整ffmpeg路径>
```

前端：`npm exec vitest run -- src/features/avatarMvp`、`npm run build`。浏览器合同测试位于 `tests/e2e/avatar-mvp.spec.ts`，全部供应商响应为测试 fixture；不属于真实模型验收。

在前端目录先启动 `npm run dev -- --host 127.0.0.1 --port 5173`，再执行：

```powershell
$env:E2E_BASE_URL='http://127.0.0.1:5173'
$env:E2E_USE_REAL_API='1'
$env:PLAYWRIGHT_CHROMIUM_EXECUTABLE='C:\Program Files\Google\Chrome\Application\chrome.exe'
npm exec playwright test -- tests/e2e/avatar-mvp.spec.ts --workers=1
```

这里沿用原 Playwright 配置，`E2E_USE_REAL_API=1` 只用于跳过其托管 dev server；本测试文件始终拦截 API，并不请求真实供应商。Chrome 路径按本机修改；若使用 Playwright 自带浏览器，则先安装浏览器并省略该环境变量。

talkcraft 的有限源代码来源、哈希及改编脚本见 `avatar-runtime/UPSTREAM.json`、`scripts/sync_avatar_talkcraft.py`，许可证原文保存在 `avatar-runtime/TALKCRAFT-LICENSE`。上游为 PolyForm Noncommercial，商业使用需落实作者授权。没有把上游整个仓库重新声明为本项目许可证。Remotion 的使用条件也需按其现有授权范围执行。
