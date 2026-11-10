# 制作能力补齐计划：技能绑定、背景音乐与音效、AI 生图、AI 视频空镜、模型对比

状态：计划稿（2026-11-09），尚未开发。可用模型清单见 `docs/runninghub-model-catalog.md`。

## 0. 目标与原则

- 工具和模型只在后端固定调用，不对终端用户开放插件、MCP 或安装能力。
- 所有新模型统一走 RunningHub 国际站的标准模型 API（`/openapi/v2/...` 提交，`/openapi/v2/query` 查询）。复用现有 `avatar.RunningHub` 客户端的上传、提交、查询、下载能力，以及防重复付费机制（`beforeSubmit` 写入 submitting 状态，订单状态待确认时禁止重新提交）。
- 测试阶段**列出全部可用模型供选择**，并提供"同一输入、多模型并排对比"。对比结果用来选出默认模型，之后可以收窄清单。
- 遵循 TDD：每个适配器先写请求结构测试（路径、字段、枚举映射、不支持参数时在本地拒绝），再接入工作流节点。
- 每个阶段完成后部署到 WSL，并做浏览器验收。

## 1. 统一的模型注册表（所有步骤共用的基础）

参考已经完成的 TTS 模型注册表（`avatar/speech_models.go`），扩展成通用的媒体模型注册表：

```
MediaModel {
  ID        // 如 "kling-v3.0-pro/image-to-video"
  Kind      // music | sfx | t2i | i2i | t2v | i2v | r2v | enhance
  Family    // kling | seedance | veo | wan | vidu | hailuo | sora | ...（决定使用哪个适配器）
  Name, Path
  Caps      // 支持的时长、画幅、分辨率、首帧/尾帧、原生音频、参考图数量
  Adapter   // 把统一请求映射成该接口的 JSON
}
```

- **注册表数据**：由脚本从 RunningHub 的 OpenAPI 文档生成 `media_models.json`（打包进程序），记录路径、各字段的枚举值和必填项。人工维护的只有"家族适配器"。
- **家族适配器**：大约 15 个，例如：
  - kling 用 `firstImageUrl`、`duration`、`sound`
  - seedance 用 `firstFrameUrl`、`ratio`、`generateAudio`
  - veo3.1 用 `imageUrl`、`aspectRatio`、`generateAudio`
  - 另外还有 wan、vidu、hailuo、sora、grok、pixverse、happyhorse、skyreels、gemini-omni、minimax-h3、flux-3、midjourney，以及图片侧的 nano-banana、gpt-image、seedream、qwen-image、midjourney 等

  统一请求是 `{prompt, image(s), durationSec, ratio, resolution, audio}`，适配器负责把它**向下取到该模型支持的最近值**（例如把 7 秒转成 kling 允许的 5 或 10 秒）。实在不支持的组合在本地就拒绝，不下单。
- **接口**：`GET /avatar-mvp/catalog` 新增 `media_models`，按 Kind 分组，带上能力和是否已配置。项目输入新增 `media_models: {music, image, video}`；留空时使用部署默认值，老项目不受影响。

工作量：3–4 天（包括生成脚本、15 个适配器的结构测试、目录接口）。

## 2. 步骤 1：skill 绑定到节点

- 改成从 `SKILL.md` 的开头元信息生成 skill 注册表，并新增字段 `nodes: [F01.6, F10]`。
- `w.model(...)` 调用模型时，按节点自动注入已绑定的 skill 正文。项目首次运行时记录 `skill_hash`，同一项目重做时使用同一份 skill，保证可复现。
- 新增三个后端固定 skill（只能在代码库里修改）：
  - `bgm-sfx-direction`：背景音乐风格、情绪随段落变化、音效放置原则
  - `cover-design`：封面构图和文字安全区
  - `broll-prompting`：空镜提示词写法和镜头语言
- 工作量：3–4 天。

## 3. 步骤 2：背景音乐、音效与混音

