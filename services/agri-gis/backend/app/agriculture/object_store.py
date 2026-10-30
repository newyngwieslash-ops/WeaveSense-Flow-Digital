"""Read-only object references under independently enforced PostgreSQL scope."""

from dataclasses import dataclass

from .identity.verify import IdentityError
from .object_storage import object_key


@dataclass(frozen=True)
class ObjectReference:
    tenant_id: str
    workspace_id: str
    object_id: str
    version: int
    resource_kind: str
    resource_id: str
    resource_revision: int
    category: str
    object_key: str
    content_sha256: str
    size_bytes: int
    media_type: str
    data_class: str

    def metadata(self):
        return {
            "tenant_id": self.tenant_id,
            "workspace_id": self.workspace_id,
            "object_id": self.object_id,
            "version": self.version,
            "resource": {"kind": self.resource_kind, "id": self.resource_id},
            "resource_revision": self.resource_revision,
        }


class PostgresObjectStore:
    def __init__(self, pool):
        self.pool = pool

    async def require_ready(self):
        try:
            async with self.pool.acquire(timeout=3) as connection:
                ready = await connection.fetchval("""
SELECT current_user='flow_geo_app' AND c.relrowsecurity AND c.relforcerowsecurity
 AND pg_get_userbyid(c.relowner)='flow_geo_migrate'
 AND has_table_privilege(current_user,c.oid,'SELECT')
 AND NOT has_table_privilege(current_user,c.oid,'INSERT,UPDATE,DELETE,TRUNCATE')
 AND EXISTS(SELECT 1 FROM pg_catalog.pg_policy p WHERE p.polrelid=c.oid
  AND p.polname='agriculture_object_scope' AND NOT p.polpermissive)
 AND EXISTS(SELECT 1 FROM pg_catalog.pg_trigger t WHERE t.tgrelid=c.oid
  AND t.tgname='agriculture_object_immutable' AND t.tgenabled='O')
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relname='agriculture_private_objects'
""")
                if ready is not True:
                    raise ValueError("object storage boundary unavailable")
        except Exception:
            raise IdentityError("object_dependency_unavailable", 503) from None

    async def get(self, tenant, workspace, object_id, version):
        try:
            object_key(tenant, workspace, object_id, version)
        except ValueError:
            raise IdentityError("object_not_found", 404) from None
        try:
            async with self.pool.acquire(timeout=3) as connection:
                async with connection.transaction():
                    await connection.execute(
                        "SELECT set_config('app.tenant_ids',$1,true),"
                        "set_config('app.workspace_id',$2,true)",
                        tenant,
                        workspace,
                    )
                    row = await connection.fetchrow(
                        "SELECT tenant_id,workspace_id,object_id,version,resource_kind,"
                        "resource_id,resource_revision,category,object_key,content_sha256,"
                        "size_bytes,media_type,data_class "
                        "FROM public.agriculture_private_objects "
                        "WHERE tenant_id=$1 AND workspace_id=$2 "
                        "AND object_id=$3 AND version=$4 "
                        "AND state='active' AND distribution_allowed",
                        tenant,
                        workspace,
                        object_id,
                        version,
                    )
            if row is None:
                raise IdentityError("object_not_found", 404)
            return ObjectReference(**dict(row))
        except IdentityError:
            raise
        except Exception:
            raise IdentityError("object_dependency_unavailable", 503) from None
