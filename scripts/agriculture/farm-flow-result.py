"""Exact real-PG Flow adapter and legacy worker regression gate."""
import argparse
import importlib.util
import json
from pathlib import Path

spec = importlib.util.spec_from_file_location("farm_ci_result", Path(__file__).with_name("farm-ci-result.py"))
common = importlib.util.module_from_spec(spec)
spec.loader.exec_module(common)
PACKAGE = "yingce/backend/internal/flow"
CHILDREN = {
    "TestCommandAdapterPostgres": {
        "concurrent_enqueue_no_orphan", "expired_worker_fenced_and_io_outside_transaction",
        "cancelled_before_receipt_no_apply", "cross_tenant_and_empty_scope_denied",
    },
    "TestFlowPostgresWorkerShortTransactions": {
        "missing_scanner_fails_closed", "model_gateway_and_notification_outside_transaction",
        "model_reconciliation_http_outside_transaction", "command_http_outside_transaction_and_retry_receipt_commits",
    },
}
REQUIRED = set(CHILDREN) | {root + "/" + child for root, children in CHILDREN.items() for child in children}


def reduce(raw):
    passed, terminal = common.reduce_go(raw, REQUIRED, PACKAGE)
    return {"suite": "real_postgres_flow_adapter_and_legacy_worker", "passed": passed,
            "data_class": "fixture", "production_acceptance": False,
            "tests": [{"name": n, "result": terminal.get(n, "not_run")} for n in sorted(REQUIRED)],
            "passed_count": sum(v == "pass" for v in terminal.values())}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        report = reduce(common.read_bounded(args.input, 16 * 1024 * 1024))
    except (OSError, ValueError, TypeError, RecursionError):
        report = {"suite": "real_postgres_flow_adapter_and_legacy_worker", "passed": False,
                  "production_acceptance": False, "reason": "missing_or_invalid_required_report"}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
