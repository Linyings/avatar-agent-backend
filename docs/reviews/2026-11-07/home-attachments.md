# 首页多素材添加框验收（2026-11-07）

首页输入区按用户参考图改为左侧虚线素材添加框、右侧文字输入。点击添加框或底部加号可多选人物图片、参考视频，或按行添加多个网页链接；支持拖放文件、缩略图、移除、重复选择去重及主人物图片选择。

图片格式与服务端解码规则一致：PNG/JPG，单张不超过 8 MiB，宽高为 64–8192 像素。视频上传上限约 256 MiB，最终以服务端解码结果为准。图片像素尺寸由服务端校验。

网页链接最多 20 条，限制为 HTTP/HTTPS 且不含账号密码。链接通过 `reference_links` 保存在项目元数据中，Agent 可通过 `avatar_inspect` 查看，工作台“素材与产物”页可打开。当前没有网页正文抓取功能，Agent 提示词明确不能声称已读取正文。

上传失败后保留同一草稿和已成功上传的素材；可移除失败文件、添加替换文件并重试，不重新创建项目或重传成功文件。

## 验证结果

- 前端相关测试原有 31 项通过；补充格式/大小边界后重跑首页 6 项全部通过（本轮相关用例合计 32 项）。TypeScript 与生产构建通过。
- 后端 Agent/package/workflow 相关 129 项通过，结果见 `node-adversarial-home-references.json`。
- `verify-home-attachments.cjs` 在 WSL 实际服务完成多图片、多视频选择、重复选择、移除后重新选择、主人物选择、多链接去重、非法协议拒绝、390px 手机布局验收。
- 真实上传顺序为图片、主人物图片、视频、损坏视频；前三个成功，损坏视频返回 400。移除损坏视频并替换后成功进入工作台。共创建 1 个临时项目、请求上传 5 次，其中 4 次成功；没有重传成功文件。
- 对全部 4 个成功上传素材进行鉴权下载，逐字节与原文件比较一致。刷新工作台后素材和 2 条参考链接仍存在，图片预览正常。
- 浏览器无页面异常。临时项目已删除。
- 仅拦截 `/api/v1/agent/chat` 返回验收回复，以避免付费生成；创建、上传、读取、下载、删除均使用真实 WSL API。本次未验证模型阅读素材或视频生成质量。
- `verify-current-browser.cjs` 在 localhost 上通过登录、首页、刷新、旧入口重定向、项目列表和现有工作台只读检查，没有启动模型任务。

## 部署

使用 `deploy/wsl2/deploy.ps1 -Service all` 更新现有 `Ubuntu-24.04` 的 `/opt/aicat/avatar-mvp`，前端、Go API、数据库均健康。保留现有配置、账号、数据库和素材；部署脚本已备份数据库与配置。

实测地址：`http://localhost:8080/`、`http://172.17.234.187:8080/`。

验收脚本：`deploy/wsl2/verify-home-attachments.cjs`。

截图及机器报告：`backend/data/wsl-deployment/home-attachments/` 下的 `home-empty-desktop.png`、`home-selected-desktop.png`、`home-selected-mobile.png`、`upload-failure-recovery.png`、`workspace-persisted-assets.png` 和 `verification.json`。
