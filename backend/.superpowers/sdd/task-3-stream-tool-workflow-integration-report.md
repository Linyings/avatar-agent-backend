# Task 3: Stream Tool Workflow Integration Report

## Scope

Reviewed the interrupted implementation against the task brief for the shared `streamTool` handler and its SSE emitter. The review stayed within the owned handler, focused stream-tool tests, and this report. No concrete timeline or frame handler was changed.

## Requirement Review

- Legacy requests remain on the immediate billing path and retain the existing SSE shape.
- Workflow identity is extracted before provider setup, all four identity fields are stripped from the request map, and partial or invalid identity is rejected before any provider call.
- Workflow execution is resolved only after model configuration is confirmed. The estimated credit precheck runs before opening the provider stream; insufficient precheck balance returns HTTP 402, emits the structured workflow error contract, and marks the step blocked.
- Workflow provider events are buffered. Usage and provider request ID are required, then `finalizeProviderSuccess` persists the step and one run-level reservation before buffered events and the terminal `completed` event are replayed.
- Missing usage is classified as retryable `provider_usage_missing` and does not reserve credits.
- Succeeded-step replays may call the provider again. A replay provider failure skips non-success finalization when the step is already succeeded, preserving the original step and reservation.
- Provider failures finalize running steps with sanitized retryable diagnostics. Provider response bodies are not sent to workflow SSE clients.
- Final reservation failures after SSE headers are open are emitted as structured SSE errors containing `request_id`, `code`, `message`, and `retryable`; insufficient final balance marks the step blocked.
- The emitter serializes connected/stage output, provider output, heartbeat comments, replay, terminal events, errors, and flushes through one mutex-protected writer.
- Heartbeats are SSE comments on a 15-second ticker and the stop function now waits for the ticker goroutine to exit on completion or cancellation.
- `OpenAIClient.Stream` continues forcing `stream_options.include_usage=true` and removes `stream_options` from prompt construction. Workflow identity is removed by the handler before prompt construction.
- Persisted workflow metadata and structured workflow errors contain only sanitized identifiers/diagnostics, not prompt or generated-content fields.

## Red Evidence

1. Added `TestStreamToolWorkflowUnavailableProviderDoesNotStartStep` and ran:

   `wsl.exe -d Ubuntu -- bash -lc 'cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience && docker run --rm -v /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience:/workspace -w /workspace -e GOPROXY=https://goproxy.cn,direct narratoai-go-cgo:1.26.2 go test ./internal/server -run TestStreamToolWorkflowUnavailableProviderDoesNotStartStep -count=1'`

   Failed as expected: HTTP 503 was returned, but the unconfigured provider had already created a workflow step with status `running`.

2. Ran the heartbeat lifecycle test before the fix:

   `wsl.exe -d Ubuntu -- bash -lc 'cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience && docker run --rm -v /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience:/workspace -w /workspace -e GOPROXY=https://goproxy.cn,direct narratoai-go-cgo:1.26.2 go test ./internal/server -run TestToolStreamHeartbeat -count=1'`

   Failed as expected: `heartbeat count grew after stop: got 2, want 1`.

## Fixes and Green Evidence

- Moved the `ConfiguredFor` gate before workflow execution resolution so configuration failures do not create or strand a running step.
- Made heartbeat shutdown synchronous and idempotent by waiting for the ticker goroutine to close its completion channel.
- Focused workflow tests:

  `wsl.exe -d Ubuntu -- bash -lc 'cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience && docker run --rm -v /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience:/workspace -w /workspace -e GOPROXY=https://goproxy.cn,direct narratoai-go-cgo:1.26.2 go test ./internal/server -run TestStreamTool -count=1'`

  Result: `ok github.com/narratoai/backend-go/internal/server 7.234s`.

- Heartbeat lifecycle tests:

  `wsl.exe -d Ubuntu -- bash -lc 'cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience && docker run --rm -v /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience:/workspace -w /workspace -e GOPROXY=https://goproxy.cn,direct narratoai-go-cgo:1.26.2 go test ./internal/server -run TestToolStreamHeartbeat -count=1'`

  Result: `ok github.com/narratoai/backend-go/internal/server 0.032s`.

- Race check:

  `wsl.exe -d Ubuntu -- bash -lc 'cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience && docker run --rm -v /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience:/workspace -w /workspace -e GOPROXY=https://goproxy.cn,direct narratoai-go-cgo:1.26.2 go test -race ./internal/server -run TestStreamToolEmitterSerializesConcurrentHeartbeatAndProviderWrites -count=1'`

  Result: `ok github.com/narratoai/backend-go/internal/server 1.033s`.

- Required server suite:

  `wsl.exe -d Ubuntu -- bash -lc 'cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience && docker run --rm -v /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience:/workspace -w /workspace -e GOPROXY=https://goproxy.cn,direct narratoai-go-cgo:1.26.2 go test ./internal/server -count=1'`

  Result: `ok github.com/narratoai/backend-go/internal/server 103.583s`.

