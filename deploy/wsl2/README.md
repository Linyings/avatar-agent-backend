# 本机 WSL2 Docker 部署

工作区默认部署目标为本机 Ubuntu-24.04，Linux 文件位于 `/opt/aicat/avatar-mvp`。

前后端已独立版本管理：此脚本属于后端仓库，`frontend/` 是另一个独立仓库的默认检出位置。后端克隆中不会包含前端；首次完整部署需另外克隆前端，或通过 `-FrontendPath` 指定其位置。

```powershell
.\deploy\wsl2\deploy.ps1 -Service all
.\deploy\wsl2\deploy.ps1 -Service backend
.\deploy\wsl2\deploy.ps1 -Service frontend
.\deploy\wsl2\deploy.ps1 -Service all -FrontendPath D:\pro\avatar-agent-frontend
```

`backend` 只同步、构建和更新 API，`frontend` 只同步、构建和更新 Web；单端更新要求已有完整部署。仅后端更新无需前端检出。Bash 对应 `bash deploy/wsl2/deploy.sh all|backend|frontend [前端仓库路径]`，也可设置 `AICAT_FRONTEND_SOURCE`。原 `deploy-web.sh` 继续作为仅前端更新入口。完整部署仍按顺序构建两端。

部署验证成功后，WSL `DEPLOYED_REVISIONS.json` 分别记录前后端 Git commit、工作区是否有未提交改动和部署时间；单端更新保留另一端的记录，不读取或记录密钥。日常使用与克隆说明见 [双仓库文档](../../docs/repositories.md)。

部署源码来自独立项目 `D:\pro\ai_cat\avatar-agent` 的 `backend/` 和 `frontend/`，同步到 WSL `src/backend` 和 `src/frontend`。上级参考仓库不参与构建。服务目录与 Compose 名称保留，以继续使用原数据库和素材卷。

2026-11-05 已更新 V2.2：`http://172.17.234.187:8080/avatar-mvp` 与 `http://localhost:8080/avatar-mvp` 可访问，三个服务健康。真实模型已配置，新流程交付16.76秒人物＋动画成片；浏览器播放/下载/Range与工程独立重渲通过。完整P0及人工效果评审仍未全部完成，见 `docs/workflow-v22-acceptance.md` 和 `docs/workflow-v22-operations.md`。

在 Windows 更新代码后执行：

```powershell
wsl -d Ubuntu-24.04 -u root -- bash /mnt/d/pro/ai_cat/avatar-agent/deploy/wsl2/deploy.sh
```

脚本将代码同步到 Linux 文件系统并重建/更新 Docker 服务，保留 PostgreSQL、任务目录、模型缓存及 `.env`。前端通过同源 Nginx 转发 API；不会要求把 JWT 或模型密钥放入地址栏。

推荐 Windows 入口为 `deploy/wsl2/deploy.ps1`，完成部署后也会启动隐藏的 WSL 保活进程。仅需恢复已构建服务时执行 `deploy/wsl2/start.ps1`；它不重建镜像、不清除数据。

默认入口 `http://localhost:8080/avatar-mvp`，也可使用实测的 WSL2 IP:8080。API 本机端口为 18000，数据库不公开端口。

初始管理员登录信息保存在 WSL2 `/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt`，通过 `wsl -d Ubuntu-24.04 -u root -- cat /opt/aicat/avatar-mvp/LOCAL_ACCESS.txt` 查看。密码随机生成，不随代码提交。

Windows 部署入口会将登录信息另存到 `%LOCALAPPDATA%\AiCat\AvatarMVP\LOCAL_ACCESS.txt`，便于直接打开；该文件不在源码目录。部署脚本自动验证健康检查、未登录拒绝、管理员登录及 MVP 配置接口。`verify-browser.cjs` 可用本机 Chrome 对实际 WSL HTTP IP 服务做登录、刷新及响应式页面验证，不 mock API。

第三方服务配置文件为 WSL2 `/opt/aicat/avatar-mvp/.env`；修改后，在该目录运行 `docker compose up -d`。未配置密钥时明确显示配置缺失，不产生模拟结果。`AVATAR_MVP_BASELINE_APPROVED` 仅适用于历史任务基准逻辑；新V2.2不以质量提醒阻断制作，仍保留稿件和配音的内容确认。

视频生成可切换 RunningHub、MiniMax H3 V2，或其他供应商的通用 HTTP 网关，详见后端 `docs/avatar-video-providers.md`。统一配置为 `VIDEO_PROVIDER`、`VIDEO_API_PROTOCOL`、`VIDEO_API_BASE_URL`、`VIDEO_API_KEY`、`VIDEO_MODEL`、`VIDEO_RESOLUTION`。各原生协议不同，不能仅替换 Key 假定接口兼容；切换厂商不会自动对旧订单重新付费提交。

诊断：`docker compose ps`、`docker compose logs --tail=100 api web`。禁止使用 `docker compose down -v` 更新服务，以免删除用户数据。WSL2 地址在重启后可能改变，本机优先使用 localhost。
