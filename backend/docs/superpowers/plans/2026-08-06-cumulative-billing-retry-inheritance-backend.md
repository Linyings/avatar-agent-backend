# 剪辑失败重试累计计费（后端）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 PC 端跨任务 ID 的重试可以继承上一次失败 run 的 pending 预留，最终成功时累计确认并只生成一条消费记录。

**Architecture:** 在现有 `inherit_reservations_from_run_id` 能力（commit 111faa2）之上，把 PC 端继承校验从“client_task_id 相等”放宽为“client_project_name 相等”，并让终态源 run 静默忽略、复用路径也执行迁移。

**Tech Stack:** Go + Gin + GORM + PostgreSQL（本地测试用 docker 的 narratoai-postgres）。

## Global Constraints

- 不修改数据库 schema，不修改 API 路径与错误码（`invalid_reservation_inheritance`、`workflow_run_not_found` 保持不变）。
- 计费语义不变：run 成功确认预留、取消/过期释放预留；确认时 `confirmCreditReservationsTx` 只生成一条 ledger 记录。
- `inherit_reservations_from_run_id` 保持可选字段；未传时行为与现状完全一致。
- 后端测试需要本地 Postgres：先确认 WSL 里的 docker 已启动（`narratoai-postgres` 监听 127.0.0.1:15432）。
- 提交信息遵循仓库现有风格（`feat:` / `fix:` 前缀）。

---

### Task 1: 放宽 PC 继承校验 + 终态 no-op + 修复复用路径迁移

**Files:**
- Modify: `internal/server/workflow_run_billing_test.go`（文件末尾追加 3 个测试）
- Modify: `internal/server/workflow_run_state_service.go`（`Create` 与 `loadWorkflowRunForReservationInheritance`）

**Interfaces:**
- Consumes: 现有测试辅助 `newWorkflowRunBillingFixture`、`createWorkflowBillingStep`、`workflowRunTestConsumption`、`assertWorkflowReservationStatus`、`assertWorkflowBillingCounts`（均在 `workflow_run_billing_test.go` 内）。
- Produces: `loadWorkflowRunForReservationInheritance` 对 PC 端改为按 `client_project_name` 校验；`Create` 中终态源 no-op、复用路径也执行 `migrateWorkflowRunReservationsTx` + `AfterCancel`。

- [ ] **Step 1: 写失败测试**

在 `internal/server/workflow_run_billing_test.go` 文件末尾追加以下三个测试函数：

