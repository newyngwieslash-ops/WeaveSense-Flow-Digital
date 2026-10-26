# -*- coding: utf-8 -*-
"""Error codes shared with the platform (PRD FR-P06)."""

CODES = {
    "INPUT_AMBIGUOUS": (False, "teacher_confirm_input"),
    "CONDITION_CONFLICT": (False, "teacher_fix_conditions"),
    "UNSUPPORTED_FAMILY": (False, "manual_authoring"),
    "PARAMETER_LIMIT": (False, "adjust_parameters"),
    "METHOD_MISMATCH": (False, "change_method_profile"),
    "VALIDATION_FAILED": (False, "teacher_review"),
    "RENDER_FAILED": (True, "retry_render"),
    "BUDGET_EXCEEDED": (False, "raise_budget"),
    "RATE_LIMITED": (True, "retry_later"),
    "PROVIDER_UNAVAILABLE": (True, "retry_later"),
    "OUTCOME_UNKNOWN": (False, "reconcile"),
    "REVISION_CONFLICT": (False, "reload_revision"),
    "POLICY_REVOKED": (False, "contact_admin"),
    "TIMEOUT_UNDETERMINED": (False, "simplify_or_manual"),
    "INTERNAL": (True, "retry_later"),
}


class MathError(Exception):
    def __init__(self, code, message, details=None):
        super().__init__(message)
        if code not in CODES:
            code = "INTERNAL"
        self.code = code
        self.message = message
        self.details = details or {}

    def to_dict(self):
        retryable, next_action = CODES[self.code]
        return {"code": self.code, "message": self.message, "retryable": retryable,
                "next_action": next_action, "details": self.details}
