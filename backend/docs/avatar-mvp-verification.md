# MVP 开发验证记录

日期：2026-11-03；Windows 及本机 WSL2 Docker 执行。当前状态为**核心代码已部署，真实模型验收待进行**。

**独立项目整理**：本记录现归属 `avatar-agent/backend`。已将源码迁移到独立项目并恢复参考仓库；迁移后的测试、真实部署、数据库保留和登录/渲染结果见 [目录整理记录](../../docs/project-layout.md)。旧路径只保留在下文历史验证描述中。

## 已通过

| 检查 | 结果 |
|---|---|
| Go `internal/avatar` | 7 个测试通过：RunningHub 协议、异常响应、提交不明、保密错误、原稿覆盖与运行路径 |
| Go `internal/server` 的 `TestAvatar*` | 11 个测试通过：三镜执行、无积分预留、检查失败停止后续生成、复用订单、人工质量重试、运行故障复用源片、归属/Range/登录、全局忙、幂等、基准门禁、重启恢复 |
| Go API 构建 | `go build ./cmd/api` 通过 |
| Python 媒体计算 | 4 个测试通过：偏移实测、不同声音拒绝、停顿边界、短源片拒绝 |
| 渲染工程 | TypeScript 检查通过，4 个 Node 测试通过：帧边界连续、重复时间拒绝、人脸安全区映射与机位缩放约束 |
| 环境 doctor | Python 3.12 虚拟环境、媒体依赖、完整 FFmpeg 9.0.2、FFprobe、Node 检查通过 |
| 前端 | MVP 6 个单元测试；连同登录路由回归共 10 个通过，目标 ESLint、Docker 内 TypeScript/Vite 正式构建通过 |
| 浏览器 | 3 个 Chrome 合同测试通过：提交/轮询/刷新/视频 Blob/下载、手机配置错误与禁用、任务地址恢复与人工重试 |
| 真实渲染器合成 | Remotion 产出 720×1280、25fps、154 视频帧、单个音轨的三镜 MP4；人工查看抽帧并调整卡片字号/区域 |

合成输入是 6.125 秒几何动画与正弦音，不含真人或生成模型结果。帧数 `ceil(6.125×25)=154`，封装时长约 6.208 秒（包含 AAC 尾部）。文件在本机 `data/avatar-render-smoke/output.mp4`，不是用户验收成片。

浏览器测试显式 mock API，使用自制 0.6 秒几何 MP4 测试可解码的鉴权视频 Blob。没有绕过生产认证，也没有在生产接口加入模拟成功分支。页面沿用原项目登录壳，首版中文；用 `data-i18n-skip` 避免旧 DOM 翻译器将实时计数和任务状态恢复成初始文本，浏览器断言覆盖计数及三镜完成状态。

页面截图：[桌面](avatar-mvp-desktop.png)、[手机](avatar-mvp-mobile.png)。桌面预览是上述几何 fixture，手机展示未配置服务时的真实界面分支。

## 原有测试的限制

`go test ./...` 未全绿，失败日志在本机 `data/go-tests.log`。对原始 HEAD 的独立导出执行失败项后，下列六组既有失败仍可复现：

- `TestGeneralNarrationV3VersionsAndReplays`
- `TestFrameQualityGateWorkflowIdentityProviderFailureFinalizesFailedStep`
- `TestFrameWorkflowIdentityProviderFailureFinalizesWithoutReservation`
- `TestFrameWorkflowSucceededReplayProviderFailurePreservesSuccess`
- `TestTimelineToolWorkflowIdentityProviderFailureFinalizesWithoutReservation`
- `TestResolveWorkflowProviderQuerySurvivesDatabaseRestart`（Windows SQLite 文件占用清理）

另有原有 `TestFrameWorkflowNarrativeIdentityKeepsStreamAliveWithoutEarlyReplay` 在全量执行中出现租约续期断言失败，原始 HEAD 定向执行时通过；该时序问题尚未确诊。本次未改写原工作流逻辑来隐藏这些失败。原始对照日志在 `data/baseline-tests.log`。

Go SQLite 测试在本机使用 CGO + Zig cc；依赖下载使用 GOPROXY 镜像，未改仓库依赖版本或已有锁文件。渲染工程单独固定依赖并生成自己的锁文件。

## 尚未验收

