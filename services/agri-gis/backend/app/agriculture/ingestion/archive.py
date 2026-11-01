"""Bounded archive consumption before existing Office/archive parsers run."""

import stat
from pathlib import PurePosixPath
from xml.etree import ElementTree
from zipfile import ZipFile

from .limits import IngestionDenied


def check_archive(path, limits):
    try:
        total, seen = 0, set()
        with ZipFile(path) as archive:
            entries = archive.infolist()
            if not 1 <= len(entries) <= limits.members:
                raise IngestionDenied("archive_members_limit")
            for item in entries:
                name = item.filename
                parts = name.rstrip("/").split("/")
                mode = item.external_attr >> 16
                if (
                    not name
                    or name.startswith("/")
                    or "\\" in name
                    or ":" in name
                    or any(p in {"", ".", ".."} for p in parts)
                    or any(ord(c) < 32 for c in name)
                    or name in seen
                    or stat.S_ISLNK(mode)
                    or (stat.S_IFMT(mode) not in {0, stat.S_IFREG, stat.S_IFDIR})
                    or item.flag_bits & 1
                ):
                    raise IngestionDenied("unsafe_archive_member")
                seen.add(name)
                if PurePosixPath(name).suffix.lower() in {
                    ".zip",
                    ".gz",
                    ".bz2",
                    ".xz",
                    ".tar",
                    ".7z",
                    ".jar",
                    ".xlsx",
                    ".docx",
                }:
                    raise IngestionDenied("nested_archive_denied")
                if item.file_size > max(1, item.compress_size) * limits.ratio:
                    raise IngestionDenied("archive_ratio_limit")
                if total + item.file_size > limits.expanded_bytes:
                    raise IngestionDenied("archive_expanded_limit")
                content = bytearray()
                with archive.open(item) as stream:
                    while chunk := stream.read(65536):
                        total += len(chunk)
                        if total > limits.expanded_bytes:
                            raise IngestionDenied("archive_expanded_limit")
                        if name.lower().endswith((".xml", ".rels")):
                            content.extend(chunk)
                            if len(content) > 4 * 1024 * 1024:
                                raise IngestionDenied("xml_size_limit")
                if content:
                    lower = bytes(content).lower()
                    if (
                        b"<!doctype" in lower
                        or b"<!entity" in lower
                        or b'targetmode="external"' in lower
                        or b"targetmode='external'" in lower
                    ):
                        raise IngestionDenied("external_document_reference_denied")
                    try:
                        text = bytes(content).decode("utf-8-sig")
                        root = ElementTree.fromstring(text)
                    except Exception:
                        raise IngestionDenied("invalid_document_xml") from None
                    for node in root.iter():
                        if any(
                            key.rsplit("}", 1)[-1].lower() == "targetmode"
                            and value.lower() == "external"
                            for key, value in node.attrib.items()
                        ):
                            raise IngestionDenied("external_document_reference_denied")
        return {
            "archive_member_count": len(entries),
            "archive_expanded_size_bytes": total,
        }
    except IngestionDenied:
        raise
    except Exception:
        raise IngestionDenied("invalid_archive") from None