**新增节点 F09.5「声音设计」**，在 F09 素材接入之后、F10 分镜之前，此时人声时长已经确定：
1. 导演稿里的"声音"计划加上 `bgm-sfx-direction` skill，由 LLM 输出 `{bgm: {prompt, mood_curve}, sfx: [{at, prompt, duration}]}`。
2. **背景音乐**：调用所选音乐模型生成纯音乐，裁剪或循环到与成片等长，在拐点做交叉淡化。
3. **音效**：目前没有专门的音效接口，有两种实现：
   - A：`doubao-seed-audio-1.0` 生成环境音和短音效
   - B：本地音效库（需要准备有授权的素材）

   测试阶段两种都做，用对比来决定。
4. **混音（本地 ffmpeg）**：人声优先，背景音乐在人声段自动降低音量；统一响度到 -14 LUFS；片头片尾淡入淡出。写入 `workflow.py` 的 `assemble` 和 `render` 步骤。
5. **Agent 工具**：新增 `edit_audio`（换背景音乐或音乐模型、调音量、删除或移动某个音效），同样先生成提案，用户确认后再执行。
6. **默认关闭**：项目开关 `use_bgm`。老项目不产生额外费用。

可选模型（13 个，详见目录第一节）：

| 类型 | 模型 |
|---|---|
| **纯音乐背景音乐（推荐优先测试）** | `minimax/music-2.6/text-to-instrumental`、`mureka-v9/generate-bgm`、`mureka-v8/generate-bgm`、`mureka-v7.6/generate-bgm` |
| 可生成纯音乐 | `suno-single-v5.5`、`v5`、`v4.5`（设置 `make_instrumental=true`；每次返回 2 首可选） |
| 带人声歌曲（主题曲类场景） | `minimax/music-2.6/text-to-music`、`minimax/music-2.5`、`suno-custom-v5.5`、`v5`、`v4.5` |
| 多模态音频（环境音和音效） | `doubao-seed-audio-1.0` |

工作量：6–8 天。

## 4. 步骤 3：AI 生图（封面和配图素材）

- 在素材规划里新增第 4 类素材 `image`，用于静态配图、信息图底图、分镜参考图，以及 F12 封面。
  - 素材规划时由 LLM 生成提示词，按项目画幅选取最接近的支持比例。
  - 生成的图片加上 Remotion 的推拉、平移运动作为镜头使用。
- 封面：F12 默认继续从成片截帧；开启"AI 封面"后，用成片截帧加文案，通过图生图生成封面。
- 可选模型：**文生图 35 个**（seedream 4/4.5/4.7/5、nano-banana 系列、gpt-image 1.5/2、qwen-image 2/3、wan 2.5/2.7、midjourney v6.1/v7/v8.2/niji、grok、jimeng 4.6 等）；**图生图、编辑、放大 41 个**（topaz 系列放大、wan 2.7 编辑、seedream、nano-banana 编辑等）。
- Agent 工具：沿用 `edit_material`，增加"换模型重新生成"这个参数。
- 工作量：5–7 天。

## 5. 步骤 4：AI 视频空镜

- 新增第 5 类素材 `broll`，有三种生成方式：
  - 文生视频：由提示词直接生成
  - 图生视频：用第 4 步生成的图片或用户上传的图片作为首帧，部分模型支持首尾帧
  - 参考生视频：用多张参考图保持一致性
- 时长按分镜确定，适配器取模型支持的最近值，最后在本地裁剪。
- 复用 F06 的下单、轮询、防重复付费和 `PreserveOrder` 逻辑。分镜批次和失败重试沿用 `avatar_workflow_shot_batches.go`。
- 默认关闭模型自带的音频（成片声音由第 2 步统一处理），只有用户明确要求时才打开。
- 可选模型：**文生视频 60 个、图生视频 79 个、参考生视频 31 个**，覆盖 kling（v2.5、v2.6、v3、O1、O3、4k）、seedance（1.5 pro、2.0、2.0 fast/mini/global、2.5）、veo3.1（lite、fast、pro）、wan（2.5–3.0 prime）、vidu（q2、q3、q4）、hailuo（02、2.3、H3）、sora-2、grok-imagine、pixverse v6、happyhorse、skyreels v4、gemini-omni、flux-3 等。
- 可选后处理：视频增强 11 个（`rh-video-upscaler`、`volc-enhance` 三档、DetailX、topaz 系列），作为"成片增强"开关，测试阶段只在对比实验里使用。
- 数字人对比：`kling-v2-ai-avatar-standard/pro` 可以作为 F06 的备选模型加入对比，但不替换现有的 Seedance 2.5。
- 工作量：8–10 天（适配器最多，需要逐个家族做真实小样验证）。

