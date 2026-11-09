# 第七步动效“模型输出被截断”诊断

核查日期：2026-11-09，时间统一为北京时间（UTC+8）。本次只读取 WSL2 现有任务、数据库、调用记录和源码，并在现有容器内调用代码校验函数复现问题。没有发起模型请求、重试制作、改动业务代码或部署。

## 结论

存在三个独立问题：

1. **模型请求触及应用设置的单次输出上限 10,000 token。** 有 HTTP 200、`finish_reason=length`、`completion_tokens=10000` 的直接证据；没有证据表明是浏览器缓存、网络分包或模型上下文缓存耗尽。
2. **完整生成的“历史估值对比”动效被本地校验误拦。** 校验器把普通字符串 `'document'` 当成禁用的浏览器对象；这是当前该节点的实际渲染阻塞。
3. **顶部报错的节点名称与错误来源不一致。** 后端保留首个素材失败的错误，却继续执行后续素材；前端从最后执行的 `run.stage` 取节点名，再配上 `run.error`。因此其他节点的截断会显示在“历史估值对比”标题下。

当前不能仅靠流式传输或调大输出上限解决全部失败。

## 1. 证据来源与当前运行

项目：`465168c3-7348-4914-b535-b59d9f0d7d16`。

当前核查运行：`74f95998-614c-4cc7-ac9d-e94fcdc74376`。数据库中状态为 `failed`，最后执行步骤为 `F07/n_ead52812854138d6_1`，更新时间为 2026-11-09 09:33:14。

数据源：

- WSL 发行版 `Ubuntu-24.04`，现有 `aicat-avatar-api-1` 与 `aicat-avatar-postgres-1` 容器。
- 数据库 `workflow_step_executions` 中 `step_id=avatar.workflow.v2` 的 `result`。这是本次判断最终状态的依据；磁盘 `workflow.json` 是媒体步骤输入快照，仍可能显示执行中的状态。
- 容器内 `/app/data/avatar-mvp/workflow-v2/runs/<run-id>/operations.jsonl`。
- 同目录 `model-results/8083e032ca6910bd-attempt-1.json`、`materials/n_ead52812854138d6_1-motion-plan.json` 和 `material.log`。
- 运行配置快照记录 `flash_model`、`vision_model` 均为 `anthropic/claude-opus-5.5`；这只表示该次运行使用的模型标识，不推断供应商的底层模型规格。

本机与 WSL 部署源中的 `backend/internal/providers/avatar_content.go` SHA-256 相同，均为 `4cb03169d425ec8d1fb450f92c8492e21dac135b2d4918f7c723d1b0ca89a771`。

## 2. 截断的实际原因

当前运行的“资金到账/金币堆叠”素材（`F07/n_4c96608597fc1059_1`，段落 `p2`，约 11 秒）已发生三次相同类型的失败：

| 请求结束时间 | HTTP | finish_reason | 输入 token | 输出 token | 耗时 |
|---|---:|---|---:|---:|---:|
| 2026-11-09 08:59:35 | 200 | length | 8,557 | 10,000 | 94.56 秒 |
| 2026-11-09 09:28:21 | 200 | length | 8,557 | 10,000 | 103.43 秒 |
| 2026-11-09 09:31:47 | 200 | length | 8,557 | 10,000 | 96.46 秒 |

最后一次请求 ID：`chatcmpl-1fcb9c54-fb31-4ad5-8378-0ec5b04c445a`。

截图所指“历史估值对比”节点也曾在较早运行 `d7880572-1ed4-417a-adbc-222658a1dc73` 中触及相同限制：2026-11-09 00:55:37 返回 HTTP 200、`length`，输入 8,624 token、输出 10,000 token，耗时 103.55 秒。因此截断本身确实存在，但不能用这一旧记录解释该节点后续所有失败。

代码路径：

