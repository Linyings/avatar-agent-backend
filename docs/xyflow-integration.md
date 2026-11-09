# React Flow 工作流画布接入

日期：2026-11-07。

## 来源与依赖

- 用户指定的仓库已完整克隆到 `D:/pro/ai_cat/参考/xyflow`，origin 为 `https://github.com/xyflow/xyflow.git`。
- 参考提交：`1bc405cd113618c17fd181b931a9fe573bec9fee`，2026-11-02；参考源码未修改。
- 正式使用 `@xyflow/react` **12.12.1**、其依赖 `@xyflow/system` **0.0.84**，与参考仓库版本对应，MIT。
- 自动布局使用 `elkjs` **0.12.1**，选择 EPL-2.0 授权；来源 tag `0.12.1`，提交 `624ae90aa37110033a8af59c5ccbb292708b4317`。未修改其源码。
- 依赖与完整性摘要固定在前端 package-lock.json。许可证及来源随网站发布到 `/licenses/`，本地记录见 `frontend/THIRD_PARTY_NOTICES.md`。

## 实现

- 工作台 `AI 画布流` 使用 React Flow 的实际节点、端口和边，移除原三列 CSS 卡片和装饰连线。
- 13 个制作节点及分支、汇合连线来自服务端 package 的 `node` / `dependencies`，插件执行仍走原有后端。画布拖动仅调整展示位置，不改变执行依赖。
- 自定义节点沿用 Mantine，支持完整文案滚动、图片、视频、音频播放器、段落和分镜列表、按实际完成步骤计数的进度。当前运行、失败、待审核、订单待核对等状态映射到节点。
- 支持节点标题拖动、空白平移、滚轮与按钮缩放、Shift 多选、小地图、定位步骤、查看全图、ELK 自动排列及展开大画布（按钮或 Escape 退出）。
- 详情作为画布内侧栏打开，右侧 Agent 对话继续保留；现有设置、审核、执行和修改入口保留。
- 节点位置和视角按账号、项目隔离保存到**当前浏览器** localStorage。后台轮询不会重排节点，刷新和切换标签页可恢复；该布局目前不跨设备同步。仅保存坐标和视角，不保存文案、媒体或认证令牌。
- 媒体复用已有鉴权请求与 blob URL 回收逻辑；只有节点处于可见范围且缩放达到可读大小后才加载，避免全图预览时批量下载所有媒体。
- ELK 动态加载为独立资源，不在首页加载。构建对它约 1.47 MB 的压缩前资源给出大小提示；gzip 约 452 KB。

## 代码入口

- `frontend/src/features/agent/WorkflowCanvasView.tsx`：React Flow、自定义节点、交互和布局保存。
- `frontend/src/features/agent/workflowCanvas.ts`：依赖图、ELK 排列、内容及进度映射、存储校验。
- `frontend/src/features/agent/WorkflowCanvas.module.css`：画布和节点样式。
- `frontend/src/features/agent/StudioWorkspace.tsx`：工作台集成及详情侧栏。
- `frontend/src/features/agent/workflowCanvas.test.ts`：分支/汇合、自动布局、媒体 URL、状态与进度、存储隔离回归。
- `deploy/wsl2/verify-workflow-canvas.cjs`：实际部署浏览器验收，只删除脚本新建项目，已有项目只读。

## 验证

前端相关 **23/23** 项测试通过，新增文件 ESLint、TypeScript 和生产构建通过。2026-11-07 **14:01:51（Asia/Shanghai）** 已在原 WSL 部署目录构建并更新前端，未重启 API 或数据库，三个容器均 healthy。

最终浏览器验收于 **14:02:32** 完成，15 项检查全部通过：真实依赖连线、上传图片预览、节点拖动、后台更新内容后保留布局、刷新恢复、缩放、平移、文案内部滚动不触发缩放、节点详情侧栏、自动排列、大画布展开与 Escape 退出、项目隔离、手机布局及标题无遮挡、历史项目真实音频点击播放、后端实际进度映射。无浏览器异常或 React Flow 警告，未提交制作或模型任务。

新版首页、登录、旧路由重定向、项目库与工作台回归通过；改名后保存设置、上传失败替换文件回归也通过。临时验收项目均已删除，历史项目与素材仅用于读取和播放。

实测入口：`http://172.17.234.187:8080/` 和 `http://localhost:8080/`。两份开源许可证 URL 也已验证返回 HTTP 200。

证据：`backend/data/wsl-deployment/workflow-canvas/verification.json`、`unit-results.json`；同目录 `input-node.png`、`workflow-overview.png`、`audio-node.png`、`mobile.png` 保存真实界面截图。修复前失败的浏览器尝试曾发现 Escape 退出被画布键盘事件拦截，现已改为捕获阶段处理并回归通过。

执行：

```powershell
npm.cmd test -- --run src/features/agent/workflowCanvas.test.ts src/features/agent/StudioHome.test.tsx src/features/agent/studioService.test.ts src/features/agent/agentStream.test.ts
node deploy/wsl2/verify-workflow-canvas.cjs
```

第一条在 frontend 目录执行，第二条在 avatar-agent 目录执行。
