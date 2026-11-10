# Seedance 原声口播

2026-11-10：新项目改用 `speech_mode=native-video`。视频复刻功能仍待定，本次没有实现。

## 画布调整（2026-11-10，暂未部署）

按用户要求，以下调整仅保存在开发目录。当前用户正在测试已部署版本；本批次没有更新 WSL、重启容器或修改运行任务，待用户确认测试结束后再部署。

- 原声模式隐藏独立的第 6 步，文案确认直接连接原第 7 步，保留现有步骤编号。
- 第 7 步按段显示「数字人视频（含原声）」节点，生成时显示实际执行状态，视频就绪后直接播放原生音画。无需等待素材规划完成才显示视频。
- 素材规划完成后沿用同一个人物节点，继续显示已生成的视频，并开放原有提示词编辑；动效连接到相应的人物节点，不再显示一次额外的人物生成。
- 字幕对齐、时长检查以及错误仍显示在制作流程内。这里只修改前端投影和预览；后端 F04/F06 执行 ID、模型订单、缓存和复用顺序保持原有实现。
- 历史 TTS 任务保持原来的流程卡片，避免改变旧任务的审核方式。

本批次验证使用 Windows 本地单元测试、生产构建及隔离浏览器。`frontend/scripts/verify-native-workflow.mjs` 使用虚构任务和自制带音轨测试片，拦截所有 API 请求，检查画布、播放和规划前后的节点保留；不会访问已部署任务或调用模型。构建后在 frontend 目录运行 `node scripts/verify-native-workflow.mjs`（本机使用 Edge），证据位于 `backend/data/native-ui-offline/`。前端回归与构建日志分别为 `backend/data/native-ui-unit-tests.log` 和 `backend/data/native-ui-build.log`。

验证结果：Agent 前端 21 个测试文件、123 项测试全部通过；最终步骤编号文案调整后相关 25 项测试再次通过；完整前端生产构建通过。隔离浏览器确认视频能够播放、包含音轨且未静音，页面无脚本错误、无写入请求。

待测试结束后，本次画布改动只需更新前端：`deploy/wsl2/deploy.ps1 -Service frontend`；部署前仍需核对其他待发布修改，部署后再实测 WSL 访问地址。下文的“已部署”记录指此前的原声生成版本，不包含本节待部署的 UI 调整。

## 使用流程

1. 首页填写主题，选择人物照片、画幅和动效风格。首页原音色选择组件保留，但不显示，也不读取旧选择作为新任务声音。
2. 工作台「制作设置」可上传 2–30 秒、不超过 20MB 的参考音频；不选时使用视频模型的默认声音。选择或清空已有音频后保存设置。
3. 确认口播文案后，F04 把文案、人物照片和表演要求交给 Seedance 2.5，生成带原声的人物视频。新任务不经过独立配音确认。
4. 提取同一视频的原声并进行语音识别、字词对齐，取得实际时长，再规划真实素材、字幕、动效与镜头。F06 复用 F04 人物画面。
5. 镜头与成片审阅沿用现有流程。

## 接口与实现边界

- 使用 RunningHub 的 `/openapi/v2/bytedance/seedance-2.5-token/multimodal-video`，模型为 `seedance-2.5-token`，请求开启 `generateAudio`。声音和画面由同一次模型调用产生。
- 长文按标点分片，每片最多 80 个字符；提交时长只是 4–30 秒的请求预算，后期只使用视频返回后的实测时长。
- 用户上传的音频只作音色参考，不作为逐字驱动声音。未上传时，首片使用模型默认声音，后续片段提取首片/首段声音作为参考。音色一致性仍需实际审听，不能保证跨模型生成完全一致。
- 中间片段用 PCM 保存声音，合并后统一编码，防止分片累积 AAC 编码延迟。合成器播放从这段人物视频提取的声音，不重新配音或变速。
- 语音识别与对齐检查不等同于人工逐字审听。发现明显漏读、改词或无音轨时停止，保留原始视频和识别记录，不切回 TTS。
- 当前每段复用一个正面人物视频；布局、缩放、窗口和动效在后期完成，不把同一视频声称为多个新机位。

## 修改、重试和历史任务

- 改字幕、动效或素材规划不重新生成原声人物。
- 改文案、人物照片、表演提示词或参考音色会使对应原声人物失效，并重新测时、规划后续画面。重做首段默认声音也会使依赖它的后续段失效。
- 人物节点「重新生成」会同时更新声音与画面，完成原声后暂停，随后继续素材规划。其他素材节点仍沿用局部修改。
- 已取得供应商订单号的重试继续查询原订单；提交结果未知时不盲目重复下单。默认声音的首段失败后不生成依赖它的后续段。
- 已保存的历史 TTS/可灵任务保留恢复支持；独立 TTS 和旧音色组件没有删除。新建项目固定使用原声模式。
- 服务端配置和密钥仅保留于 WSL2，数据库、素材、历史订单与缓存按原部署保留。

## 验证范围

自动化测试覆盖默认声音/参考音频请求、原声步骤到人物复用、订单恢复、局部修改缓存、首页隐藏与工作台配置；FFmpeg 测试使用实际生成的测试音视频检查拼接时长及提取音轨一致性。模型生成效果、人物嘴型和声线自然度需另行实片审阅，自动化测试不代表模型效果验收。

本次结果：6 项原声服务测试、3 项原声领域测试、44 项相关前端测试、TypeScript 构建、4 项媒体测试通过。完整后端四个包共 724 项顶层测试通过；以下 6 组失败在修改前的 Git HEAD 隔离副本中得到相同结果，本次未改动对应业务：

- `TestContentMotionUsesAvatarFramesAndReusesValidatedPlanOnRenderRetry`
- `TestGeneralNarrationV3VersionsAndReplays`
- `TestFrameQualityGateWorkflowIdentityProviderFailureFinalizesFailedStep`
- `TestFrameWorkflowIdentityProviderFailureFinalizesWithoutReservation`
- `TestFrameWorkflowSucceededReplayProviderFailurePreservesSuccess`
- `TestTimelineToolWorkflowIdentityProviderFailureFinalizesWithoutReservation`

部署后浏览器验收命令：设置 `AVATAR_VERIFY_NATIVE_SPEECH=true`，运行 `node deploy/wsl2/verify-current-browser.cjs`。检查首页、登录、历史项目、新草稿原声模式、音频上传/保存/清空以及无效参考拒绝；临时草稿在检查结束时删除，不提交模型生成。

2026-11-10 已更新 Ubuntu-24.04 中的前后端 Docker 容器，数据库原卷保持运行。上述浏览器验收通过，无页面脚本错误、无模型任务提交；新项目 F04 服务配置检查通过。实测前端 `http://172.17.234.187:8080/` 和 `http://127.0.0.1:8080/` 返回 200；后端 `http://127.0.0.1:18000/health` 返回 200。WSL 重启后 IP 可能变化。验收证据保存在 `backend/data/native-verification-summary.json` 与 `backend/data/wsl-deployment/current/172.17.234.187/`。