```go
func TestWorkflowRunBillingRetryInheritsAcrossTaskIds(t *testing.T) {
	app, user, run, step := newWorkflowRunBillingFixture(t, "pc-inherit-cross-task@example.com", nil)
	if err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits on first run: %v", err)
	}

	// A retry of the same video uses a NEW client_task_id but the same client_project_name.
	newTaskID := "clip-source-" + uuid.NewString()
	replacement, err := app.WorkflowRuns().Create(context.Background(), CreateWorkflowRunInput{
		UserID:                       user.ID,
		ClientType:                   "pc",
		ClientTaskID:                 &newTaskID,
		ClientProjectName:            "source.mp4",
		InputHash:                    "workflow-input-" + uuid.NewString(),
		ClientInstanceID:             "billing-client",
		InheritReservationsFromRunID: &run.ID,
	})
	if err != nil {
		t.Fatalf("create replacement run: %v", err)
	}
	if replacement.Reused {
		t.Fatalf("replacement run was reused instead of created")
	}

	var stored models.WorkflowRun
	if err := app.db.First(&stored, "id = ?", run.ID).Error; err != nil {
		t.Fatalf("load cancelled run: %v", err)
	}
	if stored.Status != workflowRunStatusCancelled {
		t.Fatalf("cancelled run status = %q, want cancelled", stored.Status)
	}
	if stored.BillingStatus != workflowRunBillingStatusReleased {
		t.Fatalf("cancelled run billing status = %q, want released", stored.BillingStatus)
	}
	assertWorkflowReservationStatus(t, app, run.ID, creditReservationPending, 0)
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationPending, 1)

	// The replacement run bills its own execution; the total is cumulative.
	replacementStep := createWorkflowBillingStep(t, app, replacement.Run.ID, "generate-script", "script-input-retry")
	if err := app.billCredits(workflowRunTestConsumption(user.ID, replacement.Run.ID, replacementStep.ID, replacementStep.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits on replacement run: %v", err)
	}
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationPending, 2)

	if _, err := app.WorkflowRuns().Complete(context.Background(), user.ID, replacement.Run.ID, time.Now().UTC()); err != nil {
		t.Fatalf("complete replacement run: %v", err)
	}
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationConfirmed, 2)
	assertWorkflowBillingCounts(t, app, user.ID, 20, 2, 1)
}

func TestWorkflowRunBillingRetryInheritanceTerminalSourceNoop(t *testing.T) {
	app, user, run, step := newWorkflowRunBillingFixture(t, "pc-inherit-terminal@example.com", nil)
	if err := app.billCredits(workflowRunTestConsumption(user.ID, run.ID, step.ID, step.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits: %v", err)
	}
	if _, err := app.WorkflowRuns().Cancel(context.Background(), user.ID, run.ID); err != nil {
		t.Fatalf("cancel source run: %v", err)
	}
	assertWorkflowReservationStatus(t, app, run.ID, creditReservationReleased, 1)

	newTaskID := "clip-source-" + uuid.NewString()
	replacement, err := app.WorkflowRuns().Create(context.Background(), CreateWorkflowRunInput{
		UserID:                       user.ID,
		ClientType:                   "pc",
		ClientTaskID:                 &newTaskID,
		ClientProjectName:            "source.mp4",
		InputHash:                    "workflow-input-" + uuid.NewString(),
		ClientInstanceID:             "billing-client",
		InheritReservationsFromRunID: &run.ID,
	})
	if err != nil {
		t.Fatalf("create replacement run after terminal source: %v", err)
	}
	if replacement.Reused {
		t.Fatalf("replacement run was reused instead of created")
	}
	assertWorkflowReservationStatus(t, app, run.ID, creditReservationReleased, 1)
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationPending, 0)
}

func TestWorkflowRunBillingRetryInheritanceReusePathMigrates(t *testing.T) {
	app, user, run, _ := newWorkflowRunBillingFixture(t, "pc-inherit-reuse@example.com", nil)

	// Source run B is a separate non-terminal run with pending reservations.
	sourceTaskID := "clip-source-b-" + uuid.NewString()
	source, err := app.WorkflowRuns().Create(context.Background(), CreateWorkflowRunInput{
		UserID:            user.ID,
		ClientType:        "pc",
		ClientTaskID:      &sourceTaskID,
		ClientProjectName: "source.mp4",
		InputHash:         "workflow-input-" + uuid.NewString(),
		ClientInstanceID:  "billing-client",
	})
	if err != nil {
		t.Fatalf("create source run: %v", err)
	}
	sourceStep := createWorkflowBillingStep(t, app, source.Run.ID, "generate-script", "script-input-b")
	if err := app.billCredits(workflowRunTestConsumption(user.ID, source.Run.ID, sourceStep.ID, sourceStep.ExecutionKey, 10)); err != nil {
		t.Fatalf("reserve credits on source run: %v", err)
	}

	// The fixture run is still active for the original task id, so the new create reuses it.
	replacement, err := app.WorkflowRuns().Create(context.Background(), CreateWorkflowRunInput{
		UserID:                       user.ID,
		ClientType:                   "pc",
		ClientTaskID:                 run.ClientTaskID,
		ClientProjectName:            "source.mp4",
		InputHash:                    run.InputHash,
		ClientInstanceID:             "billing-client",
		InheritReservationsFromRunID: &source.Run.ID,
	})
	if err != nil {
		t.Fatalf("create replacement run: %v", err)
	}
	if !replacement.Reused {
		t.Fatalf("expected the active run to be reused")
	}
	var sourceStored models.WorkflowRun
	if err := app.db.First(&sourceStored, "id = ?", source.Run.ID).Error; err != nil {
		t.Fatalf("load source run: %v", err)
	}
	if sourceStored.Status != workflowRunStatusCancelled {
		t.Fatalf("source run status = %q, want cancelled", sourceStored.Status)
	}
	assertWorkflowReservationStatus(t, app, source.Run.ID, creditReservationPending, 0)
	assertWorkflowReservationStatus(t, app, replacement.Run.ID, creditReservationPending, 2)
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/server/ -run 'TestWorkflowRunBillingRetryInheritsAcrossTaskIds|TestWorkflowRunBillingRetryInheritanceTerminalSourceNoop|TestWorkflowRunBillingRetryInheritanceReusePathMigrates' -count=1 -v`