## 6. 模型对比（测试阶段）

**后端：新增"模型实验"**
- `POST /avatar-mvp/model-lab/runs`，输入 `{kind, prompt, inputs, params, models: [...]}`。
- 每个模型单独下一个订单，最多同时运行 4 个，单次实验最多 8 个模型。提交前显示预计调用次数，需要二次确认。
- 记录每个模型的：结果文件、耗时、`thirdPartyConsumeMoney`、失败原因、实际发送的参数（包括被调整过的时长和画幅）。
- 结果存在 `workflow-v2/model-lab/`，和项目数据分开，不影响制作流程。

**前端：新增页面 `/model-lab`（只有管理员可用）**
- 选择类别（背景音乐、文生图、图生图、文生视频、图生视频、参考生视频、增强），多选模型，填写统一的输入。
- 结果按网格并排展示（音频带波形，图片和视频同步播放），标出耗时、费用、实际参数。
- 每个结果可以打分（1–5）和写备注，汇总成各模型的平均分、平均耗时、平均费用，可导出 CSV。
- 支持"设为该类别默认模型"，写入服务端配置，作为后续项目的默认值。

**项目内选择**
- 首页和工作台的设置里增加"背景音乐模型、图片模型、视频空镜模型"三个下拉框，显示方式和音色模型选择器一样（按家族分组，标出能力，例如"支持尾帧""原生音频""最长 10 秒"）。

工作量：6–8 天。

## 7. 排期（1 人，含测试和部署验收）

| 阶段 | 内容 | 工作量 | 交付后可测 |
|---|---|---|---|
| P0 | 模型注册表、生成脚本、目录接口 | 3–4 天 | 目录接口能看到全部模型 |
| P1 | skill 绑定到节点 | 3–4 天 | 导演稿和分镜质量对比 |
| P2 | 模型实验（后端和前端），先接音乐和图片 | 4–5 天 | 背景音乐和图片多模型并排对比 |
| P3 | 背景音乐、音效、混音节点 | 6–8 天 | 带背景音乐的完整成片 |
| P4 | AI 生图素材和封面 | 5–7 天 | 配图镜头、AI 封面 |
| P5 | 视频适配器（按家族分批）和空镜素材，模型实验加入视频 | 8–10 天 | 空镜镜头、视频模型对比 |
| P6 | 视频增强、数字人备选、默认模型收敛 | 2–3 天 | 成片增强对比 |
| **合计** | | **约 31–41 个工作日，6–8 周** | |

建议按顺序做：P0 → P1 → P2 → P3 → P4 → P5 → P6。P2 提前做，后续每一步都可以马上用对比来选择默认模型。

## 8. 风险与对策

| 风险 | 对策 |
|---|---|
| 测试成本：视频模型单次费用高，全量对比开销大 | 实验必须二次确认并显示预计调用次数；视频默认 480p 或 720p、4–5 秒；按家族分批测试 |
| RunningHub 文档里没有公开价格 | 记录每单实际扣费，在实验页汇总 |
| 不同模型参数差异大，可能出现 1007 校验错误 | 每个家族用适配器加结构测试，不支持的参数在本地拒绝；首次接入每个家族时先做 1 个真实小样 |
| 渠道版（channel-low-price）稳定性 | 标注出来，优先默认使用 official-stable 版本 |
| 授权：部分模型输出有商用限制（如 midjourney） | 在目录里标注，默认模型避免使用有限制的 |
| 音效没有专用接口 | 测试阶段比较 doubao-seed-audio 和本地音效库，再决定 |
| 成片时长和费用上升 | 背景音乐、生图、空镜都默认关闭，按项目开启 |

## 9. 需要确认的事项

1. 是否同意先做 P0 和 P2（模型实验），再做具体节点。
2. 模型实验的单次预算上限（例如每次最多 8 个模型、视频最长 5 秒）。
3. 音效方案：是否准备一套有授权的本地音效库作为方案 B。
4. AI 视频空镜是否允许出现真人（涉及肖像），还是只生成物体、场景、图形类镜头。
