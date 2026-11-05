# Avatar Agent

独立的 AI 数字人口播 Agent 项目。已确认的下一版目标是：七项创建输入、可选视频理解、写稿与内容拆解、分段 TTS 锁时、人物/动画/真实素材准备，再通过 talkcraft 拆镜组合并输出 MP4。数字人默认单角度上半身；风格、音色和成片比例可选择。当前版本定位为固定 Workflow，模型仅执行预设节点内的内容任务，无通用 Agent 自主推理或自我修复能力。

当前已部署的基础 MVP 仍有固定三镜等旧链路，完整真实人物成片尚待验收；上述目标以 PRD 和开发计划为准，不代表已交付。实际部署与测试结果见下文及进度记录。新文档要求质检仅提醒并继续制作，由用户主动停止；本次只更新文档，已部署代码的旧质量门禁尚未按此规则调整。

本目录是唯一开发源码根目录，使用一个独立 Git 仓库；没有连接参考项目的 Git remote。上级目录各项目仅作为参考，部署与运行不依赖它们。

## 目录

```text
avatar-agent/
├── backend/                 # Go API、认证、数据库、任务与视频模型适配
│   ├── internal/avatar/     # RunningHub、MiniMax H3、HTTP 网关
│   ├── internal/server/     # MVP API、工作流、重试与鉴权
│   └── avatar-runtime/      # 独立 talkcraft / Remotion / Python 媒体工程
├── frontend/                # React / Mantine 制作页面与登录
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
```

该入口将本项目 backend/frontend 同步到 WSL `/opt/aicat/avatar-mvp/src/backend` 和 `src/frontend`，构建并更新 Docker 容器。保留现有服务运行目录、Compose 项目名和持久卷，以继续使用原账号、数据库与素材；不会从上级参考仓库同步源码。

- 页面：<http://localhost:8080/avatar-mvp>；迁移时实测 IP 为 `172.17.234.187:8080`，WSL 重启后 IP 可能改变。
- 后端健康接口：<http://localhost:18000/health>；数据库不发布端口。
- 服务端配置：WSL `/opt/aicat/avatar-mvp/.env`。
- 登录信息：Windows `%LOCALAPPDATA%\AiCat\AvatarMVP\LOCAL_ACCESS.txt` 或 WSL `/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt`。
- 仅恢复服务：`.\deploy\wsl2\start.ps1`。

所有模型 Key 仅留在服务端配置中，网页暂不编辑密钥。2026-11-04 已配置真实模型：本轮文本分镜与豆包配音调用通过；已有相同照片的 Seedance 首镜生成成功，但指定音轨质量检查未通过，完整成片仍待验收。协议测试和几何渲染测试不等于数字人验收。详见 [当前进度](docs/progress.md) 和 [真实测试报告](docs/mvp-test-20261104.md)。

用户授权新增的一次测试已完成配音、字级对齐和三段切分；视频提交未取得可确认的订单 ID，已停止且没有重发。已定位并修复该模型短镜头最低 4 秒的请求规则，原订单仍待供应商核实，不作为成片成功结果。

## 说明

- [产品需求 PRD V1.10](docs/AI数字人口播Agent平台-PRD.md)（第 6 章功能及第 8、9 章固定工作流、错误分类、质检提醒继续与用户停止规则已同步）
- [开发计划 V2.1](docs/AI数字人口播Agent平台-开发计划.md)（M0–M6 任务、非阻塞质检/用户停止、交付标准、工作量及 WSL2 验证；替代旧固定三镜路线）
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

