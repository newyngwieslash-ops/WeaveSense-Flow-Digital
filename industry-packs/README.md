# Industry packs (§9.2)

Each directory is the reviewable export of one skill pack:

- `manifest.json`: id, version, kind, the frozen skill ids, versions and hashes, `required_tools`, `optional_connectors`, `report_schema`, `evaluation_set`
- `schemas/<skill>.json`: each skill's output schema. `schemas/report.json` is the pack's report section layout.
- `evals/evidence-cases.jsonl`: evaluation cases, one JSON object per line
- `glossary.json`, `source-templates.json`: present for every pack (core ships generic templates and a base glossary). The manifest `mcp_requirements` lists required MCP servers (education-math needs `math-service`; the other packs need none)

The runtime source of truth is `backend/internal/flow/skillpacks/<id>.json`. It is embedded into the platform binary, and each run freezes the skill hash.

Do not edit the files here by hand. Change the source and regenerate:

```sh
cd backend && go test ./internal/flow -run TestIndustryPackExport -update-packs
```

CI fails if this export drifts from the embedded packs.

The eval sets are review checklists for people and the model. No automated model-graded eval runner exists yet.

Chinese term aliases are separate: `services/loom-engine/flow/terms/zh-industry.json` holds the built-in set, and tenant-specific aliases go in `flow_term_aliases`.
