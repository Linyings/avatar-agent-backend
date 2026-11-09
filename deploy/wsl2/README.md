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

当前默认入口为 `http://localhost:8080/`（新版首页），Agent 工作台为 `/workspace/:projectId`；也可使用实测的 WSL2 IP:8080。旧版测试表单 UI 已删除，`/avatar-mvp` 自动返回首页。API 本机端口为 18000，数据库不公开端口。

初始管理员登录信息保存在 WSL2 `/opt/aicat/avatar-mvp/LOCAL_ACCESS.txt`，通过 `wsl -d Ubuntu-24.04 -u root -- cat /opt/aicat/avatar-mvp/LOCAL_ACCESS.txt` 查看。密码随机生成，不随代码提交。

Windows 部署入口会将登录信息另存到 `%LOCALAPPDATA%\AiCat\AvatarMVP\LOCAL_ACCESS.txt`，便于直接打开；该文件不在源码目录。部署脚本自动验证健康检查、未登录拒绝、已保存账号登录和 Agent 路由。首个保存账号失效时会尝试后续完整账号，不改密码。`verify-current-browser.cjs` 使用本机 Chrome 实测新版首页、登录、刷新、旧地址跳转、项目列表及已有项目工作台，不 mock API、不提交模型任务。

第三方服务配置文件为 WSL2 `/opt/aicat/avatar-mvp/.env`；修改后，在该目录运行 `docker compose up -d`。未配置密钥时明确显示配置缺失，不产生模拟结果。`AVATAR_MVP_BASELINE_APPROVED` 仅适用于历史任务基准逻辑；新V2.2不以质量提醒阻断制作，仍保留稿件和配音的内容确认。

功能测试阶段可在该服务端配置中设置 `AVATAR_WORKFLOW_TEST_MODE=true`。遇到历史请求状态不明时，工作台显示“重新执行并继续”，Agent 也可响应用户的“继续”请求：有任务号则接续查询，无任务号则保留历史并用当前草稿重新执行。刷新页面和重启服务不会自动提交。相同恢复请求去重，文案仍需确认；配音连续完成所有段落后，统一选择先试听确认或直接继续制作。此开关默认关闭，本机测试部署已按用户要求开启；它不代表供应商免费，也不实现积分结算。

视频生成可切换 RunningHub、MiniMax H3 V2，或其他供应商的通用 HTTP 网关，详见后端 `docs/avatar-video-providers.md`。统一配置为 `VIDEO_PROVIDER`、`VIDEO_API_PROTOCOL`、`VIDEO_API_BASE_URL`、`VIDEO_API_KEY`、`VIDEO_MODEL`、`VIDEO_RESOLUTION`。各原生协议不同，不能仅替换 Key 假定接口兼容；切换厂商不会自动对旧订单重新付费提交。

诊断：`docker compose ps`、`docker compose logs --tail=100 api web`。禁止使用 `docker compose down -v` 更新服务，以免删除用户数据。WSL2 地址在重启后可能改变，本机优先使用 localhost。

## 动效风格卡片预览

首页的风格弹窗直接在各卡片内静音循环播放演示，支持暂停、搜索和确认选择。仅可见卡片加载媒体；滚出视区、关闭弹窗或切换到后台后释放视频资源。演示遵循系统减少动态效果设置。

后端镜像构建时，`avatar-runtime/catalog-previews.mjs` 为每种风格生成独立的 `catalog/<id>-card.mp4` 和 `-card.jpg`，原始演示与制作素材不变。卡片视频使用 H.264、最长边 480 像素、24 fps、CRF 28、最高码率 400k、无音轨及 faststart。源文件与压缩配置的摘要未变化时复用派生文件；新增风格后重新部署后端即可生成。卡片接口为 `/api/v1/avatar-mvp/catalog/:style/card-preview` 和 `/card-poster`，沿用登录鉴权；文件缺失返回 404，不回退下载原始大视频。

部署后可核对全部真实预览的尺寸、帧率、音轨和压缩体积，并验收桌面与手机播放：

```powershell
wsl.exe -d Ubuntu-24.04 --exec docker compose --project-directory /opt/aicat/avatar-mvp exec -T api node avatar-runtime/tests/verify-card-catalog.mjs
node deploy/wsl2/verify-motion-style.cjs
```

浏览器验收使用真实风格目录、媒体和项目保存接口，仅替代 Agent 聊天回复以避免触发制片；验收结束删除临时项目。报告和截图写入 `backend/data/wsl-deployment/motion-style-cards/`。

## 运行镜像空间维护

日常增量部署复用依赖文件校验一致的固定 `aicat-avatar-runtime-cache:local`，不再每次以上一次完整应用镜像为基础叠加历史层。复制源码时设置文件属主，避免递归修改已安装依赖的属主产生整套依赖的重复层。

`compact-runtime.py SOURCE_IMAGE DESTINATION_IMAGE` 用于维护历史膨胀镜像：从镜像当前文件系统建立紧凑副本，并核对环境变量、启动命令、用户、工作目录、端口和健康检查配置。它不读取运行容器的数据卷，不自动替换服务，也不删除原镜像。操作前保留原镜像标签，验证副本后通过部署保护器更新服务，再回收不再需要的构建层。源镜像标签在构建期间不能变化。

Windows D 盘实际回收以磁盘空闲字节为准。Docker 回收后，如 VHDX 仍占用空间，应在无活跃制作任务时正常停止服务，执行 Linux `sync` 和 `/usr/sbin/fstrim -v /`，终止 `Ubuntu-24.04` 后使用本机支持的 `wsl --manage Ubuntu-24.04 --compact`；完成后恢复原部署并验证健康状态。保留数据库、任务素材及模型缓存卷。
