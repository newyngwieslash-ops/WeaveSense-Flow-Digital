import { describe, expect, test } from "bun:test";

import { conflictOf, diffEdits, draftOf, draftProblems, estimateText, type Editable } from "../src/components/edu/lesson-edit-logic";

const base: Editable = {
    lessonRevisionId: "elr_1",
    lessonHash: "sha256:abc",
    state: "approved",
    beats: [
        { id: "s1", title: "找垂足", say: "过顶点作底面垂线。", board: ["PO ⊥ 底面"] },
        { id: "s2", title: "求角", say: "在直角三角形中求正弦。", board: ["sin θ = PO / PA"], reveals_answer: true },
    ],
    hints: [{ level: 1, kind: "direction", text: "先找垂足" }],
    pronunciations: { 棱: "léng" },
    editReviewInherit: "none",
    latestRevisionId: "elr_1",
    limits: { maxEdits: 50, maxText: 600, maxBoardLines: 12, maxLine: 200 },
};

describe("lesson edit logic", () => {
    test("an untouched draft has no edits", () => {
        expect(diffEdits(base, draftOf(base))).toEqual([]);
    });
    test("only changed fields become edits", () => {
        const d = draftOf(base);
        d.beats.s1.say = "  先看底面。 ";
        d.beats.s2.board = "sin θ = PO / PA\n\n连接 AC\n";
        d.hints[1] = "先找垂足";
        expect(diffEdits(base, d)).toEqual([
            { target: "beat", id: "s1", field: "say", value: "先看底面。" },
            { target: "beat", id: "s2", field: "board", value: ["sin θ = PO / PA", "连接 AC"] },
        ]);
    });
    test("pronunciation add, change and removal", () => {
        const d = draftOf(base);
        d.pronunciations = [{ term: "锥", reading: "zhuī" }];
        expect(diffEdits(base, d)).toEqual([
            { target: "pronunciation", term: "锥", reading: "zhuī" },
            { target: "pronunciation", term: "棱", reading: "" },
        ]);
    });
    test("local limits are reported before saving", () => {
        const d = draftOf(base);
        d.beats.s1.board = "";
        d.beats.s1.title = "";
        expect(draftProblems(base, diffEdits(base, d))).toEqual(["文字需 1 到 600 字", "板书需 1 到 12 行"]);
    });
    test("conflict details keep the caller's edits", () => {
        const c = conflictOf({ conflict: { reason: "head_taken", latestRevisionId: "elr_2" }, yourEdits: [{ target: "pronunciation", term: "棱", reading: "léng" }] });
        expect(c?.latestRevisionId).toBe("elr_2");
        expect(c?.yourEdits).toHaveLength(1);
        expect(conflictOf({ error: { code: "X" } })).toBeNull();
    });
    test("estimate text names the bound and the price version", () => {
        expect(estimateText({ maxComputeMs: 45000, maxCostMicro: 45000, unitMicro: 1, priceVersion: "price_v1" })).toContain("费用上限 45000");
        expect(estimateText({ maxComputeMs: 45000, maxCostMicro: 0, unitMicro: 0, priceVersion: "p0" })).toContain("未对计算计费");
    });
});
