"""Configuration regressions only; real nginx HTTP evidence is separate."""

from pathlib import Path
import re

import yaml

ROOT = Path(__file__).resolve().parents[2]
DEPLOY = ROOT / "deploy/agriculture"


def test_gateway_is_only_loopback_and_platform_is_private():
    services = yaml.safe_load((ROOT / "docker-compose.agriculture.yml").read_text())[
        "services"
    ]
    assert services["web"]["ports"] == ["127.0.0.1:${AGRI_WEB_PORT:-8091}:8080"]
    assert services["web"]["read_only"] is True
    assert services["web"]["cap_drop"] == ["ALL"]
    assert not services["platform"].get("ports")
    assert services["platform"]["environment"]["FLOW_AGRICULTURE_ENABLED"] == "true"


def test_empty_secret_template_covers_actual_compose_variables():
    body = (ROOT / "docker-compose.agriculture.yml").read_text()
    env = dict(
        line.split("=", 1)
        for line in (DEPLOY / "env.example").read_text().splitlines()
        if line and not line.startswith("#")
    )
    assert set(re.findall(r"\$\{([A-Z_]+)", body)) <= env.keys()
    for key, value in env.items():
        if "PASSWORD" in key or "SIGNING_KEY" in key:
            assert value == ""
            assert "${" + key + ":?" in body


def test_static_rule_order_and_quantifier_quoting():
    conf = (DEPLOY / "nginx.conf.template").read_text()
    assert conf.index("location ~ /\\.") < conf.index('location ~ "^/(?:agri/)?assets/')
    assert 'location ~ "^/(?:agri/)?assets/' in conf
    assert "{8,}" in conf
    assert "error_log /dev/null crit;" in conf
    assert (
        "log_format agriculture '$request_method $uri $status $body_bytes_sent';"
        in conf
    )


def test_private_paths_cannot_reach_platform_proxy():
    conf = (DEPLOY / "nginx.conf.template").read_text()
    for path in ("/internal", "/api/internal", "/api/v1", "/agri/api"):
        assert f"location = {path} {{ return 404; }}" in conf
        assert f"location ^~ {path}/ {{ return 404; }}" in conf
    assert "proxy_pass http://${AGRI_PLATFORM_UPSTREAM};" in conf
    assert "proxy_intercept_errors off;" in conf
    assert "proxy_set_header Host $agriculture_host;" in conf
    assert "default $http_host;" in conf


def test_two_real_builds_and_shared_navigation_are_in_image():
    docker = (DEPLOY / "Dockerfile.web").read_text()
    assert "bun install --frozen-lockfile --ignore-scripts" in docker
    assert "npm ci --ignore-scripts --no-audit --no-fund" in docker
    assert "RUN bun run build:agriculture" in docker
    assert "RUN npm run build:agriculture" in docker
    assert (
        "COPY contracts/agriculture/navigation.ts /app/contracts/agriculture/navigation.ts"
        in docker
    )
    assert "USER nginx" in docker
    ignore = (DEPLOY / "Dockerfile.web.dockerignore").read_text()
    assert "!deploy/agriculture/**" not in ignore
    assert "!deploy/agriculture/nginx.conf.template" in ignore
    for pattern in (
        "**/node_modules",
        "**/.env*",
        "**/*.pem",
        "**/*.key",
        "**/dist-agriculture",
    ):
        assert pattern in ignore
