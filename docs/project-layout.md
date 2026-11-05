# 独立项目整理

日期：2026-11-03。

2026-11-05 更新：前后端已改为两个独立仓库。`avatar-agent/` 的 Git 只管理 backend、deploy、docs，`frontend/` 有自己的 `.git` 并被后端忽略。下文“没有嵌套 Git”的说明为整理初期历史状态；当前约定见 [双仓库使用说明](repositories.md)。

2026-11-04 更新：10 个参考仓库已统一移动到 `D:\pro\ai_cat\参考`，完整索引见 [参考/README.md](../../参考/README.md)。用户应用仍位于 `D:\pro\ai_cat\avatar-agent`；下文的迁移前路径属于历史记录。

用户要求当前工作区的既有仓库仅用于参考，应用代码应独立成项目。因此将此前已实现的 MVP 和复用基础源码迁移到 `D:\pro\ai_cat\avatar-agent`，使用独立 Git 仓库，不继承参考仓库的 `.git` 或远程地址。

| 原位置 | 本项目位置 |
|---|---|
| NarratoAI-SaaS-B-Go 中的基础及 MVP 代码 | backend/ |
| NarratoAI-SaaS-F 中的基础及 MVP 代码 | frontend/ |
| 根 deploy/wsl2 | deploy/wsl2/ |
| 后端 avatar-runtime | backend/avatar-runtime/ |

迁移先逐文件复制并核对 SHA256，再检查本次新增与修改文件，恢复参考仓库中的本项目改动。原始改动补丁、迁移清单与本机测试产物保存在忽略目录 `.local/migration-backup`，不进入版本控制。

上游仓库中的历史数据库备份和财务 CSV 导出不作为新应用源码或初始数据；迁移副本隔离在 `.local/migration-backup/upstream-data`，不参与 Git 或 Docker 构建。应用继续使用用户当前本机 WSL 数据库。

WSL 保留 `/opt/aicat/avatar-mvp` 服务运行目录和 `aicat-avatar` Compose 名称，避免更换数据库和素材卷。新构建上下文为 `src/backend`、`src/frontend`；服务端 Key 与登录密码不复制到本项目。供应商、TTS、LLM 配置方式及接口地址不变。

后续从本项目开发和部署，禁止对上级参考仓库直接实现功能。参考仓库只用于阅读、比较与明确执行的源码引入；引入时保留来源与许可。

## 完成验证

- 本项目根目录已初始化独立 Git 仓库（main），backend/frontend 不含嵌套 `.git`，未配置远程仓库。
- 两个 NarratoAI 参考仓库以及 video-talkcraft 的 Git 状态均干净；本次 MVP 改动只在新项目中维护。
- WSL Compose 构建上下文实测为 `/opt/aicat/avatar-mvp/src/backend` 和 `src/frontend`。旧的 WSL 参考源码副本已移至服务运行目录 `backups/legacy-reference-source`，不再参与构建。`SOURCE_PROJECT.txt` 记录当前部署来源。
- 前后端运行容器具有 `com.aicat.project=avatar-agent` 标签，三个服务健康。现有账号、项目及工作流记录数与迁移前一致，数据库备份留在 WSL 的 `backups/before-project-layout.dump`。
- 新目录下前端与登录相关 11 个测试、ESLint、渲染工程 TypeScript 检查及 4 个 Node 测试通过。新 WSL backend 目录的 Go MVP 定向测试通过，Docker 内前后端构建通过。
- Chrome 使用真实账号访问 WSL HTTP IP，模型展示、登录、刷新和手机布局检查通过，没有页面脚本错误。容器从新项目脚本目录完成实际 Remotion 几何素材合成；不是人物模型验收样片。
- Windows 实测 `http://172.17.234.187:8080/avatar-mvp`，并保留 localhost:8080 和本机 API 18000 端口。模型密钥仍只保存在 WSL 服务端配置中。