Expected:
- `TestWorkflowRunBillingRetryInheritsAcrossTaskIds` FAIL（`ErrWorkflowReservationInheritanceInvalid`，因为 task id 不相等）；
- `TestWorkflowRunBillingRetryInheritanceReusePathMigrates` FAIL（复用路径提前 return，源 run 预留仍为 pending 1）；
- `TestWorkflowRunBillingRetryInheritanceTerminalSourceNoop` PASS（终态源在旧代码下本来就是 no-op，作为回归保护）。

- [ ] **Step 3: 实现 `loadWorkflowRunForReservationInheritance` 放宽校验**

在 `internal/server/workflow_run_state_service.go` 的 `loadWorkflowRunForReservationInheritance` 中，把 `switch input.ClientType` 的 `case "pc":` 分支替换为：

```go
	case "pc":
		if strings.TrimSpace(run.ClientProjectName) == "" ||
			strings.TrimSpace(run.ClientProjectName) != strings.TrimSpace(input.ClientProjectName) {
			return models.WorkflowRun{}, ErrWorkflowReservationInheritanceInvalid
		}
```

删除原先对 `ClientTaskID` 相等的判断。`strings` 已在本文件导入，无需新增 import。

- [ ] **Step 4: 实现 `Create` 的终态 no-op 与复用路径迁移**

在 `internal/server/workflow_run_state_service.go` 的 `Create` 事务内，把从 `var inheritedSource *models.WorkflowRun` 到 `return nil` 的整段逻辑替换为：

```go
			var inheritedSource *models.WorkflowRun
			sourceSuperseded := false
			if input.InheritReservationsFromRunID != nil {
				source, err := loadWorkflowRunForReservationInheritance(tx, input)
				if err != nil {
					return err
				}
				if isWorkflowRunTerminal(source.Status) {
					// A terminal source has no pending reservations to inherit; ignore the request.
					inheritedSource = nil
				} else {
					inheritedSource = &source
					if isWorkflowRunActive(source.Status) {
						sourceSuperseded = true
						now := s.now()
						source.Status = workflowRunStatusCancelled
						source.CancelledAt = &now
						source.LeaseExpiresAt = nil
						if err := tx.Save(&source).Error; err != nil {
							return err
						}
					}
				}
			}

			var targetRun models.WorkflowRun

			active, err := loadActiveWorkflowRun(tx, input)
			if err == nil {
				if err := reuseWorkflowRun(tx, active, input, &result); err != nil {
					return err
				}
				targetRun = result.Run
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			} else {
				now := s.now()
				run := models.WorkflowRun{
					UserID:            input.UserID,
					ProjectID:         input.ProjectID,
					ClientType:        input.ClientType,
					ClientTaskID:      input.ClientTaskID,
					ClientProjectName: input.ClientProjectName,
					InputHash:         input.InputHash,
					Status:            workflowRunStatusRunning,
					BillingStatus:     workflowRunBillingStatusPending,
					ClientInstanceID:  input.ClientInstanceID,
					LastActiveAt:      now,
					ExpiresAt:         now.Add(workflowRunInactivityDuration),
				}
				if input.ClientInstanceID != "" {
					leaseExpiresAt := now.Add(workflowRunLeaseDuration)
					run.LeaseExpiresAt = &leaseExpiresAt
				}
				created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&run)
				if created.Error != nil {
					return created.Error
				}
				if created.RowsAffected == 0 {
					active, err = loadActiveWorkflowRun(tx, input)
					if err != nil {
						return err
					}
					if err := reuseWorkflowRun(tx, active, input, &result); err != nil {
						return err
					}
					targetRun = result.Run
				} else {
					targetRun = run
					result = CreateWorkflowRunResult{Run: run}
				}
			}

			if inheritedSource != nil {
				if err := migrateWorkflowRunReservationsTx(tx, inheritedSource.ID, targetRun.ID); err != nil {
					return err
				}
				if sourceSuperseded && s.hooks.AfterCancel != nil {
					if err := s.hooks.AfterCancel(ctx, tx, inheritedSource); err != nil {
						return err
					}
				}
			}
			return nil
```

