# Task 3 Cumulative Provider Finalization Fixes Report

## Scope

Implemented only backend workflow provider finalization, async submission persistence, affected server handlers, and focused server tests. No Web, PC, deployment, migrations, or model files changed. Existing work through `cff5550` was retained.

## Implemented Fixes

1. Blocked finalization now changes both the workflow step and workflow run to `blocked` in the same transaction. The existing activity update continues to preserve `CurrentStepID`, `LastActiveAt`, the 24-hour expiry, and pending reservations. The focused test also verifies a blocked run resumes to `running` and can finalize successfully.
2. Vision, Timeline, and Frame success handlers now translate an `ErrInsufficientCredits` reservation shortfall into a structured workflow `402`, after atomically blocking the run and step. This matches the existing TTS and ASR behavior and avoids a generic `500` with a running workflow.
3. Workflow provider finalization now requires positive billable usage: positive total tokens, character count, or duration. Zero/missing values become retryable `provider_protocol_error`, persist a failed step, and create no reservation. Legacy requests retain immediate billing behavior. Tests cover Vision, Timeline analysis, Scribe zero duration, and the shared finalizer's token/duration/character cases.
4. Async provider task persistence accepts an exact outstanding submission claim after a valid workflow resume transfers the run and step lease to a new client. Persistence validates user, workflow identity, nonterminal run state, running step state, provider input hash, and claim ID, while leaving the current lease owner untouched. Replaced claims and cancelled runs remain rejected.
5. Vision, Timeline, and Frame workflow provider paths now require a valid provider request ID before successful finalization. Missing or invalid IDs are persisted as retryable `provider_protocol_error` failures with no reservation. This remains opt-in, so legacy clients and providers without an ID contract, including TTS, are unchanged.

## Regression Coverage

- `workflow_provider_finalizer_test.go`: atomic blocked run/step update and rollback, resume after blocked, pending-reservation preservation, and token/duration/character zero billable usage with no reservation.
- `workflow_provider_submission_test.go`: exact claim persistence after lease takeover, stale claim rejection, and cancelled-run rejection.
- Vision, Timeline, and Frame workflow handler tests: post-provider final shortfall returns `402` with blocked run/step; missing provider trace ID fails without reservation.
- Vision and Timeline analysis tests: zero token usage fails with `provider_protocol_error` and no reservation.
- Scribe test: zero transcript duration fails with `provider_protocol_error` and no reservation.

## Red-Green Evidence

Focused regression tests were added before implementation. The initial final-shortfall handler run called the provider then returned the generic workflow `500` response instead of structured `402`; this was the expected red state. The initial blocked finalization test setup advanced time beyond the test lease, so its clock setup was corrected before implementation to reach the intended assertion. Focused tests then passed after the shared finalizer, handler, and submission changes.

## Verification

All commands ran through Ubuntu WSL using `narratoai-go-cgo:1.26.2` and `GOPROXY=https://goproxy.cn,direct`:

```text
go test ./internal/server -run FinalizeProvider -count=1
go test ./internal/server -run AllowsExactClaimAfterLeaseTakeover -count=1
go test ./internal/server -run PersistWorkflowProviderSubmissionAfterTakeover -count=1
go test ./internal/server -run WorkflowIdentityFinalInsufficientCreditsBlocksRunAndStep -count=1
go test ./internal/server -run WorkflowIdentityMissingProviderRequestIDFailsWithoutReservation -count=1
go test ./internal/server -run ZeroUsageFailsWithoutReservation -count=1
go test ./internal/server -run TestElevenLabsScribeWorkflowZeroDurationFailsWithoutReservation -count=1
go test ./internal/server -count=1
go test ./... -count=1
```

All listed verification commands completed successfully. Models and migrations were unchanged, so the conditional database-model test requirement did not apply; `go test ./... -count=1` nevertheless also completed `internal/database` successfully.
