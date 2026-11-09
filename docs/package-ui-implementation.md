# 原型 UI 与 Agent package 实现

参考：`参考/stitch_flova_ai_saas_clone/` 中的首页、项目管理页和项目设计页。保留其深色分栏、渐变输入框、项目卡片、画布与右侧对话布局；展示真实项目和产物，不使用原型的示例进度、算力及协同信息。

实现范围：独立版本化 package 清单和输入校验；F01–F12 按目标节点执行；文案、配音、素材、分镜修改提案持久化及用户确认；暂停、继续、停止；模型工具调用协议修复；三个页面接入真实 API、上传、状态、产物与错误反馈。

部署：Ubuntu-24.04，`/opt/aicat/avatar-mvp`，保留全部持久卷。

## Package 结构与执行

`backend/internal/agentpackages/packages/<name>/package.json` 为独立包清单，包含 ID、版本、工具名、节点、依赖、输出、底层工具和 JSON Schema。清单随 Go 构建嵌入服务，浏览器与 Agent 使用同一个注册目录。新增包必须同时提供受审查的执行入口，不允许模型传入可执行命令或文件路径。

共 19 个包：需求配置、视频理解、文案、内容拆解、配音、素材需求、数字人、动效、实拍素材、素材接入、分镜、预览、交付，以及项目检查、运行控制和四类编辑提案。运行包复用现有供应商调用、缓存与订单恢复逻辑，按目标节点推进前置依赖，不越过目标节点或用户审核门。

- `GET /api/v1/agent/packages`：清单和服务配置状态。ready 表示配置检查通过，不表示完成供应商在线验收。
- `GET /api/v1/agent/project/:projectId`：最新草稿、实际运行、产物和持久化修改提案。
- `POST /api/v1/agent/packages/:packageId/invoke`：按项目归属、schema 和前置条件调用包。
- `POST /api/v1/agent/confirm`：核对 proposalId 与 revision 后创建幂等修改运行。重复点击不重复创建。

编辑工具接收精确的字段修改，不把“修改意图”误当作已修改数据。用户确认前保留原产物，确认后在新运行中复用未变化的音频与素材。暂停在安全的步骤边界生效，已提交供应商订单不会被丢弃；恢复沿用当前运行。

应用音色修改时，草稿音色和新运行版本在同一事务中更新；确认响应中断后重试仍复用同一运行。前端按全部段落或素材的完成状态汇总节点，避免第一段完成后就将整个节点显示为完成。

兼容编辑运行中被清空的下游列表，避免加载时因 `null` 列表报错；可选步骤没有输入或需求时显示“无需执行”。实时连接支持 SSE 的具名事件及重连；同时修复原自动翻译模块将 React 动态文字还原为初始值的问题，项目数量、连接状态和工具完成状态可正常更新。

## 页面

- 首页：真实项目创建、上传照片/视频、制作类型与画幅、插件目录、最近项目。
- 项目管理：搜索、状态筛选、排序、网格/列表、重命名、归档、批量归档、删除。
- 项目设计：制作节点画布及缩放、节点检查器、分镜、时间轴、素材上传、鉴权媒体预览、成片下载、制作设置、Agent 对话和可确认提案。

原型中的虚构算力、云端空间、协作人数、品牌案例与未实现的运镜/局部重绘操作未作为可用功能展示。

## 验证入口

- 后端：`go test ./internal/agentpackages ./internal/agent ./internal/avatar ./internal/config ./internal/server`；数据库测试需要 CGO，可在 WSL Go 检查镜像中运行。
- 前端：`npm run build`，`npm test -- --run src/features/agent/studioService.test.ts src/features/agent/agentStream.test.ts`。
- 真实浏览器：`node deploy/wsl2/verify-studio-packages.cjs`。创建专用验收项目，调用真实 Agent 的 inspect/configure 工具，验证上传、设置持久化、项目搜索、各工作台视图和移动布局；随后提供固定稿件，从 UI 调用 F02 并确认在 F03 前暂停，检查重命名和归档。结束后删除自身创建的项目。该脚本不提交付费视频/配音制作。
- 只读部署检查：`node deploy/wsl2/verify-current-browser.cjs`。

MiniMax 2.8 HD 的 RunningHub 路由及参数按供应商文档接入：`https://rhtv.runninghub.cn/runninghub-api-doc-cn/api-448183268`；使用 `voice_id`，保留已有服务器密钥。

## 本轮验收结果（2026-11-07，上海时间）

- WSL2 前端、API、PostgreSQL 均健康；已核对运行中的后端镜像与本次构建一致。
- 实测入口：`http://localhost:8080/`、`http://172.17.234.187:8080/`。
- 前端构建成功；Agent API、SSE 和动态文字翻译相关 30 项测试通过。WSL 中 package、工具协议、工作流与配置相关后端测试通过，另覆盖音色版本持久化及确认重试。
- 真实浏览器已验证三个页面、登录/刷新、历史成片项目、19 个 package 的目录、真实模型调用 inspect/configure 的完成状态、设置保存、图片上传及鉴权预览、各工作台视图、项目搜索/视图切换/重命名/归档、移动首页与旧入口跳转。
- 从页面调用固定稿件的 F02 package，运行在 F03 前暂停，目标边界正确；验收项目已经清理。页面脚本错误为 0。
- 证据：`backend/data/wsl-deployment/studio-packages/localhost/verification.json` 及同目录截图；IP 下的只读报告位于 `backend/data/wsl-deployment/current/172.17.234.187/`。
- 本轮未重新提交付费配音或数字人视频全链路任务，供应商在线产片仍需以实际业务任务为准。
