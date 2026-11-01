"""Apply current catalog rights to the existing immutable object lookup path."""

from .identity.verify import require


class CatalogObjectStore:
    def __init__(self, objects, catalog):
        self.objects = objects
        self.catalog = catalog

    async def require_ready(self):
        await self.objects.require_ready()
        await self.catalog.require_ready()

    async def get(self, tenant, workspace, object_id, version):
        item = await self.objects.get(tenant, workspace, object_id, version)
        if item.resource_kind != "asset":
            return item
        # Purpose is a fixed server operation, never a token/browser field.
        await self.catalog.authorize_use(
            tenant,
            workspace,
            item.resource_id,
            item.resource_revision,
            purpose="export",
            profile=item.data_class,
        )
        asset = await self.catalog.get(
            tenant, workspace, item.resource_id, item.resource_revision
        )
        require(
            asset["status"] == "ready"
            and asset["data_class"] == item.data_class
            and asset["entity"]
            == {"object_id": item.object_id, "object_version": item.version},
            "ENTITY_NOT_AVAILABLE",
            403,
        )
        # The entity FK selects this immutable DT020 reference, including its
        # content hash. Re-read rather than trust a stale or altered wrapper ref.
        current = await self.objects.get(tenant, workspace, object_id, version)
        require(current == item, "ENTITY_NOT_AVAILABLE", 403)
        return item
