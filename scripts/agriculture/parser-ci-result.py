"""Closed structural pytest diagnostics; never export raw exception strings."""
import json

STAGES = {"valid_raster", "corrupt_raster", "missing_crs", "archive_traversal",
          "archive_bomb", "pixel_limit"}
EXCEPTIONS = {"none", "other", "probe_unavailable", "ImportError",
              "ModuleNotFoundError", "OSError", "PermissionError", "MemoryError",
              "RuntimeError", "ValueError", "TypeError", "KeyError",
              "IngestionDenied", "RasterioIOError"}
REASONS = {"parser_process_failed", "parser_rejected_input", "parser_timeout",
           "parser_cleanup_failed", "parser_sandbox_unavailable", "parser_output_limit",
           "raster_pixels_limit", "invalid_parser_response"}
LIBRARIES = {"libexpat.so.1", "libstdc++.so.6", "libgcc_s.so.1", "libz.so.1"}


def diagnostic(case):
    result = {}
    for prop in case.findall("./properties/property"):
        value = prop.get("value", "")
        if prop.get("name") == "parser_stage" and value in STAGES:
            result["stage"] = value
        if prop.get("name") == "parser_diagnostic" and len(value) < 256:
            try:
                value = json.loads(value)
            except (ValueError, TypeError):
                continue
            if not isinstance(value, dict) or value.get("exception") not in EXCEPTIONS:
                continue
            result["probe_exception"] = value["exception"]
            if isinstance(value.get("missing_library"), str) and value["missing_library"] in LIBRARIES:
                result["missing_library"] = value["missing_library"]
            if type(value.get("line")) is int and 1 <= value["line"] <= 10000:
                result["probe_line"] = value["line"]
    for failure in list(case):
        if failure.tag not in {"failure", "error", "skipped"}:
            continue
        result["failure_kind"] = failure.tag
        # Extract only a known product error token, not the arbitrary message.
        message = failure.get("message", "")
        for reason in sorted(REASONS):
            if message == reason or message.endswith(": " + reason):
                result["reason"] = reason
    return result


def passed(case):
    # pytest properties are evidence, not a failed/skipped result element.
    return not any(child.tag in {"failure", "error", "skipped"} for child in case)
