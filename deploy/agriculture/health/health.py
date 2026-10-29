"""W00 dependency probe, NOT the GIS application or business implementation.

Only liveness/readiness are exposed. No imported AgriScope routes may be served
before signed identity and tenant isolation are implemented and verified.
"""
import asyncio
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import asyncpg


async def ready():
    connection = await asyncpg.connect(os.environ["DATABASE_URL"], timeout=3)
    try:
        row = await connection.fetchrow(
            "SELECT current_database() AS db, current_user AS role, "
            "rolsuper, rolbypassrls, rolcreatedb, rolcreaterole "
            "FROM pg_roles WHERE rolname = current_user"
        )
        if (row["db"] != "flow_geo" or row["role"] != "flow_geo_app"
                or any(row[k] for k in ("rolsuper", "rolbypassrls", "rolcreatedb", "rolcreaterole"))):
            return False
        return bool(await connection.fetchval("SELECT postgis_version()"))
    finally:
        await connection.close()


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass  # Never log query strings or request headers.

    def do_GET(self):
        code = 503
        body = {"stage": "W00", "data_class": "demo", "business_api": "not_implemented"}
        if self.path == "/health/live":
            code = 200
        elif self.path == "/health/ready":
            try:
                if asyncio.run(ready()):
                    code = 200
            except Exception:
                pass  # The caller receives no database URL or driver error detail.
        else:
            body["reason"] = "AGRICULTURE_BUSINESS_API_NOT_READY"
        raw = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    do_POST = do_GET
    do_PUT = do_GET
    do_PATCH = do_GET
    do_DELETE = do_GET


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8000), Handler).serve_forever()
