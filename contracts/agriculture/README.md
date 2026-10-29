# Agriculture v1 minimum machine contracts

Status: **W00 contract subset only**, not implemented routes or accepted product.
Source: `specs/agriculture/v1.0.0/docs/DATA_API_CONTRACTS.md`.

The schemas use JSON Schema 2020-12 and offline `$id` registration. The reserved
`.invalid` identifier domain is a namespace, never a network resolver. Reject
unknown schema versions and fields. This initial subset freezes delegation claims,
three event kinds, public artifact manifests, jobs/results, and NDVI-change input.
Other algorithms, events and endpoints require explicit schemas before use; they
are not implemented by substituting NDVI logic or unrestricted payload objects.

Delegation claims are **not authentication** by themselves. W01 must verify the
actual signature, configured issuer/key/audience, current authorization, resource
ownership, clock/TTL (default60s), replay receipts and canonical method/path/body
bindings. Write claims require operation_id and payload hash; read claims bind
method/path. No client request may introduce tenant_id, user_id, operator_code,
roles or direct object keys/URLs. IDs are opaque references, not authorization.

`sorted-json-v1` identifies a bounded canonical JSON protocol: UTF-8, sorted object
keys, no insignificant whitespace, no NaN/Infinity, no duplicate keys. Cross-language
number serialization must be frozen/tested before runtime use; this schema change
does not by itself implement canonical signing. Billing counters are bounded JSON
integers; monetary/decimal area values must not pass through binary float ledgers.
Event `payload_hash`, manifest hash and signature verification remain runtime
requirements; regex-format validation alone is not integrity verification. A
manifest never contains its own hash. Example hash values are explicit fixtures,
not measured production receipts.

`manifest.schema.json` is the **safe public projection**: artifact references,
object version, size/hash and spatial metadata; no object_key, bucket, token or
signed URL. Internal object locators require a separate restricted storage contract.
Expected ownership, CRS footprint order/range, lease monotonicity, time ordering,
scope membership and budget invariants require runtime/database checks beyond
JSON Schema. Public API bodies require the platform response envelope; internal
job snapshots are separately scoped by schema.

Examples contain only demo identifiers and fixed dates. Schema tests are contract
evidence, not PostGIS, signature validation, job scheduling or live API evidence.
Run with the GIS test venv plus pinned jsonschema4.26.0/referencing0.37.0:

```bash
.local/agri-venv/bin/python -m pytest -q tests/agriculture/test_contracts.py
```
