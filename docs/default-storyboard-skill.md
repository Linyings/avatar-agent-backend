# 默认分镜技能

项目内置 `talking-head-storyboard`（口播与图解视频分镜设计），来自本机已整理的两条参考视频方法。项目副本位于 `backend/internal/agent/skills/talking-head-storyboard/`，随 Go API 编译嵌入，不依赖个人技能目录或容器挂载。

默认启用代表 Agent 可以发现并自主选用。每轮正常工具对话仅加载名称、适用范围和选择规则；AI 判断相关时调用只读 `read_skill` 读取正文，再按需读取单个参考文件。普通咨询、状态查询等不强制调用，不用关键词规则自动触发制作。

技能目录同时通过 `avatar_inspect` 与工作台项目接口的 `skills` 字段返回。读取工具沿用现有工具事件，因此可以看到调用过程；它本身不消耗素材生成额度、不修改项目、不启动工作流。LLM 对话仍按原模型服务处理。

真正采用方法制作时，Agent 根据项目副本中的 `references/project-integration.md`，将选定设计要求合并进制作需求，或通过已有导演稿／素材／镜头编辑工具提交精确修改。只读取技能不会自动改写已生成的视频。

维护入口：

- `backend/internal/agent/skills.go`：默认目录、简短发现提示、只读资源工具。
- `backend/internal/agent/skills/talking-head-storyboard/SKILL.md`：主方法。
- `backend/internal/agent/skills/talking-head-storyboard/references/`：场景方法、交付格式、来源及项目接入。
- `backend/internal/agent/skills_test.go`：选择／不选择、正文与参考回灌、路径约束。
- `backend/internal/server/agent_skills_test.go`：项目接口默认技能目录。

修改项目副本后按现有 WSL2 部署脚本更新后端。个人目录里的同名技能与项目副本独立维护；修改个人副本不会静默影响已部署服务。

接入实测发现，原 Agent 请求未设置输出预算，当前兼容接口在 1024 token 时以 `finish_reason=length` 截断，并返回空正文。Agent 现显式使用现有内容预算（通常 6000 token，并受服务端 `AVATAR_MODEL_MAX_OUTPUT_TOKENS` 上限约束）；截断和无文字／无工具的空响应明确报错，不能当作成功。有效的纯工具响应仍正常执行。

`deploy/wsl2/verify-default-skill.cjs` 在临时项目中发起一次真实模型的只读分镜咨询，不指定技能名称，核对模型主动读取技能、输出文字以及项目未修改、未启动制作，最后清理临时项目。`TestLiveDefaultSkillDiagnostics` 仅在显式设置 `AVATAR_VERIFY_LIVE_AGENT_SKILL=1` 时调用真实模型，记录结束原因及长度，不记录密钥或推理正文。

2026-11-09 已在现有 `Ubuntu-24.04` Docker 部署验证：真实模型自主完成两次 `read_skill`，返回 1432 字符的建议，项目输入未改变、未创建制作运行。技能选择／读取、输出预算及截断处理的回归检查通过；浏览器首页、登录、刷新和工作台检查通过。实测入口为 `http://localhost:8080/` 与 `http://172.17.234.187:8080/`。现场结果在 `backend/data/wsl-deployment/default-skill/result.json`，浏览器结果在 `backend/data/wsl-deployment/current/172.17.234.187/`。
