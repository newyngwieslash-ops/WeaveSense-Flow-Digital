"""Strict catalog claims; an uploaded declaration is never rights approval."""

import copy
import hashlib
import re
from datetime import UTC, datetime

from .identity.canonical import canonicalize
from .identity.verify import IdentityError, require

KINDS = frozenset({"raster", "vector", "table", "photo", "audio", "model"})
SOURCE_KINDS = frozenset({"customer_data", "public_imagery", "third_party"})
USES = frozenset(
    {
        "ingest",
        "analysis",
        "report_publish",
        "export",
        "external_share",
        "model_training",
    }
)
SENSITIVITIES = frozenset({"public", "internal", "confidential", "restricted"})
REFERENCE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._:-]{0,127}")
FARM_ID = re.compile(r"farm_[0-9a-f]{32}")
ASSET_ID = re.compile(r"asset_[0-9a-f]{32}")
STAMP = re.compile(
    r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,6})?(?:Z|[+-]\d{2}:\d{2})"
)
METADATA_FIELDS = frozenset(
    {
        "name",
        "kind",
        "data_class",
        "farm_id",
        "source",
        "rights_declaration",
        "sensitivity",
        "retention",
    }
)


def digest(value):
    import json

    return hashlib.sha256(
        canonicalize(json.dumps(value, ensure_ascii=False).encode())
    ).hexdigest()


def text(value, maximum, *, nullable=False, reference=False):
    if value is None and nullable:
        return
    require(
        isinstance(value, str)
        and 1 <= len(value) <= maximum
        and value.strip() == value
        and all(ord(c) >= 32 and ord(c) != 127 for c in value),
        "INVALID_ARGUMENT",
        400,
    )
    if reference:
        require(REFERENCE.fullmatch(value) is not None, "INVALID_ARGUMENT", 400)


def timestamp(value, *, nullable=True):
    if value is None and nullable:
        return None
    require(
        isinstance(value, str) and STAMP.fullmatch(value) is not None,
        "INVALID_ARGUMENT",
        400,
    )
    try:
        return datetime.fromisoformat(value.replace("Z", "+00:00")).astimezone(UTC)
    except ValueError:
        raise IdentityError("INVALID_ARGUMENT", 400) from None


def exact(value, fields):
    require(
        isinstance(value, dict) and set(value) == set(fields), "INVALID_ARGUMENT", 400
    )


def validate_metadata(value):
    exact(value, METADATA_FIELDS)
    text(value["name"], 200)
    require(
        isinstance(value["kind"], str)
        and value["kind"] in KINDS
        and isinstance(value["data_class"], str)
        and value["data_class"] in {"demo", "operational"}
        and isinstance(value["sensitivity"], str)
        and value["sensitivity"] in SENSITIVITIES,
        "INVALID_ARGUMENT",
        400,
    )
    require(
        value["farm_id"] is None
        or (isinstance(value["farm_id"], str) and FARM_ID.fullmatch(value["farm_id"])),
        "INVALID_ARGUMENT",
        400,
    )
    source = value["source"]
    exact(source, {"kind", "source_id", "reference"})
    require(
        isinstance(source["kind"], str) and source["kind"] in SOURCE_KINDS,
        "INVALID_ARGUMENT",
        400,
    )
    text(source["source_id"], 128, nullable=True, reference=True)
    text(source["reference"], 128, reference=True)
    rights = value["rights_declaration"]
    exact(
        rights,
        {
            "owner",
            "license_ref",
            "permitted_uses",
            "attribution",
            "valid_from",
            "expiry",
        },
    )
    text(rights["owner"], 200, nullable=True)
    text(rights["license_ref"], 128, nullable=True, reference=True)
    uses = rights["permitted_uses"]
    require(
        isinstance(uses, list)
        and len(uses) <= len(USES)
        and all(isinstance(use, str) and use in USES for use in uses)
        and len(set(uses)) == len(uses),
        "INVALID_ARGUMENT",
        400,
    )
    exact(rights["attribution"], {"required", "text"})
    require(type(rights["attribution"]["required"]) is bool, "INVALID_ARGUMENT", 400)
    text(rights["attribution"]["text"], 2048, nullable=True)
    start, end = timestamp(rights["valid_from"]), timestamp(rights["expiry"])
    require(start is None or end is None or end > start, "INVALID_ARGUMENT", 400)
    exact(value["retention"], {"retain_until", "legal_hold"})
    timestamp(value["retention"]["retain_until"])
    require(type(value["retention"]["legal_hold"]) is bool, "INVALID_ARGUMENT", 400)
    result = copy.deepcopy(value)
    result["rights_declaration"]["permitted_uses"].sort()
    return result


