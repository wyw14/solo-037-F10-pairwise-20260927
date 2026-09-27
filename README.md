# WaveLab

WaveLab is a local HTTP service for tow-tank experiments. The original equipment, interlock and incidents pages expose a fixed commissioning **drill** scenario (now marked with an explicit drill banner). Operator measurements are a separate live data path served by the entry console at `/operations`.

## Operator entry console (`/operations`)

The operations page is a staging console for field entry:

- Runs and per-probe readings are staged locally in the browser (`localStorage`), grouped by run and probe, so a network outage never loses or duplicates a batch.
- Run IDs and frame IDs are generated on the client. While offline the console keeps accepting readings; once connectivity returns, the operator confirms uploads and the console syncs run-by-run in batches (25 frames per request).
- Every frame carries its client frame ID (`client_id`), so retried requests are idempotent: the server recognises retried frames and replies with a structured conflict instead of storing the reading twice. The console shows submitted vs. pending counts and can reconcile each conflict (take the server value, edit and re-stage as a new frame, or discard the local frame).
- "Refresh/reconcile" pulls server runs (read-only for runs not created locally) and checks locally submitted frames against the frames the server actually holds.
- Drill runs are flagged at creation and are visually distinct everywhere (amber DRILL markers, sorted after live runs); the flag is persisted and returned by the API and analysis.

## API

`POST /api/measured-runs` accepts `name`, `model_name`, `section`, `carriage`, optional `drill`, and an optional client-generated `id` (UUID) for runs created while offline. Re-posting the same `id` with identical metadata is idempotent; different metadata under the same `id` returns `409` with `reason: "run_content_mismatch"` and the existing run.

`POST /api/measured-runs/{id}/evidence` accepts one frame: `kind` (`wave_time` or `force`), `source_id`, finite `value`, `recorded_at` (RFC3339), and optional `client_id`. Retried or clashing frames return `409` with an `error` object:

- `duplicate_frame` — same `client_id`, identical content; safe idempotent retry, `existing` holds the stored evidence.
- `frame_content_mismatch` — same `client_id` was previously submitted with different kind/source/value/time.
- `duplicate_observation` — the same probe, kind and observation time were already recorded under a different frame ID.

`POST /api/measured-runs/{id}/evidence/batch` accepts `{"frames": [...]}` and always answers `200` for an existing run: `data.accepted` lists stored evidence, while `data.conflicts` reports per-`index` outcomes (including `invalid_frame` for frames that failed validation), so one bad frame never poisons the batch. Replaying a batch is fully idempotent.

`GET /api/measured-runs`, `GET /api/measured-runs/{id}`, and `GET /api/measured-runs/{id}/analysis` return the supplied records (including the `drill` flag and `client_id`) and the wave/force summary.

Set `WAVELAB_DATA_DIR` to choose the archive directory (default `data/wavelab`). Measurements are committed to `operator-measurements.json` (atomic temp-file rename) before they are published in memory, and are reloaded when the service restarts. The archive does not automatically grant a safety permit or drive hardware.

The `internal/probe`, `internal/force`, `internal/permit`, `internal/carriage`, `internal/track`, `internal/wave`, `internal/absorber`, and `internal/journal` packages provide component logic for further work. Their diagnostic use does not imply that the original HTTP pages accept real sensor streams.