PostgreSQL 已在本机 WSL2 Docker 配置并完成迁移，真实登录、用户信息、未登录拒绝及 MVP 配置接口已通过。当前未配置 RunningHub / DashScope / Flash LLM 真实凭据，也未提供人物照片；没有调用付费生成接口。以下项目必须在启用服务后用真实素材验证：

1. 同图三角度样片的身份、原音色、指定台词、真实透视变化与嘴型同步；保存首/中/尾帧及人工结论。
2. 第一条约 15 秒成片，通过后才开启自动分镜与后续新任务。
3. 三组不同人物和稿件的完整链路、实际费用/时延，及最长 30 秒素材。
4. 本机 PostgreSQL 环境下重启查单、活动任务限制及失败恢复。

`audioUrls` 支持不等于精确口型已获保证。若模型输出改变声音或节奏，流程将检查失败并停止后续提交；不会自动切换 InfiniteTalk。人物身份和机位、嘴型最终仍需人工观看样片。

配置、API、任务目录、重试规则与许可见 [运行说明](avatar-mvp.md)。

## WSL2 Docker 部署验证

2026-11-03 部署到本机 Ubuntu-24.04（WSL2），Docker CE 29.9.2、Compose 5.7.0。代码同步到 `/opt/aicat/avatar-mvp/src` 构建，前端、Go API 与 PostgreSQL 17 均运行于 Docker；任务文件、数据库及模型缓存各有持久卷。

- Windows 实测 `http://172.17.234.187:8080/avatar-mvp`、`http://127.0.0.1:8080/avatar-mvp` 均返回 200，API `127.0.0.1:18000/health` 返回 200；三个服务健康。
- PostgreSQL 迁移与随机管理员初始化完成；实际登录、用户信息、鉴权拒绝及配置查询通过。配置明确返回 RunningHub 尚未配置，未调用付费模型。
- Linux Docker 内 `internal/avatar` 与 `internal/server` 的 MVP 定向测试通过。
- Windows Chrome 直接访问 WSL HTTP IP，使用真实管理员登录，制作页、刷新、手机尺寸及无脚本错误验证通过，无 API mock。截图在本地 `data/wsl-deployment`。
- 修复 HTTP IP 缺少 `crypto.randomUUID` 的兼容问题，以及原登录保护路由抢先跳回首页的问题；登录路由回归先失败后通过。反向代理保留 Host 端口，支持 IP 同源登录。
- 容器内以生产运行用户、生产媒体目录执行真正的 Remotion 合成：720×1280、25fps、154 视频帧、单音轨、封装时长 6.208 秒。产物在任务持久卷 `/app/data/avatar-render-smoke/output.mp4`，仍是几何测试素材，不计作数字人验收样片。

后续修改调试统一更新 WSL2 部署；Windows 登录信息文件不在 Git 工作区，真实 API Key 只保存在 WSL 服务端 `.env`。

## 视频供应商改造验证（2026-11-03）

视频配置改为供应商、协议、地址、Key、模型 ID 和分辨率独立配置；原生支持 RunningHub、MiniMax H3 V2，其他厂商可经通用 HTTP 网关或新增 Provider 实现接入。没有把通用网关协议描述为任何厂商的原生协议。

- Linux Docker 定向 Go 测试通过：原 MVP 流程与 RunningHub 协议回归，MiniMax 上传用途/文件名/大整数 ID、图片及音频 reference role、V2 提交/GET 查单、状态与 usage 映射、异常/未知任务/空结果、HTTP 错误保密及提交不明、H3 各版本最短时长和分辨率、其他厂商 HTTP 网关合同，以及跨供应商配置/订单隔离与 Key 轮换。
- 前端 MVP 与登录回归共 11 个单元测试通过，目标 ESLint、Docker 内 TypeScript/Vite 正式构建通过；新断言覆盖 MiniMax 供应商和模型展示。
- 已更新现有 WSL2 Docker 部署，保留 PostgreSQL 与任务/模型卷。三个服务健康，Windows 的 IP:8080、localhost:8080 和 API localhost:18000 均返回 200。
- Chrome 直接访问真实 WSL HTTP IP 服务，真实登录、当前供应商/模型展示、刷新、手机布局及无页面脚本错误检查通过，无接口 mock。

目前仍保留原默认 RunningHub 配置；服务端填写 `VIDEO_PROVIDER=minimax` 与独立 Key 后即可切换 H3。没有配置或调用任何真实付费视频生成接口，协议测试不替代人物/嘴型样片验收。配置示例及其他厂商接口边界见 [视频供应商说明](avatar-video-providers.md)。
