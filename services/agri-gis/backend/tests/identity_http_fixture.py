"""Test-process-only composition; never imported by production or enabled by env."""
import asyncio
import base64
import json
import socket
import sys

import httpx
import uvicorn
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from fastapi import Depends

from app.agriculture.main import create_app
from app.agriculture.identity.callback import CallbackKey, PlatformAuthority
from app.agriculture.identity.dependency import IdentityServices, delegation_dependency
from app.agriculture.identity.keys import KeyRing, utc_second
from app.agriculture.identity.providers import Ownership
from app.agriculture.identity.verify import Verifier


async def main():
    with open(sys.argv[1], encoding="utf-8") as source:
        config = json.load(source)

    class FixtureOwnership:
        async def resolve(self, tenant_id, workspace_id, resources):
            # Fixed domain facts, not reflections of arbitrary requested scope.
            return [Ownership("farm", "farm_a", config["tenant"], config["workspace"], ("farm_a",), 1)]

    key = config["callback_key"]
    async with httpx.AsyncClient(trust_env=False) as client:
        callback = PlatformAuthority(config["platform"], CallbackKey(
            key["kid"], Ed25519PrivateKey.from_private_bytes(base64.b64decode(key["seed"])),
            utc_second(key["not_before"]), utc_second(key["not_after"])), client)
        app = create_app(IdentityServices(Verifier(KeyRing.from_config(config["platform_keys"])),
            ownership=FixtureOwnership(), authority=callback))

        def probe_resources(request, body):
            if request.method in {"GET", "HEAD"}:
                return [{"kind": "farm", "id": "farm_a"}]
            return body["resources"]

        def path_resources(request, body):
            return [{"kind": "farm", "id": request.path_params["farm_id"]}]

        @app.api_route("/internal/agri/v1/delegation/check", methods=["GET", "POST"])
        async def probe(verified=Depends(delegation_dependency("agri.farm.read", probe_resources))):
            return {"verified": True}

        @app.get("/internal/agri/v1/delegation/other")
        async def other(verified=Depends(delegation_dependency("agri.farm.read", probe_resources))):
            return {"verified": True}

        @app.get("/internal/agri/v1/probe/{farm_id}")
        async def farm_probe(verified=Depends(delegation_dependency("agri.farm.read", path_resources))):
            return {"verified": True}

        sock = socket.socket()
        sock.bind(("127.0.0.1", 0))
        sock.listen(128)
        print(sock.getsockname()[1], flush=True)
        server = uvicorn.Server(uvicorn.Config(app, log_config=None, log_level="critical", access_log=False,
                                              http="h11", h11_max_incomplete_event_size=262144))
        await server.serve(sockets=[sock])


if __name__ == "__main__":
    asyncio.run(main())
