# -*- coding: utf-8 -*-
"""Subprocess entry: python -m mathsvc.runner <stage>  (JSON in, JSON out)."""

import json
import sys

from .errors import MathError


def main():
    stage = sys.argv[1]
    req = json.load(sys.stdin)
    try:
        from .pipeline import run_stage
        out = {"ok": True, "result": run_stage(stage, req["payload"], req.get("prior") or {})}
    except MathError as e:
        out = {"ok": False, "error": e.to_dict()}
    except Exception as e:  # noqa: BLE001 - reported as internal, never as "no solution"
        out = {"ok": False, "error": MathError("INTERNAL", "stage crashed: %s" % type(e).__name__).to_dict()}
    sys.stdout.write(json.dumps(out, ensure_ascii=False))


if __name__ == "__main__":
    main()
