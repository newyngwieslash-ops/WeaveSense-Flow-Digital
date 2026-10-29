"""Bounded input validation; the pinned rfc8785 library owns JCS serialization."""
import json
import math

import rfc8785


def _number(token: str) -> float:
    value = token.lstrip("-").lower()
    exponent = 0
    if "e" in value:
        value, exp = value.split("e")
        digits = exp.lstrip("+-").lstrip("0")
        exponent = min(int(digits or "0"), 2000000) if len(digits) <= 7 else 2000000
        if exp.startswith("-"):
            exponent = -exponent
    if "." in value:
        whole, fraction = value.split(".")
        exponent -= len(fraction)
        value = whole + fraction
    digits = value.lstrip("0")
    if digits:
        position = len(digits) + exponent
        if position > 16:
            raise ValueError("unsafe JSON number")
        if position == 16:
            whole = digits[:16].ljust(16, "0")
            if whole > "9007199254740991" or (
                whole == "9007199254740991" and digits[16:].strip("0")
            ):
                raise ValueError("unsafe JSON number")
    result = float(token)
    if not math.isfinite(result):
        raise ValueError("nonfinite JSON number")
    return result


def _object(pairs):
    if len(pairs) > 256:
        raise ValueError("too many object keys")
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def _constant(_):
    raise ValueError("nonfinite JSON number")


def canonicalize(raw: bytes) -> bytes:
    """Validate rfc8785-safe-v1 and return canonical bytes or raise ValueError.

    Root is depth one; object keys do not count as value nodes.
    """
    if not isinstance(raw, bytes) or not raw or len(raw) > 1 << 20:
        raise ValueError("invalid JSON size or type")
    try:
        text = raw.decode("utf-8", errors="strict")
        # Bound parser recursion before json.loads; quoted braces do not count.
        depth = 0
        quoted = escaped = False
        for char in text:
            if quoted:
                if escaped:
                    escaped = False
                elif char == "\\":
                    escaped = True
                elif char == '"':
                    quoted = False
            elif char == '"':
                quoted = True
            elif char in "{[":
                depth += 1
                if depth > 32:
                    raise ValueError("JSON nesting limit")
            elif char in "}]":
                depth -= 1
        obj = json.loads(text, object_pairs_hook=_object, parse_int=_number,
                         parse_float=_number, parse_constant=_constant)
        if not isinstance(obj, dict):
            raise ValueError("JSON root must be object")
        pending = [(obj, 1)]
        nodes = 0
        while pending:
            value, level = pending.pop()
            nodes += 1
            if nodes > 10000 or level > 32:
                raise ValueError("JSON complexity limit")
            if isinstance(value, dict):
                for key in value:
                    key.encode("utf-8", errors="strict")
                pending.extend((v, level + 1) for v in value.values())
            elif isinstance(value, list):
                pending.extend((v, level + 1) for v in value)
            elif isinstance(value, str):
                value.encode("utf-8", errors="strict")
        return rfc8785.dumps(obj)
    except (UnicodeError, RecursionError, OverflowError) as exc:
        raise ValueError("invalid canonical JSON") from exc
