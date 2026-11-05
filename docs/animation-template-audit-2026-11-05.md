# 动效风格数量与参考效果差异排查

排查日期：2026-11-05。范围：创建页、目录接口、F07 动画生产、参考源码及 WSL2 Docker 实际运行文件。本轮只做诊断与文档校正，未修改运行代码、未创建制作任务、未调用付费模型。

## 结论

两个现象均属当前实现范围和适配质量问题。数量少来自 P0 只做两套风格及代码白名单；效果不一致来自统一简化图解组件未保留参考示例的核心动作与镜头设计。不能把这两套简化实现视为参考风格忠实度已经验收。

## 证据

| 现象 | 定位 | 实际行为 |
| --- | --- | --- |
| 仅两个可选风格 | `backend/internal/server/avatar_workflow_catalog.go` 的 `workflowCatalog`、`validateInput`、`workflowStylePreview` | 三处均限定 `flat-vector`、`line-art`；不是扫描参考目录，也不是通过 .env 扩充 |
| 前端只显示两项 | `frontend/src/features/avatarMvp/AvatarWorkflowPage.tsx` | 直接遍历 `catalog.styles`，没有遗漏第三项或分页隐藏 |
| 两风格较相似 | `backend/avatar-runtime/src/workflow-animation.tsx` | 共用一个 `AnimationScene`，共用步骤、对比、关系、强调、总结结构；主要改变配色、填充/描边和入场动画 |
| 未执行原示例源码 | `backend/avatar-runtime/workflow-render.mjs` 的 `animation` 分支及 `src/workflow-entry.tsx` | 渲染当前 Remotion `AnimationTemplate`，未执行 `templates/*/upstream/` 原片源码 |
| 原提示词未指导本次视觉生产 | `backend/avatar-runtime/workflow.py` 的 `command_material` | 先渲染，再读取 `original-prompt.md` 并附加本次参数作为归档；这提供来源记录，但不会让当前组件产生原片核心动作 |
| 画面要求利用不足 | `workflow.py` 的 `template_manifest` 与 `src/workflow-animation.tsx` | F05 的内容要求写入 `brief`，组件未消费该字段；主要将本段原稿按标点拆成最多四块文本，而非按需求制作对应视觉元素 |
| 缩略图不是参考原片 | `workflow-render.mjs` 的 `catalog` 分支 | 固定使用 4 秒、360×640、25fps 的步骤说明样例；两套均用同样的示例文字，未展示各自完整模板集 |
| 发布判断不足 | `workflowCatalog` | `published` 只判断预览 MP4 文件存在，不能证明核心风格特征、完整示例和参数适配均验收通过 |

## 与原参考的具体差异

| 风格 | 参考核心 | 当前实现 |
| --- | --- | --- |
| 扁平矢量 | 圆点变太阳、城市错峰搭建、推进窗户、几何角色动作、图形承载转场、弹性与跟随动作 | 高饱和配色、卡片弹入、文本与简单连接线；缺少上述场景叙事与元素变形 |
| 线条动画 | 连续一笔生长，种子到植物到建筑/天际线，笔尖跟随镜头，线面转换及整体拉远揭示 | 深色背景、金色描边矩形和连线；没有连续造型演变与镜头跟随 |

不要求复制原故事、品牌、音乐或固定十秒节奏；但 PRD F07 要求保留决定风格的核心视觉和动作特征，当前在这点仍有缺口。

## 参考库与上线范围

- 本地 `参考/mg-styles-15/videos/` 有 15 个 MP4 示例；当前只发布了两个简化样式，不等于另外十三套已经适配。
- `docs/animation-template-sources.json` 将其余三个动效仓库标为 `reference-only`，没有可生产模板发布记录。
- 开发计划 V2.2 的 P0 最低范围是两套风格，因此数量本身符合首版最低范围；模板忠实度不应因此降低要求。
- 原 `SOURCE.json` 的实现入口仍指向 `workflow-entry.tsx: AnimationScene`，实际组件已经拆到 `workflow-animation.tsx`；来源版本 `1` 与目录版本 `2` 也缺少明确映射。
- 页面只显示来源及比例支持，未解释当前是简化重写，容易使用户误认为已经复用了完整原模板。

## 线上核查

- WSL2 Docker 目录接口鉴权请求返回 HTTP 200，实际返回且发布的风格恰为上述两个，各列出五种内部模板。
- 容器 `/app/avatar-runtime/src/workflow-animation.tsx` 与 Windows 工作区文件 SHA-256 相同：`aedaec2433177f907a03eac01a4c406d5ee96c33073356089a80b6fce4e73f97`。
- 容器线条动画预览经 FFprobe 确认为 360×640、25fps、4.000 秒，与当前目录生成脚本一致。
- 因此不是仅有旧容器或浏览器缓存造成的差异；刷新、清缓存或调整模型 Key 不会补齐模板实现。

## 修复优先级

1. 先纠正当前两套的名称说明、来源入口和版本关系，明确简化实现及未通过的风格忠实度要求；历史任务保持原版本可查。
2. 基于原源码和提示词逐套提取核心动效，建立可控内容、动作、时长、字词锚点和三比例参数；让 F05 需求实际驱动画面，而不只把原稿放入卡片。
3. 每套分别提供完整示例与五类模板预览；缩略图、正式渲染和版本绑定。与原参考做核心特征对照，并用当前中文稿件实测。
4. 两套质量补齐后，再按参考库逐批扩充。仅有外链/提示词的案例先作为参考展示，不标成可制作；不把原片视频替换进缩略图冒充生产能力。

本报告不表示上述修复已实施，也不改变已有成片、订单或费用记录。
