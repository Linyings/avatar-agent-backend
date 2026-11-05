# 电影解说稿充值后重试修复（2026-10-27）

原因：PC 的 movieWorkflowClient.prepare 在恢复失败请求时首先查询原步骤结果。积分不足使后端步骤成为 blocked / insufficient_credits，而旧 PC 仅恢复 not_started 或可重试的 failed/interrupted。即使本地账户余额已补充至 10036，查询仍返回旧阻塞错误，未进入 resume、也未调用模型。界面的“完整解说稿未完成”是通用标题，具体错误仍是 insufficient credits。

修复：后端 getMovieHarnessResult 对因积分不足阻塞、没有供应商提交认领、请求 ID、已存结果、任何积分预留记录的步骤返回 not_started / retryable=true。旧 PC 因此可以恢复原 run，再次通过正常积分校验提交。其余 blocked、未知状态和已有付费请求的保护逻辑保持原样。

验证：WSL / CGO 的 go test ./internal/server -run TestMovieHarness -count=1 通过。新增测试覆盖：缺积分被拒且模型未调用、未充值重试仍拒绝、直接调整本地余额后恢复同一任务并成功调用一次、完成结果再次读取不重复调用，以及有供应商请求/认领/结果时禁止自动重发。

本次只改动并更新本地后端；PC 无需重新打包，已完成的项目数据不变。未推送、未部署生产，也未触发真实付费模型调用。
