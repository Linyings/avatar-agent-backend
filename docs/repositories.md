# 前后端独立仓库

2026-11-05 起，项目使用两个独立 Git 仓库。拆分前根仓库尚无提交，也没有远程地址，因此没有旧提交历史需要重写；首次提交保存各自现有源码快照。

| 仓库 | 当前本地根目录 | 跟踪内容 |
| --- | --- | --- |
| avatar-agent-backend | `D:\pro\ai_cat\avatar-agent` | `backend/`、`deploy/`、`docs/` 及根说明 |
| avatar-agent-frontend | `D:\pro\ai_cat\avatar-agent\frontend` | React/Mantine 前端、锁文件、前端 Dockerfile、测试及文档 |

保留后端源码子目录和现有部署入口以兼容当前路径。前端目录虽然默认放在后端工作区内，但有独立 `.git`，被后端根 `.gitignore` 的 `/frontend/` 排除；后端没有前端源码或 gitlink，不是 submodule，也不要求同步提交版本。

## 日常 Git 操作

```powershell
# 后端仓库
git -C D:\pro\ai_cat\avatar-agent status
git -C D:\pro\ai_cat\avatar-agent add backend deploy docs
git -C D:\pro\ai_cat\avatar-agent commit -m "说明后端改动"

# 前端仓库
git -C D:\pro\ai_cat\avatar-agent\frontend status
git -C D:\pro\ai_cat\avatar-agent\frontend add src
git -C D:\pro\ai_cat\avatar-agent\frontend commit -m "说明前端改动"
```

各自设置 `origin` 后分别 `git push -u origin main`；标签也分别创建和推送。拆分操作不自动连接参考仓库、父目录的无关仓库或未知远程地址。实际远程地址由项目所有者指定。

## 在其他机器独立克隆

将下面占位地址替换为实际的两个不同仓库地址：

```powershell
git clone <后端仓库地址> avatar-agent-backend
git clone <前端仓库地址> avatar-agent-frontend
cd avatar-agent-backend
.\deploy\wsl2\deploy.ps1 -FrontendPath ..\avatar-agent-frontend
```

也可以把前端克隆为后端工作区的 `frontend` 子目录，此时不需要 `-FrontendPath`。后端构建上下文为 `backend/`；前端构建上下文为前端仓库根。锁文件、原始媒体素材及许可证留在各自仓库，编译产物、依赖目录、真实任务验收素材、密钥与数据库不提交。后端 `avatar-runtime/catalog` 只跟踪原始 `*-reference.mp4/jpg`，其他预览与下载包由 Docker 构建生成。

## 独立更新现有 WSL 服务

```powershell
.\deploy\wsl2\deploy.ps1 -Service backend
.\deploy\wsl2\deploy.ps1 -Service frontend -FrontendPath ..\avatar-agent-frontend
```

仍使用 `Ubuntu-24.04`、`/opt/aicat/avatar-mvp` 和 `aicat-avatar` Compose 项目。仅前端更新不会重建或重启 API，仅后端更新不会重建或重启 Web；更新前备份现有数据库与服务端配置。完整部署默认 `-Service all`，成功后分别记录两端 commit 到 WSL `DEPLOYED_REVISIONS.json`。数据库、任务数据与模型缓存持续使用原卷。

前端保留的上游云发布 workflow 已改为手动触发，本项目默认仍在 WSL 部署；它不会因为推送独立仓库而自动发布到原项目服务器。

## 来源与本地备份

根 `docs/upstream-projects.json` 和前端 `UPSTREAM.json` 保留参考版本，源码许可保留原位置。原始 Git 元数据备份在忽略目录 `.local/repository-split/original-git`。首次快照使用 `Avatar Agent <avatar-agent@localhost>` 标注迁移提交；后续日常提交可在各仓库设置自己的 `user.name` 与 `user.email`。
