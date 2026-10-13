# P3 — Analysis & reports (implementation notes)

## Skills
- Skill packs are embedded in `backend/internal/flow/skillpacks/*.json`. Each skill has an id, version, method, steps, references and output_schema. Its hash is the sha256 of the canonical skill JSON.
- First-batch skills (`core` pack): `source-evaluation`, `cross-source-verification`, `change-analysis`, `periodic-brief`, `citation-integrity`.
- Industry pack `energy-storage` contains source templates, a glossary, the `storage-market-brief` skill, a report schema and eval samples.
- Install pins a version per workspace (`flow_skill_installs`, migration v53): `GET/POST /api/intelligence/skills[/install]` (install by `skillId` or a whole `packId`).
- `intel_load_skill` loads the installed skill. The first load in an agent run pins version and hash (`flow_skill_run_pins`), and later loads in the same run return the pinned version even if the workspace reinstalls. Research runs and reports store the `{id, version, hash}` skill ref.

## Engine (migration 0003)
- New tables: `flow_research_runs`, `flow_reports`, `flow_claims`, `flow_brief_previews`, `flow_brief_schedules`.
- Claim kinds are `source_statement`, `cross_source_verified` and `model_inference`.
- A report stores a citation map per claim (evidence id, document, source, revision, chunk, span, quote, truncated).
- `GET /v1/reports/:id` re-reads each cited chunk at the **cited revision**, so old reports keep resolving after documents are updated.
- `POST /v1/evidence/resolve` returns evidence visible to the caller with revision, chunk and truncation facts.
- `POST /v1/reports` is idempotent by key. It re-checks that every evidence id exists in the tenant/workspace at the cited revision and emits `report.completed`.
- `POST /v1/research-runs`:
  - single depth: a research run can't be a parent;
  - at most 3 per parent run, serialized per parent with an advisory lock;
  - fixed budget (8 documents, 400-char quotes, 8 claims) and fixed output `{claims, evidence_ids, coverage}`;
  - runs in-process and is extractive (no model call).
- Brief schedules:
  - preview (cron with 5 fields evaluated in an IANA timezone; next 3 runs shown), then apply (needs `approval_id` + idempotency key);
  - `Reports.briefTick` runs in the scheduler role and generates one report per (schedule, window), enforced by a unique index; missed windows collapse to the latest one;
  - coverage lists failed sources (latest collection run failed), truncation and revised documents (possible conflicting evidence), and the brief body states them.

## Platform
- Citation validation (`flow.ValidateCitations`, used by `intel_save_report` before anything reaches the engine) rejects:
  - unknown or foreign evidence (resolve is tenant/workspace scoped);
  - wrong revision or chunk;
  - documents outside the report's saved `material_scope`;
  - statements without citations;
  - `cross_source_verified` claims with fewer than 2 sources.
  Truncated evidence is allowed but flagged in `coverage.truncated_citations`.
- New tools:
  - `intel_load_skill` and `intel_preview_schedule` (read);
  - `intel_start_research` (parent = agent run id);
  - `intel_save_report`;
  - `intel_apply_schedule`: approval goes through the Pi chain with preview kind `intel_brief_schedule`.
- New scopes `intel.reports.write` and `intel.research` (admin/editor).
- A `report.completed` event creates exactly one in-app `flow_notifications` row (deduplicated by event id): `GET /api/intelligence/notifications`.
- Web: on `/intelligence`, a reports list and detail view (expandable evidence per claim, coverage warning, skill ref) and a brief schedules list.
