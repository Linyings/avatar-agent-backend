# Narrative SSE Heartbeat Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep workflow-identified narrative-analysis SSE requests alive through long Provider calls without exposing model output before idempotent billing finalization.

**Architecture:** Migrate `frameWorkflowNarrativeAnalyze` to the serialized SSE emitter pattern already used by script streaming endpoints. Establish the stream and a 15-second comment heartbeat after validation, buffer Provider deltas for workflow-identified calls, finalize billing, then replay buffered events and complete.

**Tech Stack:** Go 1.24, Gin, `httptest`, GORM workflow-run services, Docker Compose, GitHub Actions, Alibaba Cloud ACR.

## Global Constraints

- Do not change reverse-proxy timeout, model selection, reasoning settings, database schema, or CreditReservation identity rules.
- SSE heartbeat is the comment `: heartbeat` and contains no user or media data.
- Workflow-identified Provider output remains hidden until `finalizeProviderSuccess` succeeds.
- The same `workflow_run_id + execution_key` remains resumable and bills at most once.
- Production rollback target is `v2.1.11`.

---

### Task 1: Narrative Stream Heartbeat and Billing Boundary

**Files:**
- Modify: `internal/server/handlers_system_tools_admin.go:2211-2383`
- Modify: `internal/server/handlers_stream_tool_workflow_identity_test.go`

**Interfaces:**
- Consumes: `beginSerializedToolEventStream(*gin.Context, string, string) *streamToolEmitter`
- Consumes: `startToolStreamHeartbeat(context.Context, *streamToolEmitter, time.Duration) func()`
- Consumes: `bufferedToolEmitter.emit`, `streamToolEmitter.emit`, `emitWorkflowToolStreamError`
- Produces: workflow-identified narrative SSE with immediate connection event, periodic comment heartbeat, post-finalization replay, and structured stream errors.

- [ ] **Step 1: Add a failing heartbeat test**

Add a narrative-specific handler test using a blocking Provider test server. Start the request in a goroutine, wait for the production 15-second heartbeat, and assert the response receives `: connected` and `: heartbeat` before releasing the Provider. Also assert the body does not contain Provider result events yet. Use a 20-second test deadline so the test fails deterministically without adding mutable production timing configuration.

```go
if !waitForStreamBody(recorder, ": heartbeat\n\n", time.Second) {
    t.Fatalf("narrative stream did not emit a heartbeat: %s", recorder.Body.String())
}
bodyBeforeProviderRelease := recorder.Body.String()
for _, forbidden := range []string{"event:thinking_delta", "event:answer_delta", "event:completed"} {
    if strings.Contains(bodyBeforeProviderRelease, forbidden) {
        t.Fatalf("provider output escaped before finalization: %s", bodyBeforeProviderRelease)
    }
}
```

- [ ] **Step 2: Run the new test and verify RED**

```text
go test ./internal/server -run TestFrameWorkflowNarrativeIdentityKeepsStreamAliveWithoutEarlyReplay -count=1 -v -timeout=30s
```

Expected: FAIL because the narrative handler does not establish an identity stream or start a heartbeat before Provider completion.

- [ ] **Step 3: Establish the serialized stream and heartbeat**

After execution and Credit checks succeed, initialize the emitter for every narrative request:

```go
emitter := beginSerializedToolEventStream(c, "frame_narrative_understanding", "正在流式理解剧情")
stopHeartbeat := startToolStreamHeartbeat(c.Request.Context(), emitter, streamToolHeartbeatInterval)
defer stopHeartbeat()

bufferedEmitter := &bufferedToolEmitter{}
emit := emitter.emit
if identity != nil {
    emit = bufferedEmitter.emit
}
```

Remove the delayed `beginToolEventStream`/`bufferedEmitter.replay` branch. Keep Provider `completed` in memory for identity requests, finalize first, then replay through the already-open emitter:

```go
for _, event := range bufferedEmitter.events {
    if err := emitter.emit(event.event, event.data); err != nil {
        return
    }
}
_ = emitter.flush()
emitter.write("completed", completedPayload)
```

- [ ] **Step 4: Stream structured errors after headers are committed**

For identity Provider failures, finalize the failed execution and call:

```go
emitWorkflowToolStreamError(c, emitter, newWorkflowProviderStreamError(providerErr))
```

For finalization failures, pass the existing `workflowRequestError` to `emitWorkflowToolStreamError`. Legacy requests flush pending data and emit a normal SSE `error` event.

- [ ] **Step 5: Verify GREEN and billing ordering**

```text
go test ./internal/server -run "TestFrameWorkflowNarrative|TestStreamToolHeartbeat|TestWorkflowIdentity" -count=1 -v
```

Expected: PASS. Heartbeat appears before Provider release, Provider output stays buffered, and completion appears only after workflow step and reservation finalization.

- [ ] **Step 6: Commit the implementation**

```text
git add internal/server/handlers_system_tools_admin.go internal/server/handlers_stream_tool_workflow_identity_test.go
git commit -m "fix(workflow): keep narrative streams alive"
```

---

### Task 2: Regression Verification and Release

**Files:**
- Verify: `.github/workflows/release-acr.yml`
- Deploy: `/root/narratoai-backend-go/.env` on production server.

**Interfaces:**
- Consumes: Task 1 commit on backend `main`.
- Produces: a new ACR image tag deployed through `BACKEND_IMAGE`, with production API healthy and rollback metadata preserved.

- [ ] **Step 1: Run formatting and complete backend verification**

```text
gofmt -w internal/server/handlers_system_tools_admin.go internal/server/handlers_stream_tool_workflow_identity_test.go
go test ./internal/server -count=1
go test ./... -count=1
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 2: Push backend main and a new patch tag**

Fetch tags and verify `v2.1.12` is unused. Never overwrite an existing tag.

```text
git push origin main
git fetch --tags origin
git tag v2.1.12
git push origin v2.1.12
```

Expected: `release-acr.yml` builds the matching ACR image.

- [ ] **Step 3: Verify release workflow before deployment**

Use GitHub Actions status or `gh run view` to confirm the tag workflow completed successfully. Do not deploy until the ACR build succeeds.

- [ ] **Step 4: Back up production configuration and deploy**

Over SSH in `/root/narratoai-backend-go`, create a timestamped `.env` backup, update only `BACKEND_IMAGE`, then run:

```text
docker compose pull api
docker compose up -d api
docker compose ps
curl -fsS http://127.0.0.1:8000/health
```

Expected: API and PostgreSQL are healthy and `/health` returns HTTP 200.

- [ ] **Step 5: Verify production without a paid workflow**

Confirm the public health endpoint returns 200, Electron-origin CORS preflight remains allowed, and startup logs contain no migration or configuration errors. Do not submit a real Provider request during automated deployment verification.

- [ ] **Step 6: User acceptance with the existing task**

Reopen the existing `5min.mp4` project and click Continue. Expected: the same workflow run resumes at narrative understanding, stays connected beyond two minutes, completes the step, and does not repeat completed local/media steps or duplicate the user's Credit charge.

- [ ] **Step 7: Roll back on failure**

Restore `BACKEND_IMAGE` to `v2.1.11`, run `docker compose up -d api`, and verify `/health`. Keep the source commit and published tag for diagnosis; do not rewrite published history.
