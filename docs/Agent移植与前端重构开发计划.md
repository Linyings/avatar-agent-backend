# Agent 移植与前端重构开发计划

需求基线：PRD V1.12（第 16 章 Agent 能力规划、第 17 章前端 UI 全面重构）。
参考项目：`D:\pro\ai_cat\参考\NarratoAI-PC\`（电影模式 AI 剪辑助手 Agent）。
对应项目：`D:\pro\ai_cat\avatar-agent`。

## 总体目标

将 NarratoAI-PC 电影模式中的 AI 剪辑助手 Agent 框架移植到本项目，并重构前端 UI，实现：

1. **Agent 前期沟通**：任务开始前，Agent 与用户对话确认文案方向、实现方式、素材裁剪需求、最终效果预期，确认后才启动 Workflow 制作。
2. **Agent 节点介入**：Workflow F01–F12 执行过程中，Agent 在每个关键节点自动审核，发现问题暂停并让用户选择修改方案。
3. **前端 UI 全面重构**：放弃当前表单驱动页面，使用已克隆的 FlovaHomePage 深色设计系统重新构建全平台 UI，包含工作台三区布局、定价/订阅/后台管理等 SaaS 页面。

## 核心流程变化

```
旧流程：用户填表单 → Workflow 自动执行 → 交付
新流程：用户对话 → Agent 沟通确认方案 → 用户确认 → Agent 驱动 Workflow 执行（各节点审核介入）→ 交付
```

前期沟通四个确认项：

| 确认项 | Agent 职责 | 对应 NarratoAI-PC 机制 |
| --- | --- | --- |
| 1. 写什么文案 | Agent 根据用户主题生成文案方向提案（风格/结构/受众/时长），用户确认 | `story` step + `writing_preferences` gate |
| 2. 怎么实现 | Agent 规划制作路径（数字人/动画/真实素材组合方案），用户确认 | `movie_plan` 工具 + `prepareCast` |
| 3. 裁剪哪些素材 | Agent 列出素材需求清单（类型/时长/内容要求），用户确认 | `prepareMovieCompositionDialogue` |
| 4. 最终效果 | Agent 描述预期成片效果（节奏/画面风格/时长/比例），用户确认 | `voice_review` gate 整体确认 |

## 阶段划分

| 阶段 | 开发内容 | 交付物 | 初估 |
| --- | --- | --- | --- |
| Phase 0 | 设计系统提取 + 共享组件库 | FlovaHomePage 视觉组件抽离为可复用库 | 3–4 人日 |
| Phase 1 | Agent 后端框架移植 | AgentSession + 工具注册 + 提案/审核/确认机制 | 6–8 人日 |
| Phase 2 | 前期沟通流程 | Agent 对话式需求确认（4 项确认），确认后生成 F01 草稿 | 4–5 人日 |
| Phase 3 | 工作台 UI + Agent 对话前端 | 三区布局 + 对话窗口 + 提案卡片 + 产物卡片 | 5–7 人日 |
| Phase 4 | 节点审核介入 | F02/F05/F10 三个关键节点 Agent 自动审核 + 确认卡 | 4–6 人日 |
| Phase 5 | SaaS 商业化页面 | 定价/订阅/充值/后台管理/个人设置全套页面 | 5–7 人日 |
| Phase 6 | 联调验收 | 全流程联调 + 响应式 + 数据一致性 + 部署 | 4–5 人日 |

合计 31–42 人日。Phase 0–2 优先交付（打通 Agent 前期沟通），Phase 3–4 跟进（工作台 + 节点介入），Phase 5–6 补齐 SaaS 完整度。

---

## Phase 0：设计系统提取与共享组件库（3–4 人日）

### 目标

将已克隆的 `FlovaHomePage.tsx`（885 行）中的视觉元素提取为可复用的设计系统组件库，供主页、工作台、商业化页面统一使用。

### 任务清单

**0.1 设计 Token 系统**

从 FlovaHomePage 提取并规范化为 CSS 变量 + Mantine theme override：

```
--color-bg: #080808
--color-surface: #111111
--color-surface-hover: #1a1a1a
--color-surface-raised: #181818
--color-border: rgba(255,255,255,0.07)
--color-border-hover: rgba(255,255,255,0.14)
--color-text: rgba(255,255,255,0.92)
--color-muted: rgba(255,255,255,0.42)
--color-dim: rgba(255,255,255,0.22)
--color-accent: #e8820a
--color-accent-light: #ff9d2e
--color-accent-bg: rgba(232,130,10,0.12)
--color-accent-border: rgba(232,130,10,0.35)
--color-success: #22c55e
--color-warning: #f59e0b
--color-danger: #ef4444
--radius-sm: 4px / --radius-md: 8px / --radius-lg: 12px / --radius-pill: 999px
```

字体：Cormorant Infant（艺术标题）/ Inter（正文）/ JetBrains Mono（代码）。
动效：120–200ms ease 过渡；橙色脉冲动画用于进行中状态。

交付物：`frontend/src/theme/avatarTheme.ts`（Mantine theme override）+ `frontend/src/styles/tokens.css`（CSS 变量定义）。

**0.2 共享组件提取**

从 FlovaHomePage 拆出以下可复用组件：

| 组件 | 来源 | 复用位置 |
| --- | --- | --- |
| `Sidebar` | FlovaHomePage 左侧导航 | 主页 + 工作台 + 后台 |
| `TopNav` | FlovaHomePage 顶部导航 | 主页 |
| `GradientCard` | FlovaTV 卡片样式 | 案例区 + 产物卡片 + 套餐卡片 |
| `ArtTitle` | Cormorant Infant 斜体标题 | 全平台标题 |
| `StyleButtonGroup` | 风格选择按钮组 | 主页工具栏 + 工作台工具栏 |
| `ParticleCanvas` | 背景粒子动效 | 主页 Hero 区 |
| `RadialGlow` | 多层径向光晕背景 | 主页 Hero 区 |
| `StatusBadge` | 状态标签（进行中/完成/停止/失败） | 产物卡片 + 项目列表 |
| `EmptyState` | 空状态图示 + CTA | 全平台空列表 |
| `SkeletonCard` | 骨架屏加载态 | 产物卡片加载 |

交付物：`frontend/src/components/ui/` 目录下上述组件，每个组件独立文件 + index.ts 导出。

**0.3 字体加载**

确认 `index.html` 已引入 Google Fonts（Cormorant Infant + Inter + JetBrains Mono）。若无则补充。

### 验收

- 所有组件在 Storybook 或独立测试页可独立预览
- 设计 Token 在浅色/深色模式下均可用（当前只做深色，Token 结构预留浅色扩展）
- FlovaHomePage 改为使用共享组件后仍正常渲染

---

## Phase 1：Agent 后端框架移植（6–8 人日）

### 目标

从 NarratoAI-PC 移植 Agent 核心框架到 avatar-agent 后端（Go），建立 AgentSession、工具注册、提案/审核/确认的完整后端机制。

### 参考源码映射

| NarratoAI-PC 文件 | 移植到 | 移植说明 |
| --- | --- | --- |
| `movie-vision.service.ts` agentChat | `backend/internal/agent/session.go` | Agent 会话入口 |
| `movie-agent-edit.service.ts` | `backend/internal/agent/edit.go` | 提案生成 + 乐观锁 + 变更执行 |
| `movie-agent-pi-plugin.ts` | `backend/internal/agent/tools.go` | 工具注册 + 三道闸（重复/超限/校验） |
| `pi-agent.service.ts` | `backend/internal/agent/pi.go` | LLM 调用 + JSON 信封协议 + 工具循环 |
| `movie-agent-conversation.ts` | `backend/internal/agent/conversation.go` | 对话历史归一化（24 条/16k 字符预算） |
| `movie-agent-run.ts` | `backend/internal/agent/runner.go` | 计划执行器（读持久化状态驱动工作流） |
| `movie-confirmation-planner.ts` | `backend/internal/agent/confirmation.go` | 审核意见 → 确认选项生成 |
| `movie-agent-evidence.ts` | `backend/internal/agent/evidence.go` | 引用校验 |
| `movie-agent-memory.ts` | `backend/internal/agent/memory.go` | 四类记忆（preference/fact/decision/checkpoint） |
| `movie-harness.ts` | `backend/internal/agent/harness.go` | Issue 结构 + TaskContract 字段白名单 |

### 任务清单

**1.1 AgentSession 核心**

```
POST /api/v1/agent/chat          — 发送消息，启动 Agent 轮次
GET  /api/v1/agent/stream        — SSE 流式推送（thinking/tool/question/media_request）
POST /api/v1/agent/confirm       — 用户确认提案/审核选项
POST /api/v1/agent/stop          — 中止当前 Agent 轮次
GET  /api/v1/agent/history       — 获取对话历史
```

实现要点：
- 每轮重新构建分层上下文（规则层 → 预设层 → 索引层 → 记忆层 → 实时态）
- `AbortSignal` 支持用户中止
- 对话历史与项目数据分别存储，对话历史不进入 Workflow 状态机
- SSE 事件类型：`thinking` / `tool` / `question` / `question_closed` / `media_request`

**1.2 工具注册与三道闸**

工具按项目状态动态裁剪，初始工具集：

| 工具名 | 何时可用 | 职责 |
| --- | --- | --- |
| `plan_project` | 总是 | 提交制作计划（once:true），不直接执行 |
| `edit_script` | 有脚本段落 | 修改文案段落，生成提案 |
| `edit_voice` | 有配音段 | 修改 TTS 参数，重做对应段 |
| `edit_material` | 有素材需求 | 调整 F05 素材需求 |
| `edit_shot` | 有分镜 | 调整 F10 分镜方案 |
| `evidence` | 总是 | 检索项目数据（段落/素材/分镜概要） |
| `memory` | 总是 | 长期记忆读写 |
| `run_control` | 有活跃运行 | 暂停/继续/停止/批准 |
| `advice` | 总是 | 纯文字建议，不触发任何节点 |

三道闸：
1. 重复调用拦截（`plan_project` 全局只能提交一次）
2. 超限拦截（单轮工具调用上限 12 次）
3. 独立校验失败拦截（校验失败时 block，原因回灌模型）

**1.3 提案机制**

```go
type Proposal struct {
    ID           string   // attempt id
    RowIDs       []int    // 影响的段落/素材/镜头 ID
    Revision     int      // 生成时的项目版本
    Lines        []string // 人话描述变更内容（非 diff）
    AffectedSteps []string // 影响的下游节点（如 "F04","F06"）
}
```

核心约束：
- Agent 生成提案但不直接写入项目数据，等用户确认
- 用户确认后 0 次模型调用直接套用（`acceptProposal`）
- 项目版本变化时旧提案自动失效（`revision` 比对）
- 同一项目只保留最后一条未确认提案

**1.4 乐观锁**

```go
type EditVersion struct {
    Revision       int    // 项目 revision
    Fingerprint    string // 段落内容 SHA256
}
```

- 每次修改前比对版本指纹，不符则返回 `conflict`，不覆盖
- 并发请求用 `inflight` Map 去重，同 requestId 只跑一次
- 写入用 `locked()` 串行化同一项目的提交

**1.5 审核门控**

复用 NarratoAI-PC 的四类审核模式，适配为数字人场景：

| 审核类型 | 触发节点 | 性质 | 交互方式 |
| --- | --- | --- | --- |
| 文案审核 | F02 完成后 | 非阻断 | 列出问题 + 修改建议，用户可选：接受/让Agent改/自己编辑/重写 |
| 配音审核 | F04 全部完成后 | 阻断式 | 整批一次顺序试听，确认后才继续 F05 |
| 画面匹配审核 | F10 完成后 | 提醒式 | 时间线标记 + 上下文注入，不阻断成片 |
| 事实核验 | F10 完成后 | 阻断式 | 逐段核验素材与台词语义匹配，失败时生成确认选项 |

确认选项生成规则（来自 NarratoAI-PC `confirmation-planner`）：
- 每题 2–4 个内容明确、互有区别的选项
- 禁止"是/否"、"忽略/重试/继续"等空选项
- 选项文字禁止出现内部编号，必须写真实内容
- 审核意见"不是事实，也不是必须反复提问的命令"
- 技术错误（格式/服务）退给工作流重试，不当事实问题问用户

### 验收

- Agent 收到用户消息后能识别意图、调用工具、返回结果
- 提案生成后用户确认才写入数据，拒绝则数据不变
- 版本冲突时返回 conflict，不覆盖并发变更
- 工具调用事件通过 SSE 实时推送
- 中止信号生效，用户点击停止后 Agent 轮次终止

---

## Phase 2：前期沟通流程（4–5 人日）

### 目标

实现用户描述需求 → Agent 沟通确认 4 项内容 → 用户确认方案 → Agent 生成 F01 草稿 → 进入工作台的完整流程。

### 任务清单

**2.1 Agent 前期沟通协议**

用户在主页对话窗口输入需求后，Agent 进入"前期沟通"模式，依次确认 4 项：

```
步骤 1 — 文案方向
  Agent：根据用户主题，生成 2–3 个文案方向提案
  每个方向：标题/受众/风格/预估时长/结构概要
  用户选择一个方向或自己描述
  → 对应 NarratoAI-PC story step