要点：复用路径（`reuseWorkflowRun`）返回的 `result` 保留 `Reused: true`，只有真正新建 run 的分支才赋值 `result = CreateWorkflowRunResult{Run: run}`；迁移与 `AfterCancel` 在目标 run 确定后统一执行。

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/server/ -run 'TestWorkflowRunBillingRetry' -count=1 -v`

Expected: 全部 PASS（包含原有 `TestWorkflowRunBillingRetryInheritsPendingReservations`、`TestWorkflowRunBillingRetryInheritanceRejectsInvalidScope`、`TestWorkflowRunBillingRetryInheritsReservationsViaHTTP`）。

- [ ] **Step 6: 提交**

```bash
git add internal/server/workflow_run_state_service.go internal/server/workflow_run_billing_test.go
git commit -m "feat: allow cross-task credit reservation inheritance for pc retries"
```

---

### Task 2: HTTP 层跨任务继承覆盖 + 全量回归

**Files:**
- Modify: `internal/server/workflow_run_billing_test.go`（`TestWorkflowRunBillingRetryInheritsReservationsViaHTTP` 的 replacement 请求）

**Interfaces:**
- Consumes: Task 1 的后端行为；`request`、`decodeWorkflowRunResponse`、`registerAndLoginWithCredits`、`lookupUserByEmail`、`seedWorkflowRunBillingRate`（已存在）。
- Produces: HTTP 层确认“不同 client_task_id + 相同 client_project_name”也能继承。

- [ ] **Step 1: 修改 HTTP 测试的 replacement 请求**

在 `TestWorkflowRunBillingRetryInheritsReservationsViaHTTP` 中，把 replacement 请求里的 `"client_task_id": "http-task"` 改为 `"client_task_id": "http-task-retry"`（其余字段不动），使其覆盖“跨任务 ID 继承”：

```go
	replacement := request(app, http.MethodPost, "/api/v1/workflow-runs/", map[string]any{
		"client_type":                      "pc",
		"client_task_id":                   "http-task-retry",
		"client_project_name":              "source.mp4",
		"input_hash":                       "hash-retry",
		"client_instance_id":               "pc-client",
		"inherit_reservations_from_run_id": first.ID,
	}, token)
```

- [ ] **Step 2: 运行 HTTP 测试**

Run: `go test ./internal/server/ -run 'TestWorkflowRunBillingRetryInheritsReservationsViaHTTP' -count=1 -v`

Expected: PASS（201 创建新 run，`firstRunID` 的 pending 预留迁移到新 run）。

- [ ] **Step 3: 全量回归**

Run: `go test ./internal/server/ -count=1`

Expected: 全部 PASS，无其他测试受影响。

- [ ] **Step 4: 提交**

```bash
git add internal/server/workflow_run_billing_test.go
git commit -m "test: cover cross-task reservation inheritance over http"
```
