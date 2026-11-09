# 当前模型默认音色修复

## 范围

当前模型保持 `minimax/speech-2.8-hd`，默认音色使用 `Wise_Woman`。不在本轮增加首页全量音色选择器，不自动恢复旧配音订单或发起生成。

2026-11-07 核对 RunningHub 的 `api-448183268` 文档，`voice_id` 的 schema `default` 为 `Wise_Woman`。文档地址：`https://rhtv.runninghub.cn/runninghub-api-doc-cn/api-448183268`。

## 改动

- 模型默认音色集中在 `backend/internal/avatar/speech_voice.go`；MiniMax、豆包与原有 Qwen 默认值按模型区分。
- 当前 MiniMax 配置中遗留的豆包 `*_bigtts` 音色回退到当前默认值；配置目录过滤这些不兼容音色，始终包含当前默认音色。
- 项目输入保存时迁移这类旧音色；用户设置的其他有效音色仍可保留，不把未列入目录的任意值静默当成默认值。
- 真正调用配音接口前再次检查模型和音色。冻结运行中的错误音色在本地被拒绝，不偷偷修改原运行的配音指纹或供应商订单。
- WSL 配置清空旧 `TTS_SPEAKER` 覆盖值，让服务按模型计算默认值。原配置备份只留在 WSL 的受限目录，其他配置和密钥不变。
- 部署初始化的 `TTS_SPEAKER` 默认值改为空，避免再次写入固定豆包音色。

## 测试

- 先写测试并复现默认音色混用，再实现修复。
- avatar 包 45 项通过；工作流、Agent package、节点与对抗回归 71 项通过，总计 116 项。
- 检查空音色默认值、旧配置迁移、自定义音色保留、目录过滤、实际 HTTP 请求的 `voice_id`、错误音色零提交、草稿更新不改历史订单、未知订单继续阻止重提。
- 两个原有隔离测试的 Qwen 测试音色与豆包测试模型不匹配；校正测试数据后，继续验证确认幂等与未知订单保护。
- 供应商接口测试使用本地 httptest；未用真实付费生成验证模型出音。

## 后续首页音色选择规划

1. 首页在风格选择旁增加音色选择，默认选中当前模型默认音色。
2. 服务端按模型维护或获取音色目录，前端显示名称、语言等可用信息，不混用不同供应商的音色 ID。
3. 用户选择保存到项目的 `input.voice`；Agent、分段配音和重做流程读取同一字段。
4. 试听按模型和音色缓存，明确区分已有试听与需要新生成的试听，不因展开选择器自动调用付费服务。
5. 已有项目更换音色，从 F04 重新生成并使依赖音频的下游产物失效，保留确认与订单核对保护。

## 部署实测

- 2026-11-07 18:31（北京时间）通过 `deploy/wsl2/deploy.ps1 -Service backend` 更新既有 WSL Ubuntu-24.04 Docker 后端，Web/API/PostgreSQL 均健康。数据库和素材保留。
- 四个关键实现文件与 WSL 构建源文件的 SHA-256 一致。
- 运行容器的模型仍是 `minimax/speech-2.8-hd`，旧音色覆盖值已清空；真实目录接口只返回 `Wise_Woman`，显示“默认音色 · Wise Woman”。
- 现有项目 `5daa1faa-a31d-428d-8eaa-521d0eca73d5` 的草稿音色通过正式配置接口改为 `Wise_Woman`，草稿版本变为 4；除音色和版本外，输入字段均未变化。原运行完整快照哈希保持一致，状态仍是 `submission_unknown`，未绕过核单保护。
- 创建一个不传音色的临时项目，确认服务端自动保存 `Wise_Woman` 且没有运行；验收后删除该临时项目。
- 真实浏览器验证本机入口和 WSL IP：登录、首页、刷新、项目列表、工作台、聊天历史和旧入口重定向均通过，页面错误为 0，生成任务调用为 0。
- 实测地址：`http://localhost:8080/`、`http://172.17.234.187:8080/`。

证据：本目录 `default-voice-avatar-tests.json`、`default-voice-server-tests.json`、`default-voice-deployment.json`；浏览器报告在 `backend/data/wsl-deployment/current/localhost/verification.json` 与 `backend/data/wsl-deployment/current/172.17.234.187/verification.json`。