步骤 2 — 实现方式
  Agent：根据文案方向，规划制作路径
  内容：数字人出镜方案（是/否/小窗）、动画类型（矢量/线条/白板）、真实素材（有/无）
  用户确认或调整
  → 对应 NarratoAI-PC prepareCast + writing_preferences gate

步骤 3 — 素材裁剪需求
  Agent：列出素材需求清单
  内容：每段需要什么类型的素材、各做多久、内容要求
  用户确认或调整单条需求
  → 对应 NarratoAI-PC prepareMovieCompositionDialogue

步骤 4 — 最终效果预期
  Agent：描述预期成片效果
  内容：总时长/节奏风格/画面比例/音色确认/整体观感描述
  用户确认后 Agent 生成 F01 七项输入草稿
  → 对应 NarratoAI-PC voice_review gate
```

**2.2 前期沟通数据结构**

```go
type PreProductionPlan struct {
    ScriptDirection   ScriptDirection   // 步骤 1 确认结果
    ProductionMethod   ProductionMethod   // 步骤 2 确认结果
    MaterialNeeds      []MaterialNeed     // 步骤 3 确认结果
    FinalExpectation   FinalExpectation   // 步骤 4 确认结果
    Status             string             // draft / confirmed
    ConfirmedAt        *time.Time
}

