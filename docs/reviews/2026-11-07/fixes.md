# 对抗审查问题修复记录

2026-11-07。原始发现与失败证据保留在同目录 README 和 `backend-results.json` / `frontend-results.json`。

## 实现

| 问题 | 修改 | 回归覆盖 |
| --- | --- | --- |
| R1 未知订单绕过 | 在共用运行创建事务中扫描本项目所有运行的待核对订单，草稿版本变化、停止状态及较新的运行均不能掩盖它；相同请求仍返回原运行 | 空配置更新后不能重交；interrupted/stopped 同样受保护；历史已知订单受保护；终态失败后可建新运行；幂等重放保持原 ID |
| R2 停止后执行工具 | 每轮 context 派生自 HTTP 请求，停止直接取消模型/工具 context；模型返回后、每个工具前后以及 package 入口检查取消 | 模型返回前停止、批次中途停止、阻塞模型取消、取消请求不保存草稿 |
| R3 并发聊天串轮次 | 同一 Session 整轮互斥；第二个不同请求明确返回 conflict；会话清理跳过仍有活动轮次的项目 | 两个请求可控交错，工具仍使用 A 的轮次；活动会话跨 TTL 不被替换；Agent 全套 race 检查 |
| R4 冷启动历史为空 | 历史接口先读持久层，不依赖内存会话存在；冷启动读取失败返回可重试错误，避免伪装成空记录 | 无内存 Session 的 history 接口仍返回已存消息；部署前写入验收消息、部署后首个 history 请求读取 |
| R5 改稿要求视频条件 | 文案编辑推进到 F03 审核，配音编辑推进到 F04 审核；目标节点与新运行在同一事务提交；用户选择完整继续时再检查全流程前置条件 | 没有照片/TTS 时仍能改稿并审核；批准后在下个节点前暂停；缺少后续配置时完整继续被拦；幂等重放不能扩大执行范围 |
| R6 名称回退 | 通用项目改名与草稿标题、revision 在同一事务内更新，和配置保存共用锁；未改名的通用更新保留最新名称 | 改名后保存画幅保持新名称；旧版本设置不能覆盖改名；浏览器项目库改名后再保存设置 |
| R7 上传失败锁死 | 附件按稳定 ID 标记上传结果；失败项可删除/替换；成功项保留且不会重传；保留打开已建项目入口 | 真实组件测试替换失败项、保持原项目及成功附件；部署后真实坏图拒绝、替换 PNG、预览素材 |

配套修复：确认、重试和恢复时的占用检查改用当前事务连接，避免事务内再申请独立连接而卡住。取消请求也传入这些状态更新事务。

浏览器验收额外发现并修复：统一请求封装原先优先展示 `error: "Bad Request"`，遮住服务端 `message` 中的具体中文原因。现在优先读取有效的业务消息，兼容嵌套错误；空响应和非文本字段回退到带状态码的提示。新增 4 个回归用例，修复前其中 3 个失败，修复后全部通过。

## 验证记录

- Windows 前端 `npm run build` 通过，使用现有依赖与锁文件。
- 前端相关测试 36/36 通过，包括 2 项附件恢复测试和 4 项错误响应解析测试；结果：`fix-frontend-results.json`。
- WSL Docker 后端相关测试 64 项通过（包含子用例）；结果：`fix-backend-results.json`。
- Agent 全套 `go test -race` 40 项通过；结果：`fix-agent-race-results.json`。
- 所有供应商提交复现使用本地模拟服务，不调用真实收费供应商。

正式测试位于 `backend/internal/agent/adversarial_regression_test.go`、`backend/internal/server/agent_package_regression_test.go` 和前端 `src/features/agent/StudioHome.test.tsx`。同目录 runner 可重跑。

## WSL 部署与浏览器验收

- 环境：`Ubuntu-24.04`，原部署目录 `/opt/aicat/avatar-mvp`。前后端均已更新，PostgreSQL、已有素材和缓存保留。
- 后端部署完成时间：2026-11-07 12:07:29（Asia/Shanghai）；追加错误提示修复后，前端部署完成时间：12:11:30。部署脚本返回 0，三个容器均 healthy。
- 更新前生成数据库与配置备份；最近一次备份时间戳为 `20261107T041043Z`，位于 `/opt/aicat/avatar-mvp/backups/`。
- IP 入口 `http://172.17.234.187:8080/` 与本机入口 `http://localhost:8080/` 均经真实 Chrome 浏览器验证：首页、登录、刷新、旧链接跳转、项目库、历史项目工作台、Agent 历史均通过，无浏览器运行错误。
- 冷启动历史：后端部署前创建验收消息，重建 API 容器后确认启动时间晚于消息写入时间，首次 history 请求仍读到该消息；随后浏览器工作台也显示该消息。
- 项目改名：项目库重命名后进入制作设置，修改要求并保存；项目名称和草稿标题均保持新名称。
- 附件恢复：真实无法解码的 PNG 被服务端拒绝并显示具体中文错误；移除后上传有效 PNG，进入原项目，素材列表仅有替换文件，图片实际加载成功，未创建制作运行。
- 验收仅清理脚本新建的临时项目，清理接口均返回 200。未调用真实收费模型、配音或视频生成服务，收费制作全链路不在本轮验收范围。

浏览器证据保存在：

- `backend/data/wsl-deployment/adversarial-fixes/verification.json`：首次部署后的冷启动历史、改名验证通过；该轮随后发现上传错误提示问题，未完成附件恢复验证，因此不作为整轮通过记录。
- `backend/data/wsl-deployment/adversarial-fixes/verification-ui.json`：错误提示修复后的 UI 验收，`passed: true`，包含同项目附件替换和清理结果。
- 同目录截图：`rename-settings.png`、`upload-validation.png`、`upload-recovered.png`。
- `backend/data/wsl-deployment/current/172.17.234.187/verification.json` 和 `current/localhost/verification.json`：两种入口的完整页面回归。

重新验证当前 UI 可运行 `node deploy/wsl2/verify-adversarial-fixes.cjs verify-ui`。冷启动历史验证需先运行 `prepare`，正常部署后再运行 `verify`；不能把未重启 API 的 UI 验证称为冷启动验证。
