# 第七步：逐视频制作节点

新版 11 步工作流在素材规划完成后，按每个 `MaterialNeed` 展开人物视频、专用动效和实拍素材节点。节点使用稳定 ID，显示关联台词、实测时长、实际任务状态、制作阶段、失败原因及视频预览。动效节点连接同段人物节点，所有素材节点连接「素材汇总与镜头组装」，再进入镜头审阅。

每个人物或动效节点有独立提示词草稿，可手动保存或让 AI 按修改要求重写。草稿保存在项目数据库元数据中，带基础运行、素材摘要和版本，保存不会提交视频生成。新生成请求会记录完整提交提示词；旧视频没有完整记录时明确说明，仅显示已有制作提示词。实拍节点显示实际素材来源与区间。

「重新生成此视频」创建独立运行，仅重做选中素材，保留已确认配音和独立视频，完成后暂停在汇总前。人物重做会使同段依赖人物画面的动效变为待制作，其他段不受影响。汇总按钮补齐待制作素材，校验每个视频成功且文件可用后统一组装。既有汇总完成的旧任务可继续使用历史结果。

接口为 `POST /api/v1/agent/project/:projectId/materials/:needId`，支持 `save`、`improve`、`generate`。操作验证项目归属、基础运行、草稿版本、配音确认及未完成订单；生成请求使用幂等编号防止重复提交。运行中的视频允许编辑草稿，需等当前任务结束或暂停后才能重新生成。

验证入口：

- 前端：`npx vitest run src/features/agent`、`npx tsc -b`。
- 后端：`go test ./internal/server ./internal/avatar -run 'Test(MaterialNode|AvatarWorkflow|Package|Node|Director|Motion|ContentMotion)'`，SQLite 测试需启用 CGO。
- 部署：`deploy/wsl2/deploy.ps1 -Service all`，沿用 Ubuntu-24.04 的 `/opt/aicat/avatar-mvp`。
- 浏览器：`node deploy/wsl2/verify-current-browser.cjs`、`node deploy/wsl2/verify-material-nodes.cjs`。

浏览器验收读取真实项目及视频，并以原值保存现有提示词来验证持久化，不启动视频任务。单节点生成范围、失败重试、合并门槛和重复请求由隔离后端测试验证。
