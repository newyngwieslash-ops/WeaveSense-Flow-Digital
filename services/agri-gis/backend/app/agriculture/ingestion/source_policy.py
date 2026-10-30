"""Composition adapter for the existing W00 rights engine, not self-approval."""

from .download import ApprovedSource, SafeDownloader
from .limits import IngestionDenied


class RegistryRights:
    def __init__(self, trusted_manifest_loader, existing_authorize):
        if not callable(trusted_manifest_loader) or not callable(existing_authorize):
            raise IngestionDenied("source_policy_unavailable")
        self.load, self.authorize = trusted_manifest_loader, existing_authorize

    def __call__(self, source_id, purpose):
        try:
            # Reload authoritative rights before every hop and publication so
            # revocation is not hidden by a cached approved source URL.
            return self.authorize(
                self.load(),
                profile="production",
                purpose=purpose,
                source_ids=[source_id],
            )
        except Exception:
            raise IngestionDenied("source_rights_denied") from None


def configured_downloader(sources, trusted_manifest_loader, existing_authorize):
    """Only operator code calls this; browser/MCP may pass IDs, never this policy.

    existing_authorize is contracts/agriculture/source-rights.py:authorize.
    Deployment must supply that existing rights engine and its locked dependency;
    absent composition is an unavailable provider, not a permissive fallback.
    """
    if not isinstance(sources, tuple) or not all(
        isinstance(source, ApprovedSource) for source in sources
    ):
        raise IngestionDenied("invalid_source_registry")
    return SafeDownloader(
        sources, RegistryRights(trusted_manifest_loader, existing_authorize)
    )
