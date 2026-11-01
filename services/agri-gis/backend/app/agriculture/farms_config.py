"""Versioned deployment dictionary; codes do not prove administrative coverage."""

import copy
import hashlib
import json
import re
from pathlib import Path
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from .identity.canonical import canonicalize
from .identity.verify import IdentityError

SCHEMA_VERSION = "agriculture-farm-dictionary/1"
CODE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,63}\Z")
AREA_UNITS = frozenset({"m2", "ha", "mu"})


class FarmDictionary:
    """Immutable snapshot selected by trusted deployment, never request data."""

    def __init__(self, document, canonical):
        self._document = copy.deepcopy(document)
        self._canonical = canonical
        self.sha256 = hashlib.sha256(canonical).hexdigest()
        self.dictionary_version = document["dictionary_version"]
        self._regions = {row["code"]: row for row in self._document["regions"]}

    @classmethod
    def from_file(cls, path):
        try:
            if not isinstance(path, str | Path) or not str(path):
                raise ValueError("dictionary required")
            file = Path(path)
            if not file.is_file() or file.is_symlink() or file.stat().st_size > 1 << 20:
                raise ValueError("invalid dictionary file")
            # Duplicate keys, non-finite JSON and complexity limits are shared
            # with the trusted request contract, not silently accepted by loads.
            canonical = canonicalize(file.read_bytes())
            return cls.from_document(json.loads(canonical))
        except IdentityError:
            raise
        except Exception:
            raise IdentityError("farm_dictionary_unavailable", 503) from None

    @classmethod
    def from_document(cls, document):
        try:
            if not isinstance(document, dict) or set(document) != {
                "schema_version",
                "dictionary_version",
                "regions",
            }:
                raise ValueError("dictionary fields")
            if (
                document["schema_version"] != SCHEMA_VERSION
                or not isinstance(document["dictionary_version"], str)
                or not CODE.fullmatch(document["dictionary_version"])
            ):
                raise ValueError("dictionary version")
            regions = document["regions"]
            if not isinstance(regions, list) or not 1 <= len(regions) <= 1000:
                raise ValueError("dictionary regions")
            seen = set()
            for region in regions:
                if not isinstance(region, dict) or set(region) != {
                    "code",
                    "name",
                    "timezones",
                    "area_units",
                }:
                    raise ValueError("region fields")
                code, name = region["code"], region["name"]
                if (
                    not isinstance(code, str)
                    or not CODE.fullmatch(code)
                    or code in seen
                ):
                    raise ValueError("region code")
                seen.add(code)
                if (
                    not isinstance(name, str)
                    or not 1 <= len(name) <= 128
                    or (name != name.strip() or any(ord(c) < 32 for c in name))
                ):
                    raise ValueError("region name")
                timezones, units = region["timezones"], region["area_units"]
                if not isinstance(timezones, list) or not 1 <= len(timezones) <= 32:
                    raise ValueError("region timezones")
                if any(
                    not isinstance(zone, str) or len(zone) > 128 for zone in timezones
                ):
                    raise ValueError("timezone")
                if len(set(timezones)) != len(timezones):
                    raise ValueError("duplicate timezone")
                for zone in timezones:
                    ZoneInfo(zone)
                if not isinstance(units, list) or not 1 <= len(units) <= 3:
                    raise ValueError("area units")
                if any(
                    not isinstance(unit, str) or unit not in AREA_UNITS
                    for unit in units
                ):
                    raise ValueError("unsupported area unit")
                if len(set(units)) != len(units):
                    raise ValueError("duplicate area unit")
            canonical = canonicalize(json.dumps(document, ensure_ascii=False).encode())
            return cls(json.loads(canonical), canonical)
        except (ValueError, TypeError, KeyError, ZoneInfoNotFoundError):
            raise IdentityError("farm_dictionary_unavailable", 503) from None

    def snapshot(self):
        return copy.deepcopy(self._document)

    def validate_selection(self, dictionary_version, region_code, timezone, area_unit):
        if dictionary_version != self.dictionary_version:
            raise IdentityError("farm_dictionary_stale", 409)
        if not all(
            isinstance(value, str) for value in (region_code, timezone, area_unit)
        ):
            raise IdentityError("farm_region_invalid", 422)
        region = self._regions.get(region_code)
        if (
            region is None
            or timezone not in region["timezones"]
            or area_unit not in region["area_units"]
        ):
            raise IdentityError("farm_region_invalid", 422)
        return copy.deepcopy(region)
