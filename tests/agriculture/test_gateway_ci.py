"""CI permission/evidence boundaries, not actual runner execution."""

from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]


def test_read_only_gateway_workflow():
    body = (ROOT / ".github/workflows/agriculture-gateway.yml").read_text()
    data = yaml.safe_load(body)
    assert data["permissions"] == {"contents": "read"}
    for forbidden in (
        "pull_request_target",
        "secrets.",
        "docker push",
        "gh release",
        "git push",
        "packages: write",
        "id-token: write",
        "--no-sandbox",
    ):
        assert forbidden not in body
    assert list(data["jobs"]) == ["gateway"]
    steps = data["jobs"]["gateway"]["steps"]
    assert steps[0]["with"]["persist-credentials"] is False
    assert "node scripts/agriculture/gateway-browser-e2e.mjs" in body
    assert "python3 scripts/agriculture/gateway-container-smoke.py" in body
    uploads = [
        s for s in steps if s.get("uses", "").startswith("actions/upload-artifact@")
    ]
    assert len(uploads) == 1
    paths = uploads[0]["with"]["path"].splitlines()
    assert len(paths) == 4
    assert all(
        p.startswith("product/.local/evidence/")
        and p.endswith(".json")
        and "*" not in p
        for p in paths
    )
    assert all("log" not in p and "cookie" not in p for p in paths)
