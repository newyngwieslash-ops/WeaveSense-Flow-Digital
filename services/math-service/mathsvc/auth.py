# -*- coding: utf-8 -*-
"""Signed service context from the platform.

The platform signs who is calling (tenant, actor, purpose) with a shared key;
the service never trusts tenant ids from the request body or from a model.

REST: X-Math-Context: base64url(JSON) and
      X-Math-Signature: hex(HMAC-SHA256(key, ctx_b64 "\\n" METHOD "\\n" PATH "\\n" sha256hex(body)))
MCP:  Authorization: Bearer v1.<ctx_b64>.<hex(HMAC-SHA256(key, "mcp\\n" ctx_b64))>
      (a connection credential bound to one tenant; exp required).
"""

import base64
import hashlib
import hmac
import json
import time

from .errors import MathError

MAX_SKEW = 300


def _b64d(s):
    return base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))


def _ctx(ctx_b64, max_ttl):
    try:
        ctx = json.loads(_b64d(ctx_b64))
    except Exception:  # noqa: BLE001
        raise MathError("POLICY_REVOKED", "bad context")
    t = time.time()
    exp = ctx.get("exp", 0)
    if not isinstance(exp, (int, float)) or exp < t or exp > t + max_ttl:
        raise MathError("POLICY_REVOKED", "context expired or ttl too long")
    if ctx.get("aud") != "math-service" or not ctx.get("tenant_id"):
        raise MathError("POLICY_REVOKED", "bad context audience or tenant")
    return ctx


def verify_rest(key, headers, method, path, body):
    c, sig = headers.get("X-Math-Context", ""), headers.get("X-Math-Signature", "")
    if not c or not sig:
        raise MathError("POLICY_REVOKED", "missing service signature")
    msg = "\n".join([c, method, path, hashlib.sha256(body).hexdigest()]).encode()
    want = hmac.new(key, msg, hashlib.sha256).hexdigest()
    if not hmac.compare_digest(want, sig):
        raise MathError("POLICY_REVOKED", "bad service signature")
    return _ctx(c, MAX_SKEW)


def verify_bearer(key, authz):
    if not authz.startswith("Bearer v1."):
        raise MathError("POLICY_REVOKED", "missing bearer")
    parts = authz[len("Bearer "):].split(".")
    if len(parts) != 3:
        raise MathError("POLICY_REVOKED", "bad bearer")
    want = hmac.new(key, ("mcp\n" + parts[1]).encode(), hashlib.sha256).hexdigest()
    if not hmac.compare_digest(want, parts[2]):
        raise MathError("POLICY_REVOKED", "bad bearer signature")
    return _ctx(parts[1], 400 * 24 * 3600)