- Required repository suite:

  `wsl.exe -d Ubuntu -- bash -lc 'cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience && docker run --rm -v /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience:/workspace -w /workspace -e GOPROXY=https://goproxy.cn,direct narratoai-go-cgo:1.26.2 go test ./... -count=1'`

  Result: all packages passed, including `internal/server 101.043s`.

- `gofmt -w internal/server/handlers_system_tools_admin.go internal/server/handlers_stream_tool_workflow_identity_test.go` completed successfully.
- `git diff --check` passed.
- Docker test runs used `--rm`; the post-test `docker ps` and image-filtered `docker ps -a` checks returned no containers.

## Changed Files

- `internal/server/handlers_system_tools_admin.go`
  - Added serialized stream emission and comment heartbeat support.
  - Integrated workflow identity extraction, precheck, buffered finalization, replay, structured errors, and legacy billing preservation into `streamTool`.
  - Prevented unconfigured workflow requests from starting steps.
  - Made heartbeat shutdown synchronous.
- `internal/server/handlers_stream_tool_workflow_identity_test.go`
  - Added endpoint coverage for script, draft, and match stream tools, including success, identity rejection, billing/finalization, provider failure, replay preservation, insufficient credits, legacy compatibility, prompt leakage, serialization, heartbeat lifecycle, and provider configuration ordering.
- `.superpowers/sdd/task-3-stream-tool-workflow-integration-report.md`
  - This report.

## Self-Review and Concerns

- The focused tests use an in-process HTTP provider that emits OpenAI-compatible SSE; they do not exercise a real remote provider or proxy timeout.
- The race check covers the shared emitter write path. The full suites were run without `-race` as requested.
- Legacy provider error SSE remains compatibility-shaped and is not converted to the workflow structured error contract. Workflow requests use sanitized structured errors; changing legacy error text would broaden the compatibility impact.
- No unrelated handlers were modified.

## Provider Request ID Follow-Up

Workflow stream success now requires both nonzero usage and a sanitized, non-empty Provider request ID before `finalizeProviderSuccess` is called. Missing or invalid request IDs are classified as retryable `provider_protocol_error`; a running step is finalized as failed, no reservation is created, and buffered output is not replayed through `completed`. The validation is workflow-only, so legacy stream completion and immediate billing remain unchanged.

### Red Evidence

Command:

`wsl.exe -d Ubuntu -- bash -lc 'cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience && docker run --rm -v /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience:/workspace -w /workspace -e GOPROXY=https://goproxy.cn,direct narratoai-go-cgo:1.26.2 go test ./internal/server -run TestStreamToolWorkflowMissingProviderRequestIDFailsWithoutReservationOrCompletedEvent -count=1'`

Result: failed as expected. The response contained `event:completed` with `"request_id":""`, and the handler created a workflow credit reservation instead of emitting `provider_protocol_error`.

### Green Evidence

- Focused missing-ID regression:

  `wsl.exe -d Ubuntu -- bash -lc 'cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience && docker run --rm -v /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience:/workspace -w /workspace -e GOPROXY=https://goproxy.cn,direct narratoai-go-cgo:1.26.2 go test ./internal/server -run TestStreamToolWorkflowMissingProviderRequestIDFailsWithoutReservationOrCompletedEvent -count=1'`

  Result: `ok github.com/narratoai/backend-go/internal/server 0.562s`.

- Focused stream workflow group:

  `wsl.exe -d Ubuntu -- bash -lc 'cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience && docker run --rm -v /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience:/workspace -w /workspace -e GOPROXY=https://goproxy.cn,direct narratoai-go-cgo:1.26.2 go test ./internal/server -run TestStreamTool -count=1'`

  Result: `ok github.com/narratoai/backend-go/internal/server 8.632s`.

- Required server suite:

  `wsl.exe -d Ubuntu -- bash -lc 'cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience && docker run --rm -v /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience:/workspace -w /workspace -e GOPROXY=https://goproxy.cn,direct -e GIN_MODE=release narratoai-go-cgo:1.26.2 go test ./internal/server -count=1'`

  Result: `ok github.com/narratoai/backend-go/internal/server 113.954s`.

  An immediately preceding run without `GIN_MODE=release` exited 1, but its assertion was truncated by Gin debug output and the failure did not reproduce on the clean rerun above.

- Required repository suite:

  `wsl.exe -d Ubuntu -- bash -lc 'cd /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience && docker run --rm -v /mnt/d/NarratoAI/.worktrees/NarratoAI-SaaS-B-Go-workflow-resilience:/workspace -w /workspace -e GOPROXY=https://goproxy.cn,direct -e GIN_MODE=release narratoai-go-cgo:1.26.2 go test ./... -count=1'`

  Result: all packages passed, including `internal/server 114.519s`.