type ScriptDirection struct {
    Title       string
    Audience    string
    Style       string
    EstDuration string
    Structure   string  // 开场/主体/结尾概要
}

type ProductionMethod struct {
    AvatarEnabled   bool
    AnimationType   string  // 矢量/线条/白板/none
    RealFootage     bool
    AspectRatio     string
    VoiceID         string
    AngleMode       string  // single / multi
}

type MaterialNeed struct {
    SegmentID   int
    Type        string  // avatar / animation / real_footage
    Duration    string
    ContentReq  string
}

type FinalExpectation struct {
    TotalDuration string
    PacingStyle   string
    AspectRatio   string
    VoiceConfirmed bool
    Description   string
}
```

**2.3 前期沟通 API**

```
POST /api/v1/agent/pre-production/start   — 开始前期沟通
POST /api/v1/agent/pre-production/confirm — 确认某一步骤
GET  /api/v1/agent/pre-production/:id     — 获取当前沟通状态
POST /api/v1/agent/pre-production/finalize — 确认全部，生成 F01 草稿
```

**2.4 确认卡片前端组件**

前期沟通的每一步都用确认卡片展示（复用 `MovieDecisionCard` 模式）：

```
┌─ 文案方向确认 ──────────────────────┐
│                                       │
│  方向 A：面向初学者的 React Hooks 教程 │
│  受众：前端初学者                      │
│  风格：轻松讲解                        │
│  预估时长：3-5 分钟                    │
│  结构：开场 → useState → useEffect    │
│                                       │
│  方向 B：...                          │
│                                       │
│  [选择方向 A]  [选择方向 B]  [自己描述] │
└───────────────────────────────────────┘
```

### 验收

- 用户输入主题后 Agent 依次引导完成 4 项确认
- 每一步的确认卡片有 2–4 个明确选项 + 自由输入入口
- 用户确认全部 4 项后 Agent 自动生成 F01 草稿并展示
- 用户可在任一步骤回退修改之前的选择
- 未完成前期沟通不能进入工作台

---

## Phase 3：工作台 UI + Agent 对话前端（5–7 人日）

### 目标

实现 PRD 第 17.4 节定义的三区工作台布局，包含 Agent 对话窗口、节点产物卡片、提案卡片。

### 任务清单

**3.1 路由结构**

```
/                          — 主页（Landing + Agent 对话入口）
/workspace/:projectId      — 工作台
/projects                  — 项目列表
/assets                    — 素材库
/styles                    — 风格库
/subscription              — 订阅与账单
/recharge                  — 充值
/settings                  — 个人设置
/pricing                   — 定价（公开）
/login /register /forgot   — 认证
/admin                     — 后台管理（含子路由）
```

**3.2 主页实现**

使用 Phase 0 共享组件构建：
- 顶部导航栏（Logo + 导航链接 + 登录/注册按钮）
- 左侧导航（登录后显示，200px）
- Hero 区（未登录：大标题 + CTA；已登录：Agent 对话窗口）
- 底部工具栏（风格按钮组 + 比例/音色/角度下拉框）
- FlovaTV 公共案例区（渐变卡片 + 分类 Tab）

主页 Agent 对话窗口：
- 输入框（文字 + 图片拖拽 + 视频上传 + @引用 + /命令）
- 消息气泡（用户右对齐橙色 / Agent 左对齐深色磨砂）
- 发送按钮 `生成 →`
- 进入前期沟通流程（Phase 2）

**3.3 工作台三区布局**

```
┌─ 顶部工具栏（48px）──────────────────────────────────┐
│ 项目名 [可编辑]  节点进度条 F01→F12  [停止] [导出]    │
├─ 左区 220px ─┬─ 中区 flex:1 ──────┬─ 右区 380px ────┤
│              │                    │                  │
│ 工具导航     │ 节点产物卡片列表   │ Agent 对话       │
│ 素材库       │ F01 [✓] 输入摘要   │ 消息气泡流       │
│ 风格库       │ F02 [✓] 文案预览   │                  │
│ 项目管理     │ F04 [⟳] 生成中     │ [提案卡片]       │
│ 历史记录     │ F05 [ ] 待开始     │ 确认 / 取消      │
│              │                    │                  │
│ 项目列表     │ [预览抽屉]         │ [工具状态 pill]  │
│ 当前项目 ●   │                    │                  │
│ 历史...      │                    │ 输入框           │
│              │                    │ [紧凑工具栏]     │
└──────────────┴────────────────────┴──────────────────┘
```

**3.4 节点产物卡片**

每个 F 节点一张卡片，竖向堆叠，已执行节点显示内容，未执行灰色占位：

| 节点 | 卡片内容 | 操作 |
| --- | --- | --- |
| F01 | 缩略图 + 主题 + 配置摘要 | [查看详情] |
| F02 | 段落数 + 字数 + 前3段预览 | [查看全文] [让 Agent 修改] |
| F03 | 段落列表 + 各段时长 | [确认拆解] [调整] |
| F04 | 每段时长 + 试听按钮 | [试听] [重做某段] |
| F05 | 需求数量 + 分类统计 | [查看清单] [调整] |
| F06–F08 | 缩略图网格 + 生成进度 | [预览] [重做] |
| F10 | 镜头数 + 布局分布预览 | [查看分镜] [让 Agent 调整] |
| F11 | 视频缩略图 + 时长 | [全屏播放] |
| F12 | 视频缩略图 + 时长 + 文件大小 | [全屏播放] [下载 MP4] [下载 SRT] |

正在执行中的卡片：灰色背景 + 橙色进度条 + 已用时间计时器。
卡片右上角"让 Agent 重新生成"按钮：点击后右区对话窗口自动填充对应修改意图。

**3.5 Agent 对话窗口（右区）**

- 消息气泡流：用户消息右对齐（橙色背景），Agent 消息左对齐（深色磨砂卡片），支持 Markdown
- 提案卡片：
  - 标题（变更类型）
  - 变更内容摘要（before/after 对比）
  - 影响范围提示（"将重做 F04 配音"）
  - [确认执行]（橙色）/ [取消]（灰色）
- 工具状态 pill：实时显示 Agent 当前工具调用（"正在生成配音…"）
- 输入框：固定底部，支持文字/图片/视频上传
- 底部紧凑工具栏：风格/比例/音色下拉（比主页版本小）

**3.6 节点进度条**

顶部工具栏内的紧凑进度条：
- F01–F12 十二个节点圆点，连线串联
- 已完成：绿色 ✓
- 进行中：橙色脉冲
- 未开始：灰色
- 点击已完成节点：滚动到对应产物卡片

**3.7 SSE 事件消费**

前端订阅 `/api/v1/agent/stream`，按 `turnId` 匹配当前轮次：
- `thinking`：更新 Agent 消息气泡的流式文本
- `tool`：更新/追加工具状态行
- `question`：弹出确认卡片
- `question_closed`：关闭确认卡片
- `media_request`：触发中区对应产物的生成动画

迟到的事件（`turnId` 不匹配）直接丢弃。

### 验收

- 主页登录后可发起 Agent 对话，进入前期沟通
- 工作台三区在 1280px 以上正常显示
- 节点进度条正确反映执行状态
- Agent 提案卡片确认后中区对应产物卡片更新
- SSE 流式消息无延迟、无丢消息

---

## Phase 4：节点审核介入（4–6 人日）

### 目标

实现 Agent 在 F02（文案审核）、F05（素材需求审核）、F10（分镜审核）三个关键节点的自动审核 + 确认卡交互。

### 任务清单

**4.1 F02 文案审核（非阻断）**

Agent 在 F02 完成后自动审核：
- 结构完整性（开场/主体/结尾是否齐全）
- 专业性和准确性（关键词覆盖、专业术语使用）
- 台词时长合理性（与目标时长匹配）

审核结果：
- 无问题：自动继续 F03，不打断用户
- 有问题：对话窗口输出审核报告 + 确认卡片

确认卡片选项（每题 2–4 个明确选项）：
```
┌─ 文案审核结果 ─────────────────────────────────┐
│                                                 │
│ 第 2 段问题：                                   │
│ "useEffect 的依赖数组"部分表述可能让初学者困惑   │
│                                                 │
│ ○ 让 Agent 修改这段（推荐）                     │
│   Agent 会重新生成第 2 段，保持其他段不变        │
│ ○ 我自己改                                      │
│   打开文案编辑器手动修改                        │
│ ○ 接受当前版本继续                              │
│   不修改，直接进入 F03 拆解                     │
│                                                 │
│ [确认选择]                              [稍后]  │
└─────────────────────────────────────────────────┘
```

**4.2 F05 素材需求审核（阻断式）**

Agent 在 F05 完成后自动审核：
- 素材类型与文案内容匹配
- 时长覆盖完整
- 无无法生产的需求

审核结果以可视化列表展示：
```
┌─ 素材需求确认 ──────────────────────────────────┐
│                                                  │
│ 第 1 段（12.4s）：数字人上半身口播  ✓            │
│ 第 2 段（8.2s）：useState 流程动画   ✓            │
│ 第 3 段（15.6s）：数字人 + 操作录屏  ✓            │
│ 第 4 段（6.0s）：useEffect 代码动画 ⚠ 需确认     │
│   Agent 建议：改为对比图动画，更适合展示前后变化  │
│                                                  │
│ [全部确认]  [调整第 4 段]  [让 Agent 重新规划]   │
└──────────────────────────────────────────────────┘
```

**4.3 F10 分镜审核（提醒式 + 阻断式混合）**

F10 完成后两层审核：

第一层 — 画面匹配提醒（不阻断）：
- 画面类型多样性检查（是否全是单一布局）
- 素材与台词语义相关性检查
- 结果以时间线标记形式注入 Agent 上下文

第二层 — 事实核验（阻断式，逐段）：
- 每段分镜的素材是否与台词语义匹配
- 人物出镜比例是否符合用户预期
- 失败段生成确认选项，用户选择后继续

```
┌─ 分镜审核 ──────────────────────────────────────┐
│                                                  │
│ 第 5 段分镜问题：                                │
│ 台词讲"useEffect 依赖数组变化"，但画面是人物口播  │
│                                                  │
│ ○ 换成代码动画展示依赖变化（推荐）               │
│   将重新规划第 5 段分镜，使用动画素材             │
│ ○ 保留人物口播，补充代码字幕                      │
│   在人物画面上叠加代码字幕                        │
│ ○ 我自己调整分镜                                 │
│   打开分镜编辑器手动修改                          │
│                                                  │
│ [确认选择]                              [稍后]   │
└──────────────────────────────────────────────────┘
```

**4.4 审核结果缓存**

按输入指纹缓存审核结果（复用 NarratoAI-PC 机制）：
- 版本 + 行内容 + 对白 + 用户确认 全部 hash
- 什么都没变就复用，一变就失效
- 文案修改时自动删除分镜审核缓存（`delete compositionDialogueReview`）

### 验收

- F02 完成后 Agent 自动审核，无问题时自动继续，有问题时弹出确认卡
- F05 完成后素材需求列表可视化展示，用户可逐条确认或调整
- F10 完成后两层审核正常触发，事实核验失败时生成明确选项
- 确认选项每题 2–4 个，无"是/否"空选项
- 审核结果缓存有效，修改文案后分镜审核缓存自动失效

---

## Phase 5：SaaS 商业化页面（5–7 人日）

### 目标

完成定价/订阅/充值/后台管理/个人设置等 SaaS 平台必需页面，全部使用 Phase 0 设计系统。

### 任务清单

**5.1 定价页（/pricing）**

- 三列套餐对比卡片（免费版 / 专业版 / 团队版）
- 年/月切换 Toggle，切换时价格动态过渡
- 每档：价格大字号 + 功能清单（✓ 已包含 / — 不包含）
- CTA：推荐套餐橙色主按钮，其余白色次按钮

**5.2 订阅管理页（/subscription）**

- 当前套餐状态卡片：套餐名 + 到期时间 + 已用额度进度条
- 用量详情：本月模型调用次数、TTS 字符数、视频生成分钟数（图表）
- 历史账单列表：日期 + 金额 + 类型 + 下载发票
- 套餐升级/降级入口

**5.3 充值页（/recharge）**

- 积分包选项（多档，橙色高亮推荐档）
- 支付方式：微信支付 / 支付宝
- 支付状态流：待支付 → 处理中 → 成功/失败 → 到账确认

**5.4 个人设置页（/settings）**

分 Tab：
- 账号与安全（邮箱修改、密码修改、双因素认证、登录历史）
- 通知偏好（邮件通知开关）
- API 访问（API Key 管理）
- 数据与隐私（导出数据、注销账号）

**5.5 后台管理（/admin/*）**

独立侧边栏导航（深色主题保持不变）：

| 页面 | 内容 |
| --- | --- |
| /admin 总览 | 核心指标卡片 + 折线图 + 快捷入口 |
| /admin/users | 搜索筛选 + 用户列表表格 + 详情侧滑 |
| /admin/plans | 套餐配置列表 + 权限矩阵编辑 + 变更历史 |
| /admin/templates | 模板卡片列表 + 上传/编辑/发布/下架 |
| /admin/notices | 公告列表 + 富文本编辑 + 定时发布 |
| /admin/billing | 收入汇总图表 + 模型用量明细 + 导出 CSV |

**5.6 项目列表页（/projects）**

- 筛选栏（状态/风格/时间范围）
- 项目卡片网格（4 列）：封面图 + 项目名 + 状态标签 + 时长 + 最近修改 + 操作菜单
- 空状态引导

### 验收

- 定价页三档套餐清晰对比，年/月切换正常
- 订阅页用量进度条准确，超额警告色
- 后台管理仅管理员可访问，非管理员跳转 403
- 全部页面使用统一设计 Token，无混用其他色彩规范

---

## Phase 6：联调验收（4–5 人日）

### 任务清单

**6.1 全流程联调**

从主页发起对话 → 前期沟通 4 项确认 → 进入工作台 → F01–F12 执行（Agent 各节点审核介入）→ 成片交付的完整链路。

**6.2 响应式验证**

- 工作台 1280px 三区可用
- 主页/定价页 768px 正常展示
- 平板 768px：左区收起为图标，右区可折叠

**6.3 数据一致性**

- Agent 对话历史与项目数据分离存储
- 版本乐观锁在并发修改时正确拦截
- 前期沟通确认后 F01 草稿字段完整映射七项输入

**6.4 部署**

- 更新 WSL2 Docker 部署
- 保留已有数据和任务素材
- 验证登录、历史任务、新流程

### 验收

- 完整链路从对话到成片可走通
- 响应式在各断点正常
- 部署后入口可用，实测 IP 与端口反馈

---

## 实施约束

- 参考目录 `D:\pro\ai_cat\参考\NarratoAI-PC\` 只读，不在参考项目里修改业务代码
- 框架复用（对话协议、提案机制、乐观锁、工具事件流、审核门控）直接移植，替换工具插件实现
- 前端全部使用 Phase 0 设计系统，不允许混用旧 UI 组件
- Agent 不绕过 Workflow 节点前置条件，不自主发起付费生成
- 所有修改通过提案确认机制，用户确认后才写入数据
- API Key 只在服务端，不进入前端
- 保留现有认证逻辑、任务持久化和 API 对接层

## 优先级与依赖关系

```
Phase 0（设计系统）──┐
                     ├──→ Phase 2（前期沟通）──→ Phase 3（工作台）──→ Phase 4（节点审核）
Phase 1（Agent后端）──┘                                              │
                                                                     ├──→ Phase 6（联调）
                                              Phase 5（SaaS页面）───┘
```

Phase 0 和 Phase 1 可并行；Phase 2 依赖 Phase 0+1；Phase 3 依赖 Phase 2；Phase 4 依赖 Phase 3；Phase 5 可与 Phase 3–4 并行；Phase 6 依赖全部完成。
