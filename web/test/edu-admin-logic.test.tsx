import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";

import { dropReasonLabel, extensionDueISO, setProgress, variantApprovable, variantShortfall } from "../src/components/edu/edu-admin-logic";
import { SetSelfTest } from "../src/components/edu/set-self-test";
import type { EduAttempt } from "../src/services/api/edu";

describe("variant set review", () => {
    test("only a clean, non-empty, pending set is approvable", () => {
        expect(variantApprovable({ state: "ready_for_review", blockingErrors: "", itemCount: 2 })).toBe(true);
        expect(variantApprovable({ state: "ready_for_review", blockingErrors: "v2:not_verified", itemCount: 2 })).toBe(false);
        expect(variantApprovable({ state: "approved", blockingErrors: "", itemCount: 2 })).toBe(false);
        expect(variantApprovable({ state: "ready_for_review", blockingErrors: "", itemCount: 0 })).toBe(false);
    });
    test("a shortfall is reported, never padded", () => {
        expect(variantShortfall({ requested: 3, itemCount: 3 })).toBe("");
        expect(variantShortfall({ requested: 3, itemCount: 2 })).toContain("保留 2 题");
    });
});

test("drop reasons are explained", () => {
    expect(dropReasonLabel("answer_reused")).toContain("重复");
    expect(dropReasonLabel("not_verified:candidate")).toContain("candidate");
    expect(dropReasonLabel("TIMEOUT")).toContain("TIMEOUT");
});

describe("extensions", () => {
    test("must be later than the assignment due", () => {
        expect(extensionDueISO("", null).ok).toBe(false);
        const r = extensionDueISO("2026-11-02T20:00", "2026-11-01T12:00:00Z");
        expect(r.ok).toBe(true);
        expect(extensionDueISO("2026-10-30T20:00", "2026-11-01T12:00:00Z").ok).toBe(false);
    });
});

describe("set self-test progress", () => {
    test("counts each item once and completes only when all are submitted", () => {
        const qs = [{ id: "v1" }, { id: "v2" }];
        const p1 = setProgress(qs, [{ id: "a", item: "v1", result: "recorded", valid: true, response: "1" }]);
        expect(p1.remaining).toBe(1);
        expect(p1.complete).toBe(false);
        const p2 = setProgress(qs, [
            { id: "a", item: "v1", result: "correct", valid: true, response: "1" },
            { id: "m", item: "main", result: "correct", valid: true, response: "x" },
            { id: "b", item: "v2", result: "incorrect", valid: true, response: "2" },
        ]);
        expect(p2.complete).toBe(true);
        expect(setProgress([], []).complete).toBe(false);
    });
});

describe("SetSelfTest rendering", () => {
    const questions = [
        { id: "v1", prompt: "题一", unit: "", answer_format: "exact" },
        { id: "v2", prompt: "题二", unit: "", answer_format: "exact" },
    ];
    const at = (item: string, result: string) => ({ id: item, clientKey: item, item, kind: "answer", response: "r" + item, valid: true, result }) as unknown as EduAttempt;
    test("before the unit is complete only 'recorded' shows, never a result or key", () => {
        const html = renderToStaticMarkup(<SetSelfTest selfTest={{ state: "started", helpUsed: false, sawSolution: false, measurement: "set", questions }} attempts={[at("v1", "recorded")]} busy={false} onSubmit={() => {}} />);
        expect(html).toContain("已记录");
        expect(html).toContain("还剩 1 题");
        expect(html).not.toContain("参考答案");
        expect(html).toContain("提交本题");
    });
    test("after submission results and keys show", () => {
        const html = renderToStaticMarkup(
            <SetSelfTest
                selfTest={{ state: "submitted", helpUsed: false, sawSolution: false, measurement: "set", questions, answerKeys: { v1: { plain: "1/3", latex: "" }, v2: { plain: "2", latex: "" } } }}
                attempts={[at("v1", "correct"), at("v2", "incorrect")]}
                busy={false}
                onSubmit={() => {}}
            />,
        );
        expect(html).toContain("参考答案：1/3");
        expect(html).toContain("不正确");
        expect(html).not.toContain("提交本题");
    });
});
