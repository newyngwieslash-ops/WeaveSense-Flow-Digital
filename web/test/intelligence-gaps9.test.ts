import { describe, expect, test } from "bun:test";

import { expansionFromError } from "../src/pages/intelligence/monitor-editor";
import { credits } from "../src/pages/intelligence/plan-card";

describe("intelligence audit-8 UI helpers", () => {
    test("re-approval expansion is parsed from the server 409 message", () => {
        expect(expansionFromError(new Error("reapproval_required: sources,budget"))).toEqual(["sources", "budget"]);
        expect(expansionFromError({ message: "monitor version conflict" })).toBeUndefined();
    });
    test("micro-credits render as credits", () => {
        expect(credits(1_500_000)).toContain("1.5");
        expect(credits(undefined)).toBe("—");
    });
});
