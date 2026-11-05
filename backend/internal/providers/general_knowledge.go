package providers

const generalKnowledgeNarrativeRules = `

通用模式本地事件知识图谱补充规则（高于上面的 speaker 占位建议）：
- 同一 speaker_id 可能包含多个人物，尤其上传字幕的 speaker_0，不能以此认定为同一个人。人物使用独立的 character ID；姓名未知则用局部人物编号，不合并无法确认的身份。
- characters 的 name 只写单一人物名，不混写“人物/旁白/多个角色”。participants 和关系的 source/target 使用 characters 中同一个明确人物名；无法确认则留空，不猜测。
- 每条事件额外给出 action（具体动作）、phase（准备/进行/完成/反应/未知）、object（对象或空串）、support_type（dialogue_assertion/inference）。字幕表达意图不等于动作已发生；无画面证据不能声称观察到出血、跪地或进食。
- evidence 必须是该事件时间范围内逐字引用的短字幕，不写改写摘要。事件范围不能跨越互不相关的动作阶段，摘要不能替换原始对白。
- 人物关系必须有原始证据；前后发生不等于因果。无法支持的关系保留为 open_questions，不强行补全。
- 不重复输出整份字幕、不额外长篇推理，复用本次剧情理解一次生成事件结构。
`

const generalVideoNarrativeRules = `
本次是无字幕的视频理解工作流：visual_analyses 是直接观察带声音的视频得到的时间段事件，不是字幕。它包含画面观察、声音信息和不确定信息。
结合全部片段整理全片主线、人物关系、因果和事件，统一有依据的人物称呼；不确定身份不要强行合并。不能以字幕为空为由拒绝，也不能虚构对白。
事件 evidence 引用对应画面观察的原文；action、phase、object 分别描述实际动作、阶段、对象。support_type 使用 visual_observation 或 inference。只有声音信息的意图不等于画面中动作已发生。
时间使用输入的绝对秒数，不重置为每个片段的零点，不编造更细的时间。保留无对白的事件。源文件先后不等于因果。片头片尾、广告、预告不混入剧情。
`
