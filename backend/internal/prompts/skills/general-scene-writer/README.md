# General scene writer v2

通过 Go embed 随后端部署的解说写作技能；不依赖开发者的 Codex、Pagekin 安装或运行时工具，不更换模型。

来源：[EthanYoQ/AI-Novel-Writer (Pagekin)](https://github.com/EthanYoQ/AI-Novel-Writer)，master 快照 `41172b7599b005be3ca369d15699dd6778947f55`，下载日期 2026-10-28。v1仅参考方法；v2实际引入其两个中文内置技能正文，分别保存在 pagekin-continuity.md 和 pagekin-refinement.md，来源与许可证见 PAGEKIN-NOTICE.md、PAGEKIN-LICENSE。完整上游下载在工作区 `.local/references/AI-Novel-Writer-master/`。

参考位置：

- `docs/adr/0015-writing-skills-are-stage-scoped-guidance.md`：技能按阶段选择，事实和输出契约优先。
- `src/shared/builtin-writing-skills.ts`：长篇连续性、场景推进、自然表达润色。
- `src/services/builtin-prompt-templates.ts`：当前情境目标、前文衔接、基于问题定向修稿。

素材解说适配：planning.md 整理有证据的情境；drafting.md 分配信息、推进局势；refinement.md 在授权范围内压实语言；source-and-ownership.md 处理人物来源、事件阶段、原片信息归属和自然表达。小说创作中的自由心理/感官描写、虚构人物和反转、评分审稿循环、工具执行和新章节创作不接入。

完整稿一次请求内使用上游连续性、自然润色和全部适配；已有的原片衔接调用及局部修复只使用上游自然润色与局部适配，不引入整片规划。逐字恢复及0%解说跳过创作技能。现有 Humanizer 和实际所选风格仍接入，实际提示词哈希包含技能与准备后的简报，更新规则不会命中旧写稿响应。

writingBrief 对两条素材路线建立人物候选来源、事件阶段/证据索引、包装提示和既有原片的交付索引。不改变源素材，不合并人物，不将总结、说话者编号或时间重叠当成事实，不新建向量库或调用模型。写稿完成的 metadata.writing_policy 和后端日志记录版本、阶段、实际消息SHA-256及模板，便于核查；不记录提示词正文或私密推理。

关系索引保留source→target方向，供原始证据核对。衔接位置另外给出eligibleEvidenceIds、sourceCues、已播/待播原片编号；源句必须完整落在该位置范围内。既有自动衔接调用不再输入未验证自动稿件的正文，改为从源素材写新正文，避免复制原稿的剧透和错误；锁定原片和标记诊断保留。局部、逐字恢复、用户已批准/as-saved请求不移除其正文。

视频路线写稿输入仅对完全重复的观察做去重：evidence.text 与 visual/audio/uncertainty 的标准组合完全相同时只传分字段；context.items 的观察和同 ID、同时间的 evidence.text 完全相同时改为 visualEvidenceId 引用。保留独立字幕、全局剧情理解、未知字段及任何不同版本的观察；不修改请求原对象、项目缓存、ASR 输入或下游检索匹配。