def validate_successor(previous, value):
    """Versioning never changes identity/class or releases retention obligations."""
    value = validate_metadata(value)
    require(
        all(previous[key] == value[key] for key in ("kind", "farm_id", "data_class")),
        "IMMUTABLE_ASSET_SCOPE",
        409,
    )
    old, new = previous["retention"], value["retention"]
    require(
        not old["legal_hold"] or new["legal_hold"], "RETENTION_CANNOT_BE_REDUCED", 409
    )
    before, after = timestamp(old["retain_until"]), timestamp(new["retain_until"])
    require(
        after is None or (before is not None and after >= before),
        "RETENTION_CANNOT_BE_REDUCED",
        409,
    )
    levels = {"public": 0, "internal": 1, "confidential": 2, "restricted": 3}
    require(
        levels[value["sensitivity"]] >= levels[previous["sensitivity"]],
        "SENSITIVITY_CANNOT_BE_REDUCED",
        409,
    )
    return value


class AssetCatalogRights:
    """Operator-composed adapter around the EXISTING W00 rights engine.

    Loaders/functions are configuration, never request fields. Scope records are
    an exact per-source list of tenant/workspace/source_kind admissions. No '*'
    wildcard or inference from a URL, repository license or owning tenant.
    """

    def __init__(
        self,
        registry_loader=None,
        scope_loader=None,
        authorize=None,
        validate_registry=None,
    ):
        self.registry_loader = registry_loader
        self.scope_loader = scope_loader
        self.authorize = authorize
        self.validate_registry = validate_registry

    def _reviewed(self, tenant, workspace, metadata):
        source_id = metadata["source"]["source_id"]
        if source_id is None:
            return None
        require(
            all(
                callable(fn)
                for fn in (
                    self.registry_loader,
                    self.scope_loader,
                    self.authorize,
                    self.validate_registry,
                )
            ),
            "SOURCE_NOT_AVAILABLE",
            404,
        )
        try:
            registry = self.registry_loader()
            sources = self.validate_registry(registry)
            scopes = self.scope_loader()
            require(source_id in sources, "SOURCE_NOT_AVAILABLE", 404)
            visited = set()

            def admit(selected):
                if selected in visited:
                    return
                grants = scopes.get(selected, []) if isinstance(scopes, dict) else []
                require(
                    isinstance(grants, list)
                    and any(
                        isinstance(g, dict)
                        and set(g) == {"tenant_id", "workspace_id", "source_kind"}
                        and g["tenant_id"] == tenant
                        and g["workspace_id"] == workspace
                        and g["source_kind"] in SOURCE_KINDS
                        and (
                            selected != source_id
                            or g["source_kind"] == metadata["source"]["kind"]
                        )
                        for g in grants
                    ),
                    "SOURCE_NOT_AVAILABLE",
                    404,
                )
                record = sources[selected]
                require(
                    record["data_class"] == metadata["data_class"],
                    "DATA_CLASS_MISMATCH",
                    403,
                )
                visited.add(selected)
                for parent in record["parent_ids"]:
                    admit(parent)

            admit(source_id)
            return registry, sources[source_id], sorted(visited)
        except IdentityError:
            raise
        except Exception:
            raise IdentityError("RIGHTS_DEPENDENCY_UNAVAILABLE", 503) from None

    def source_options(self, tenant, workspace):
        if not callable(self.registry_loader) or not callable(self.scope_loader):
            return []
        try:
            sources = self.validate_registry(self.registry_loader())
            scopes = self.scope_loader()
            output = []
            for source_id, source in sorted(sources.items()):
                if not REFERENCE.fullmatch(source_id) or not source["enabled"]:
                    continue
                if source["rights"]["status"] != "approved":
                    continue
                for grant in scopes.get(source_id, []):
                    if (
                        grant.get("tenant_id") != tenant
                        or grant.get("workspace_id") != workspace
                        or grant.get("source_kind") not in SOURCE_KINDS
                    ):
                        continue
                    candidate = {
                        "source": {
                            "source_id": source_id,
                            "kind": grant["source_kind"],
                        },
                        "data_class": source["data_class"],
                    }
                    try:
                        _, _, lineage = self._reviewed(tenant, workspace, candidate)
                    except IdentityError as error:
                        if error.code == "SOURCE_NOT_AVAILABLE":
                            continue
                        raise
                    now = datetime.now(UTC)
                    if any(
                        not sources[item]["enabled"]
                        or sources[item]["rights"]["status"] != "approved"
                        or (
                            timestamp(sources[item]["rights"]["valid_from"]) is not None
                            and now < timestamp(sources[item]["rights"]["valid_from"])
                        )
                        or (
                            timestamp(sources[item]["rights"]["expiry"]) is not None
                            and now >= timestamp(sources[item]["rights"]["expiry"])
                        )
                        for item in lineage
                    ):
                        continue
                    entry = {
                        "source_id": source_id,
                        "kind": grant["source_kind"],
                        "label": source_id,
                    }
                    if entry not in output:
                        output.append(entry)
            return output
        except IdentityError:
            raise
        except Exception:
            raise IdentityError("RIGHTS_DEPENDENCY_UNAVAILABLE", 503) from None

    def snapshot(self, tenant, workspace, metadata):
        reviewed = self._reviewed(tenant, workspace, metadata)
        if reviewed is None:
            value = {
                "status": "pending",
                "review_ref": None,
                "source_id": metadata["source"]["source_id"],
                "declaration": metadata["rights_declaration"],
                "reviewed_sources": [],
            }
        else:
            registry, source, selected = reviewed
            require(
                all(
                    s["enabled"]
                    and s["rights"]["status"] not in {"revoked", "prohibited"}
                    for s in registry["sources"]
                    if s["id"] in selected
                ),
                "SOURCE_NOT_AVAILABLE",
                404,
            )
            value = {
                "status": "pending"
                if source["rights"]["status"] == "unknown"
                else source["rights"]["status"],
                "review_ref": source["rights"]["review_ref"]
                if isinstance(source["rights"]["review_ref"], str)
                and REFERENCE.fullmatch(source["rights"]["review_ref"])
                else None,
                "source_id": source["id"],
                "declaration": metadata["rights_declaration"],
                "reviewed_sources": [
                    {
                        "source_id": s["id"],
                        "data_class": s["data_class"],
                        "status": s["rights"]["status"],
                        "source_hash": digest(s),
                    }
                    for s in registry["sources"]
                    if s["id"] in selected
                ],
            }
        value["private_reviewed_sources"] = (
            []
            if reviewed is None
            else [copy.deepcopy(s) for s in registry["sources"] if s["id"] in selected]
        )
        return {**value, "snapshot_hash": digest(value)}

    def check(self, tenant, workspace, metadata, *, purpose, profile, now=None):
        require(
            isinstance(purpose, str)
            and purpose in USES
            and isinstance(profile, str)
            and profile in {"demo", "operational"},
            "PURPOSE_NOT_PERMITTED",
            403,
        )
        require(metadata["data_class"] == profile, "DATA_CLASS_MISMATCH", 403)
        declaration = metadata["rights_declaration"]
        require(
            purpose != "external_share" or metadata["sensitivity"] == "public",
            "SENSITIVE_ASSET_NOT_SHAREABLE",
            403,
        )
        require(purpose in declaration["permitted_uses"], "PURPOSE_NOT_PERMITTED", 403)
        current = now or datetime.now(UTC)
        start, end = (
            timestamp(declaration["valid_from"]),
            timestamp(declaration["expiry"]),
        )
        require(
            current.tzinfo is not None
            and (start is None or current >= start)
            and (end is None or current < end),
            "RIGHTS_OUTSIDE_VALIDITY",
            403,
        )
        reviewed = self._reviewed(tenant, workspace, metadata)
        require(reviewed is not None, "RIGHTS_NOT_APPROVED", 403)
        registry, source, _ = reviewed
        try:
            return self.authorize(
                registry,
                profile="demo" if profile == "demo" else "production",
                purpose=purpose,
                source_ids=[source["id"]],
                now=now,
            )
        except Exception:
            # Fixed code only: upstream exceptions may contain private evidence.
            raise IdentityError("RIGHTS_NOT_PERMITTED", 403) from None
