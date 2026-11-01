# DT025 inventory drift independent review

Conclusion: approve explicit refresh of the two generated snapshots for candidate `8a22a4cd`; no scanner, production or assertion weakening is needed. This is static inventory review, **not runtime permission/RLS/HTTP acceptance**.

## Exact comparison method

Compared the previous snapshots read with `git show 8a22a4cd:<path>` against `.local/audit/dt025-inventory-new.json` and `dt025-query-inventory-new.json`. The working-tree snapshots had already been refreshed by the parent, so comparing only working-tree files would incorrectly report no drift.

For each new inventory's 234 source entries, independently SHA256-hashed `git show 8a22a4cd:<source path>` bytes. Both inventories: **234 checked, zero mismatches**. No source change outside the frozen candidate was silently adopted.

## Identity inventory

- All 1,526 `identity_sites` are unchanged; no old caller-identity location was removed or reclassified.
- Declared routes stay 179; routes without caller identity stay 52.
- Two existing `main.py` declarations (health, signed ownership resolve) only shift lines +7 and add the `install_farm_routes` import. Function, HTTP method, path, direct identity parameters, schema identity fields and calls remain unchanged.
- Dynamic registration count 2 -> 4. Added registrations at `farms_routes.py:136` and `:244` expand only fixed collections: six `WORKSPACE_PATHS` POST actions and five literal business tuples. No caller-controlled path/method/handler registration.
- Workspace route handler `:75` calls `verify_request(WORKSPACE_TYP, action)` and `current_workspace`; mutation also claims replay and rechecks current authority before write. Business handler `:146` uses the existing `delegation_dependency` with exact farm path resource and closed action. Dynamic AST entries are not falsely counted as eleven independently statically verified routes; runtime route-set tests remain necessary.

## Query inventory

- Entries 417 -> 425. No prior `(path,function)` identity disappears.
- Three removed/replaced serialized records are the existing `validate_legacy_schema`, `migrate_schema`, and `verify_request` functions with line/hash changes, not deleted query boundaries.
- New records include two farm endpoint wrappers calling `store.execute`, `FarmStore.transaction`, `_record`, `_dictionary`, `execute`, ownership `resolve`, and `FarmAuthority.check_farm`.
- `FarmStore.transaction` (`farms_store.py:131-159`) requires a verified scope, sets **transaction-local** single tenant and workspace settings, applies statement/lock timeouts, and checks ticket expiry before commit. Actual create/update/archive, receipts, listing and details use this transaction path. `_record` and `_dictionary` receive the same scoped connection from execute; revision, command receipt and outbox writes share its transaction.
- Ownership `resolve` (`:563`) separately sets transaction-local tenant/workspace and restricts real active farm facts; it is reached through the signed service-only ownership route, not browser-provided identity.
- `prepare` performs bounded stateless PostGIS geometry measurement outside a long domain transaction. It does not use an unscoped connection to fetch farm business rows. `require_ready` is schema-boundary inspection, not business content access.
- The inventory's `check_farm` `self.client.stream` match is an authenticated fixed-platform HTTP callback, not an unscoped SQL access. `request.stream` is likewise body acquisition. Scanner labels are broad review markers and do not themselves prove ownership or authorization.

## Limitations retained

Static scanning does not resolve arbitrary dynamic routing or all asyncpg `fetch/fetchrow/fetchval` calls. The refresh neither removes that limitation nor claims completeness from the inventory counts. Real signed-identity route matrices, tenant/workspace RLS tests, the actual farm two-PG HTTP/browser chain and strict CI source provenance remain separate required evidence. No task status or runtime acceptance is changed by this review.
