# Project Workflow Migration Runbook

This runbook migrates legacy `projects` JSON workflow fields into the canonical workflow step tables without deleting user data.

## Scope

Legacy source columns:

- `projects.video_url`
- `projects.audio_url`
- `projects.video_context`
- `projects.subtitle`
- `projects.video_config`
- `projects.ai_analysis_script`

Canonical destination tables:

- `project_video_sources`
- `project_subtitles`
- `project_generation_configs`
- `project_scripts`
- `project_voiceovers`

## Safety Properties

- The migration is additive.
- Legacy `projects` JSON columns are not dropped or overwritten.
- Re-running the migration is idempotent; an active step row is not duplicated.
- Rollback can switch the application read path back to legacy `projects` columns because source data remains intact.
- `workflow_migrated_at` only marks projects that had legacy workflow data.

## Preflight

1. Take a production database snapshot.
2. Deploy the expand migration:

   ```bash
   psql "$DATABASE_URL" -f scripts/migrations/20260428_project_workflow_expand.sql
   ```

3. Confirm the app version still supports legacy fallback reads.
4. Run dry-run:

   ```bash
   go run ./cmd/workflow-migrate --dry-run
   ```

5. Dry-run must show expected `pending_insert` counts and no command error.

## Apply

Run:

```bash
go run ./cmd/workflow-migrate --apply
```

The command prints JSON with per-step counts:

- `legacy_candidates`: projects with non-empty legacy data for that step.
- `existing_active`: projects already covered by an active workflow row.
- `pending_insert`: rows the migration intends to create.
- `inserted`: rows actually inserted during apply.
- `missing_after`: only used by verify.

## Verify

Run:

```bash
go run ./cmd/workflow-migrate --verify-only
```

Success criteria:

- command exits `0`
- every step has `missing_after: 0`
- application smoke test can load old and new projects through `/api/v1/projects/:project_id/workflow`

- every `legacy_without_active_step` count is `0`
- `workflow_event_count` is increasing when canonical saves or conflicts happen

Deprecated workflow writes through `PUT /api/v1/projects/:project_id` are rejected with HTTP `410`.
Rejections are counted in `legacy_project_update_rejections` and emitted to
`project_workflow_events` as `legacy_workflow_write_rejected`.

For a repeatable local or CI smoke gate, run:

```bash
scripts/workflow_production_acceptance.sh
```

Set `RUN_DB_CHECKS=1` when `DATABASE_URL` points at a disposable clone or a production-readiness
environment where migration dry-run and verify-only are safe to execute.

## Recovery

If apply fails:

1. Keep the application on the legacy-compatible build.
2. Do not drop workflow tables.
3. Re-run `--verify-only` to identify missing steps.
4. Re-run `--apply`; idempotence skips already active rows.

If a post-deploy incident requires rollback:

1. Roll application back to the previous legacy-read build or disable workflow-first reads.
2. Keep all workflow tables in place.
3. Use the database snapshot only if legacy source columns were modified by another operation; this migration itself does not remove user data.

## Final Cutover Checklist

- `go test ./...` passed.
- `npm run build` and `npm run lint` passed.
- `go run ./cmd/workflow-migrate --verify-only` exits `0`.
- On-call has this runbook and the latest database snapshot id.
