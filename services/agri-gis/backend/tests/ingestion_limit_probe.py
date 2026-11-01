"""Test-only fixed child for actual resource limit enforcement, never a route."""

import errno
import json
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from app.agriculture.ingestion.limits import ParseLimits  # noqa: E402
from app.agriculture.ingestion.worker import constrain  # noqa: E402

request = json.loads(Path(sys.argv[1]).read_bytes())
constrain(ParseLimits(**request["limits"]))
mode = request["filename"].split(".")[0]
if mode == "cpu":
    while True:
        pass
elif mode == "wall":
    time.sleep(20)
elif mode == "memory":
    try:
        bytearray(1024**3)
    except MemoryError:
        print(json.dumps({"ok": False, "code": "probe_memory_enforced"}))
        raise SystemExit(2) from None
elif mode == "file":
    try:
        (Path(sys.argv[1]).parent / "too-large").write_bytes(b"x" * 8192)
    except OSError as error:
        if error.errno != errno.EFBIG:
            raise
        print(json.dumps({"ok": False, "code": "probe_file_enforced"}))
        raise SystemExit(2) from None
elif mode == "output":
    print("x" * 100000)
print(json.dumps({"ok": True, "result": {"unexpected_success": True}}))
