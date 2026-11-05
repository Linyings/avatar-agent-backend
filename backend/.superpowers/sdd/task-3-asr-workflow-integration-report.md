# Task 3 ASR Workflow Integration Report

## Outcome

Integrated workflow identity, durable Provider task submission state, workflow-based query resolution, and run-level ASR billing for ElevenLabs Scribe, Qwen ASR, and FunASR. Legacy requests retain the original in-memory submission, lookup, and billing paths.

## Red/Green Evidence

### Red

1. Added `handlers_asr_workflow_identity_test.go` before production edits. The first attempt to execute its focused test in `narratoai-go-cgo:1.26.2` did not reach assertions because the container timed out downloading modules from `proxy.golang.org`. Retrying with `GOPROXY=https://goproxy.cn,direct` made the image test environment usable.
2. During self-review, added a regression assertion requiring a failed workflow query to retain `asrTask_01JQ7Y8K9M2N4P6R8T0V3W5X7Z` and successfully retry without another submission. It failed as expected before the fix: the workflow step was `failed` with `ProviderRequestID: nil` after the Provider failure.

### Green

1. Updated workflow finalization to retain persisted asynchronous task IDs and input hashes on retryable Provider failure, and updated query refresh to reactivate failed/interrupted submitted steps.
2. Re-ran `TestVendorASRWorkflowQueryPendingFailureAndTaskMismatch`; it passed and demonstrated retrying the original provider task without another submission.
3. Focused vendor workflow tests and focused Scribe multipart workflow submission test pass.

## Changed Files

- `internal/server/handlers_system_tools_admin.go`
  - Parses complete workflow identity from Scribe multipart data, removes it from form values, fingerprints provider-relevant media/ASR input, preflights submission replays, persists opaque provider IDs, and finalizes Scribe workflow billing on successful query.
  - Parses JSON identity for Qwen/FunASR submission and query identity from URL values; provider input only receives sanitized business request data.
  - Uses persisted submission metadata for workflow query validation, pending activity refresh, final billing/finalization, structured Provider errors, and 402 blocking.
- `internal/server/workflow_provider_submission.go`
  - Allows workflow query callers to validate the stored input hash without resubmitting it in the URL.
  - Reactivates retryable failed/interrupted async submissions while retaining their original provider task IDs.
- `internal/server/workflow_provider_finalizer.go`
  - Retains provider task metadata for asynchronous submissions on retryable non-success finalization.
- `internal/server/handlers_asr_workflow_identity_test.go`
  - Focused tests for first submission, lost-response replay, changed-input conflict, pending query, task mismatch, provider failure, retrying the same task ID, finalization idempotency, partial/invalid identity, 402 precheck blocking, legacy compatibility, metadata secrecy, and Scribe multipart identity stripping/persistence.

## Commands and Results

All commands were run in WSL through image `narratoai-go-cgo:1.26.2` with `GOPROXY=https://goproxy.cn,direct`.

```text
go test ./internal/server -run TestVendorASRWorkflow -count=1
PASS (2.749s)

go test ./internal/server -run TestElevenLabsScribeWorkflowMultipartIdentityPersistsSubmission -count=1
PASS (0.292s)

go test ./internal/server -run TestVendorASRWorkflowQueryPendingFailureAndTaskMismatch -count=1
PASS (1.808s)

go test ./internal/server -count=1
PASS (70.124s)

go test ./... -count=1
PASS (75.3s; internal/server 70.630s)
```

## Self-Review

- Workflow task IDs are persisted only through the existing opaque 16-128 identifier sanitizer.
- Provider input fingerprints are SHA256 hashes. No URL, filename, local path, media bytes, transcript, or provider transcript result is stored in the workflow step or credit reservation metadata.
- Query paths no longer depend on the legacy Scribe or vendor in-memory maps when workflow identity is supplied.
- Successful finalization uses the existing idempotent reservation path; repeated workflow queries keep one run-level reservation.
- `streamTool` behavior was not changed.

## Concerns

- The initial test-first baseline was delayed by Go module download timeouts; the later retry regression did execute and fail before its fix. This report records that distinction explicitly.
- The focused ASR handler suite clears the legacy in-memory billing map to demonstrate workflow queries do not depend on it. Durable provider-query restart behavior is also covered by the existing `TestResolveWorkflowProviderQuerySurvivesDatabaseRestart` helper test; this task did not add a separate HTTP-handler process-restart fixture.

