# DT025 local candidate evidence — not completion

DT025 remains `in_progress`; 24/144 tasks are done. These are actual local
execution results, not a substitute for the required frozen-source CI chain.

- `farm-python-postgis.json`: fresh PostgreSQL 17/PostGIS, 81/81 passed, zero
  skips/errors, source stable and cluster shutdown exit 0. Exact 81-case reducer
  also passed (`farm-python-exact.json`). Includes real geometry and farm store
  migrations/RLS/revisions/replay tests.
- `isolation-postgis.json`: expanded 156/156 pytest bridge cases passed, race
  detector enabled. Platform matrix has 260 Go records and 229 HTTP observations
  across 30 routes; GIS has 20 routes. Other concurrent source edits mean this
  run is diagnostic rather than frozen-source evidence. It explicitly uses
  local auth-component mode. Consequently the full-CI reducer **correctly
  rejects it**, as retained in `isolation-ci-gate-local-rejection.json`.
- `farm-http-first-pass.json`: actual dual-database signed HTTP chain, root and
  eight subtests passed. Local auth-component mode is explicit. Later browser
  report hardening requires rerun on the final candidate.
- `farm-http-race-pass.json`: later real dual-PG HTTP run passes root plus eight
  children with the race detector, source stability and shutdown exit 0. The
  six signed withdrawal negatives now have real valid-before-revocation intent
  controls, including a genuinely pending operation produced by controlled
  transport failure. Subsequent GET-status response normalization is covered by
  separate response-contract tests and still requires the frozen CI rerun.
- Vue component/build and browser-runner unit results are retained separately.
  Browser-runner unit tests do not establish a successful browser integration.
  `vue-reconciliation.json` additionally verifies that 202 reconciliation stays
  unresolved and preserves the real operation reference, while non-completed
  200 responses are rejected.

Remaining gates: final source freeze; real Redis runtime, actual Chromium
forms/revocation, Flow adapter/legacy regression, exact source-verified CI and
independent review. No live farm data, commercial deployment or DT026 completion
is claimed. All source fingerprints in reports correspond to the stated test
execution, not automatically to later commits. Raw private logs stay outside Git.
