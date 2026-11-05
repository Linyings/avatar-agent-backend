# Avatar Agent 项目约定

- 本目录现为 `avatar-agent-backend` 仓库根（管理 backend、deploy、docs）；frontend 是独立的 `avatar-agent-frontend` 仓库，已被本仓库忽略，不是 submodule。后端 Git 操作在本目录，前端 Git 操作必须使用 `git -C frontend ...`。不要将 frontend 强制加入后端索引，不继承参考仓库 remote。
- 前后端分别提交、打标签和推送；本机部署可用 `deploy/wsl2/deploy.ps1 -Service backend|frontend|all`。前端放在其他目录时传 `-FrontendPath`。规范见 docs/repositories.md。
- 这是用户的独立项目，日常开发只修改本目录内的 backend、frontend、deploy 和 docs。
- 上级 `参考/` 目录统一保存 NarratoAI-SaaS-B-Go、NarratoAI-SaaS-F、video-talkcraft、video-shotcraft、mg-styles-15、InfiniteTalk、lemo-opuscar、awesome-opus5-5-videos、opus-video-prompts、onetake。先阅读 `../参考/README.md` 查找功能与入口；禁止直接修改参考项目源码或在其中开发本项目。
- 新增视频参考项目的固定版本、阅读入口和许可记录见 docs/video-reference-repositories.json；功能与架构分析见 docs/opus-video-reference-analysis.md。它们是参考，不表示已经集成或部署。
- 使用中文交互；后端沿用 Go，前端沿用 Mantine 与已有锁文件；遵循 TDD，对影响行为的改动先写有意义的测试。
- 前端、Go API 和数据库统一部署到本机 WSL2 Docker；默认更新入口为本项目 deploy/wsl2/deploy.ps1。日常修改后必须更新部署并验证，不只交付 Windows 构建结果。
- 保留 /opt/aicat/avatar-mvp 中的服务端配置、账号、数据库、任务素材及模型缓存；禁止 docker compose down -v。
- 当前 WSL2 发行版 `Ubuntu-24.04` 已迁移到 `D:\WSL\Ubuntu-24.04`。后续所有部署与调试使用此 D 盘发行版；Linux 部署位置继续为 `/opt/aicat/avatar-mvp`。
- 部署后反馈实测 IP 和端口。模型 Key 只保存在 WSL 的服务端 .env，不进 Git、前端或日志。
- 保留复用源码的许可与来源记录。后端 Go module 和前端依赖包名保留上游名称，以维持既有导入及锁文件；这不表示使用参考目录构建。