## Task Review Fixes (2026-07-15)

### Findings Addressed

1. DashScope workflow queries no longer treat Provider task status `SUCCEEDED` as sufficient for workflow success. The normalized response must also have `success: true` and contain a fetched transcript artifact. A missing/failed transcript result now becomes a structured retryable `provider_unavailable` response, leaves zero reservations, and retains the original Provider task ID/input hash for retry.
2. ElevenLabs Scribe workflow pending responses now use the configured Scribe model (falling back to `scribe_v2` consistently with the Provider client). Legacy pending responses continue to use the model stored in the legacy in-memory job.
3. Scribe workflow handler tests now cover persisted query behavior after clearing both `elevenLabsScribeJobs` and `elevenLabsScribeBilled`: pending, successful finalization, repeated query idempotency with one reservation, retryable Provider failure, and successful retry using the original task ID.

### Red Evidence

```text
go test ./internal/server -run TestVendorASRWorkflowSucceededWithoutTranscriptFailsWithoutReservation -count=1
FAIL: status = 200, want 502
body normalized to status SUCCEEDED with success=false because transcription_url was empty

go test ./internal/server -run TestElevenLabsScribeWorkflowPendingUsesConfiguredModelWithoutLegacyJob -count=1
FAIL: pending workflow response model was "", want "scribe-workflow-model"
```

### Fix Verification

All commands ran in WSL image `narratoai-go-cgo:1.26.2` with `GOPROXY=https://goproxy.cn,direct` and `GIN_MODE=release`.

```text
go test ./internal/server -run TestVendorASRWorkflow -count=1
PASS (2.916s)

go test ./internal/server -run TestElevenLabsScribe -count=1
PASS (1.752s)

go test ./internal/server -count=1
PASS (76.051s)

go test ./... -count=1
PASS (internal/server 76.788s; all packages passed)
```

### Exact Restart/Map Coverage

The Scribe handler tests use the same persisted workflow database state while explicitly replacing both legacy maps with empty `sync.Map` values before workflow queries. They then query only with persisted workflow identity and Provider task metadata. This proves the HTTP handler workflow branch does not depend on process-local job or billing maps. A second `App` is not constructed in this fixture because `newTestApp` uses an isolated in-memory database; database-close/reopen persistence remains covered by `TestResolveWorkflowProviderQuerySurvivesDatabaseRestart` at the shared provider-submission layer.

### Review Notes

- No `streamTool` code changed.
- Legacy Scribe pending behavior has a dedicated regression test.
- Missing transcript artifacts create no workflow reservation and preserve async submission metadata for retry.

## Durable Provider Submission Claim Fix (2026-07-15)

### Red Evidence

```text
go test ./internal/server -run TestClaimWorkflowProviderSubmissionSerializesAndRequiresOwnership -count=1 -v -timeout 90s
FAIL: persisted claimed submission retained ProviderSubmissionClaimedAt in the returned step after the database value was cleared.

go test ./internal/server -run TestClaimWorkflowProviderSubmissionStaleTakeoverReleaseAndReplay -count=1 -v -timeout 90s
FAIL: a claim was incorrectly eligible for takeover exactly at workflowRunLeaseDuration.
```

### Fix Verification

All commands ran in WSL image `narratoai-go-cgo:1.26.2` with `GOPROXY=https://goproxy.cn,direct` and `GIN_MODE=release`.

```text
go test ./internal/server -run TestClaimWorkflowProviderSubmission -count=1 -v -timeout 90s
PASS (0.590s)

go test ./internal/server -run TestVendorASRWorkflow -count=1 -v -timeout 90s
PASS (4.944s)

go test ./internal/server -run TestElevenLabsScribeWorkflow -count=1 -v -timeout 90s
PASS (1.683s)

go test ./internal/database -count=1 -timeout 90s
PASS (0.063s)

go test ./internal/server -count=1
PASS (75.191s)

go test ./... -count=1
PASS (internal/server 77.349s; all packages passed)
```

### Review Notes

- Claims use generated UUIDs and record their claim timestamp atomically with the provider input hash.
- An active same-input claim returns retryable `workflow_provider_submission_in_progress`; changed input returns `workflow_input_mismatch`.
- A stale claim is replaceable only after the lease duration. Persist and release require the exact claim ID, and persist clears both claim fields.
- Scribe and DashScope claim before submitting to their Provider, replay an already stored task without a Provider call, and release their own claim after submission failures.
- No `streamTool` code changed.