- [avatar_content.go:50](../backend/internal/providers/avatar_content.go#L50)：普通任务上限为 6,000；动效代码任务改成 **10,000**，作为 `max_tokens` 发送。这是应用设置，不是已测得的模型最大能力。
- [avatar_content.go:85](../backend/internal/providers/avatar_content.go#L85)：`finish_reason == "length"` 或 JSON 无效时拒收；`length` 被分类为 `model_output_truncated`。
- [avatar_workflow_execute.go:276](../backend/internal/server/avatar_workflow_execute.go#L276)：把此分类映射为用户看到的“模型输出被截断，未返回完整的 JSON”。
- [openai.go:406](../backend/internal/providers/openai.go#L406)：该流程明确使用 `stream=false`；响应体通过 `io.ReadAll` 完整读取，没有在这里设置固定响应字节截取长度。

单次请求需要同时完成场景结构与 Canvas 绘图代码，并用一个 JSON 返回。提示词虽要求代码在 6,000 字符内，这只是文本指令；**字符数与 token 数不是同一个计量单位**，它也不构成服务端对总输出 token 的可靠预算控制。单个素材内部可以包含 1–5 个场景。

此外，每个单段请求仍传入整份 `director`，增加了输入体积和无关上下文。但现有日志直接证明的是输出上限被触发，不能据此断言输入超过了模型上下文窗口。

### 现有日志不能证明的部分

- 截断调用的原始输出没有保存到 `model-results/`；只有成功通过 JSON/finish reason 检查的结果才落盘。因此无法确定原文断在哪一行、缺哪一个括号。
- 当前分支只要收到 `length` 就报告“未返回完整 JSON”，并没有单独保存这次 `json.Valid` 的结果；不能把界面文字当成对原始 JSON 语法的额外独立证据。
- 统计只保留输入/输出/总 token，没有保留推理 token 的细分，也没有保留模型思考正文。不能断言 10,000 token 全部是可见代码，也不能确认是否是内部推理耗尽预算。
- 没有核验该供应商路由支持的更高输出上限，不应直接承诺调到某个数值即可解决。

## 3. “历史估值对比”当前实际失败原因

该节点是 `F07/n_ead52812854138d6_1`，段落 `p6`，约 15.91 秒。

当前运行中，模型已在 **2026-11-09 09:05:32** 正常结束：

| 指标 | 实际值 |
|---|---|
| finish_reason | `stop` |
| prompt_tokens | 8,628 |
| completion_tokens | 9,278 |
| 保存的模型 JSON | 6,151 字节 |
| 场景数 | 2 |
| Canvas 代码长度 | 4,586 字符 |

该结果已通过 Go 层方案校验，保存为动效计划。09:29 和 09:33 的恢复复用了该计划，调用记录中没有新增此节点的模型请求。

最新 `material.log` 指向：

```text
Error: 专用动效源码不符合确定性 Canvas 绘制约定
    at validateMotionCode (/app/avatar-runtime/director-motion.mjs:12)
```

匹配到的实际代码片段：

```js
var src = fk('document', 0), val = fk('chart', 1), fin = fk('coin', 2);
```

`'document'` 在这里是字符串，表示文档图形类型；该类型本来就在动效元素允许清单中。它没有访问浏览器的 `document` 对象。

然而 [director-motion.mjs:12](../backend/avatar-runtime/director-motion.mjs#L12) 对整份源码做正则扫描，没有区分字符串、注释和可执行标识符。其禁词包含 `\bdocument\b`，导致误拦。

在正在部署的容器中直接调用 `validateMotionCode`，只做校验、不执行生成代码，得到以下复现结果：

| 输入 | 结果 |
|---|---|
| 保存的真实动效代码 | 被拒绝 |
| `var kind='document';ctx.fillRect(0,0,10,10);` | 被拒绝 |
| `var kind='chart';ctx.fillRect(0,0,10,10);` | 通过 |

所以，即使提高 token 上限或换成流式输出，这个完整结果仍会被现有校验器拒绝。修复应区分真实禁用访问与字符串内容，并保留沙箱隔离，不应简单取消安全检查。

历史补充：更早一次完整响应还出现过“元素标签不属于本句原文”的校验失败。这是另一类内容约束问题，与输出截断分开记录。

## 4. 为什么顶部错误看起来指向错误节点

当前数据库同时记录：

- `run.stage`：`F07/n_ead52812854138d6_1`（历史估值对比）。
- `run.error`：“模型输出被截断……”（来自先失败的金币堆叠素材）。
- “历史估值对比”的 `step.error`：“本地媒体步骤 material 执行失败，请查看任务日志”。

原因可以在代码中连起来：

1. [avatar_workflow_execute.go:197](../backend/internal/server/avatar_workflow_execute.go#L197) 在每个节点开始时更新 `s.Stage`。
2. [avatar_workflow_execute.go:617](../backend/internal/server/avatar_workflow_execute.go#L617) 遍历所有素材，遇到失败仍处理后面的素材，`materialErr` 只保留第一次错误。
3. 循环结束后，将第一次错误写到整个运行的 `Error`，而 `Stage` 已是最后执行的节点。
4. [workflowProgress.ts:58](../frontend/src/features/agent/workflowProgress.ts#L58) 根据 `run.stage` 取标题；[workflowProgress.ts:84](../frontend/src/features/agent/workflowProgress.ts#L84) 用 `run.error` 显示详情。

这会把两个节点的信息拼成一条提示。应绑定错误所属步骤，并在多个节点失败时逐项展示。

## 5. 是否需要分段传输或分段生成

| 方式 | 对这次问题的作用 |
|---|---|
| 流式传输 | 可显示进度、改善等待体验；同一个请求的 `max_tokens` 仍然有效，不能单独解决达到上限后的停止 |
| 按视频/素材拆请求 | 目前已经按素材调用；本次不是把整部视频的全部代码放在一次输出中 |
| 把单素材内部再拆成有边界的生成阶段 | 有帮助：先生成并校验小型场景方案，再按场景生成代码，独立保存与重试，最后统一组装 |
| 调整输出预算 | 可作为措施之一，但需核验供应商支持范围及推理/可见输出统计，不能代替分段和校验 |
| 让模型接着输出半截 JSON | 没有可靠结构边界时容易重复或破坏转义；本次失败的半截原文也未保存，不具备直接续传基础 |

建议的处理顺序：先修正错误归属与校验器误拦，让界面和执行链路可诊断；再为动效生成增加合适的输出预算、分阶段产物和按场景失败恢复。保存必要的输出统计、JSON 合法性和受控诊断产物，以区分输出上限、格式问题与渲染错误。

本报告只给出已验证原因与后续建议，不表示上述修复已经实施。
