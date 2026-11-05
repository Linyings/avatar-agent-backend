# Avatar Agent Backend

独立的 AI 数字人口播 Agent 项目。已确认的下一版目标是：七项创建输入、可选视频理解、写稿与内容拆解、分段 TTS 锁时、人物/动画/真实素材准备，再通过 talkcraft 拆镜组合并输出 MP4。数字人默认单角度上半身；风格、音色和成片比例可选择。当前版本定位为固定 Workflow，模型仅执行预设节点内的内容任务，无通用 Agent 自主推理或自我修复能力。

2026-11-05：已实现并部署 V2.2 固定工作流，旧固定三镜任务继续兼容查询。已有真实16.76秒竖屏样片及41.16秒横屏人物＋动画＋上传素材混合片，均采用分段真实配音；质检提醒不阻塞。41秒片的需求/分镜采用人工接续，原F05未知请求保留，不能代替完整自动规划验收。浏览器播放下载、Range与工程独立重渲染通过。完整P0、三人物、第二音色及效果审片尚未完成。详见 [实施验收矩阵](docs/workflow-v22-acceptance.md)。

本目录是 `avatar-agent-backend` 独立仓库，管理 Go 后端、媒体运行时、部署脚本和项目文档。`frontend/` 是单独克隆的 `avatar-agent-frontend` 仓库，拥有自己的 `.git`、提交和 remote，并被后端 `.gitignore` 排除；不是 submodule。两者可分别放在不同目录并独立版本管理，详见 [双仓库使用说明](docs/repositories.md)。上级参考项目不参与构建。

## 目录

```text
avatar-agent/
├── backend/                 # Go API、认证、数据库、任务与视频模型适配
│   ├── internal/avatar/     # RunningHub、MiniMax H3、HTTP 网关
│   ├── internal/server/     # MVP API、工作流、重试与鉴权
│   └── avatar-runtime/      # 独立 talkcraft / Remotion / Python 媒体工程
├── frontend/                # 独立 Git 仓库；不包含在后端克隆中
│   └── src/features/avatarMvp/
├── deploy/wsl2/             # Docker Compose、Nginx、部署和实机验证脚本
└── docs/                    # 计划、来源及整理说明
```

复用 NarratoAI 的基础源码作为本项目的 backend/frontend 起点，保留既有模块和锁文件，新增 MVP 功能在本项目内维护。Go module 名和部分上游产品文字暂时保留，不影响项目或部署隔离。视频合成只包含已收录的 talkcraft 必要源码，InfiniteTalk 尚未部署。

## 本机使用和更新

Windows PowerShell：

```powershell
cd D:\pro\ai_cat\avatar-agent
.\deploy\wsl2\deploy.ps1
# 仅更新一端
.\deploy\wsl2\deploy.ps1 -Service backend
.\deploy\wsl2\deploy.ps1 -Service frontend
# 前端克隆在其他位置时
.\deploy\wsl2\deploy.ps1 -Service all -FrontendPath D:\pro\avatar-agent-frontend
```

该入口将本项目 backend/frontend 同步到 WSL `/opt/aicat/avatar-mvp/src/backend` 和 `src/frontend`，构建并更新 Docker 容器。保留现有服务运行目录、Compose 项目名和持久卷，以继续使用原账号、数据库与素材；不会从上级参考仓库同步源码。

- 页面：<http://localhost:8080/avatar-mvp>；迁移时实测 IP 为 `172.17.234.187:8080`，WSL 重启后 IP 可能改变。
- 后端健康接口：<http://localhost:18000/health>；数据库不发布端口。
- 服务端配置：WSL `/opt/aicat/avatar-mvp/.env`。
- 登录信息：Windows `%LOCALAPPDATA%\AiCat\AvatarMVP\LOCAL_ACCESS.txt` 或 WSL `/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt`。
- 仅恢复服务：`.\deploy\wsl2\start.ps1`。

所有模型 Key 仅留在 WSL 服务端配置中。当前已实际使用 RunningHub 视频、豆包 TTS、Flash 文本/视觉理解模型；适配其他厂商不代表已经验证其指定音频口播效果。新流程使用稿件和声音两次版本确认；质量弹窗不用点击也继续，只有主动停止才阻止新的提交。操作、配置及日志见 [V2.2 使用说明](docs/workflow-v22-operations.md)。

历史测试报告只用于追溯旧任务，不作为当前 V2.2 的结果。新样片的 [成本耗时报告](docs/evidence/v22/case-a/成本耗时报告.md) 保留节点时间及真实回单；未返回价格或币种时不虚构金额。

## 说明

- [产品需求 PRD V1.10](docs/AI数字人口播Agent平台-PRD.md)（第 6 章功能及第 8、9 章固定工作流、错误分类、质检提醒继续与用户停止规则已同步）
- [开发计划 V2.2](docs/AI数字人口播Agent平台-开发计划.md)（先打通成片闭环，再补齐完整 P0）
- [V2.2 实施与验收](docs/workflow-v22-acceptance.md)
- [V2.2 节点与接口契约](docs/workflow-v22-contract.md)
- [历史最小 MVP 计划](docs/AI数字人口播Agent平台-最小MVP开发计划.md)（仅追溯旧范围，不作为新增功能执行依据）
- [WSL2 部署](deploy/wsl2/README.md)
- [MVP 运行说明](backend/docs/avatar-mvp.md)
- [视频模型配置](backend/docs/avatar-video-providers.md)
- [验证记录](backend/docs/avatar-mvp-verification.md)
- [参考源码版本](docs/upstream-projects.json)
- [全部 10 个参考项目：地址、功能与源码入口](../参考/README.md)
- [Opus 视频参考项目：功能与架构分析](docs/opus-video-reference-analysis.md)
- [混合型口播参考视频：画面拆解、导演方案与制作流程](docs/reference-video-BV1dxxM6mE8y-breakdown.md)
- [Opus 视频参考仓库版本索引](docs/video-reference-repositories.json)
- [目录整理记录](docs/project-layout.md)

上游许可与声明保留在对应源码中，talkcraft 来源和许可位于 `backend/avatar-runtime/UPSTREAM.json` 与 `TALKCRAFT-LICENSE`。本项目未重新授予上游代码的商业许可。

