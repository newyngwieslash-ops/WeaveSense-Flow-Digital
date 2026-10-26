import { describe, expect, test } from "bun:test";

import { anomalyLabel, anomalyUpdateError, budgetFormOf, costLineText, currentPeriod, formatMicro, parseBudgetForm, usagePercent } from "../src/components/edu/ledger-logic";

describe("ledger admin", () => {
    test("amounts are micro units and never invented", () => {
        expect(formatMicro(1234567)).toBe("1.2346");
        expect(formatMicro(-5)).toBe("-0.0000");
        expect(formatMicro(null)).toBe("无");
        expect(formatMicro(Number.NaN)).toBe("无");
    });
    test("anomaly handling follows the server rules", () => {
        expect(anomalyUpdateError("investigating", "", "")).toContain("负责人");
        expect(anomalyUpdateError("resolved", "ops", "  ")).toContain("说明");
        expect(anomalyUpdateError("resolved", "", "已补结算")).toBe("");
        expect(anomalyUpdateError("closed", "a", "b")).toBe("未知状态");
        expect(anomalyUpdateError("open", "x".repeat(41), "")).toContain("40");
        expect(anomalyLabel("duplicate_stage_charge")).toContain("重复");
        expect(anomalyLabel("new_kind")).toBe("new_kind");
    });
});

describe("budget form", () => {
    test("blank is no limit, invalid input is refused not corrected", () => {
        const ok = parseBudgetForm({ monthlyMicro: "500000", teacherMonthlyMicro: "", maxActiveJobs: "4", maxActivePerTeacher: "", maxRenderJobs: "1", alertPercent: "" });
        expect(ok.ok && ok.value).toEqual({ monthlyMicro: 500000, teacherMonthlyMicro: 0, maxActiveJobs: 4, maxActivePerTeacher: 0, maxRenderJobs: 1, alertPercent: 80 });
        const base = budgetFormOf(null);
        expect(parseBudgetForm({ ...base, monthlyMicro: "-1" }).ok).toBe(false);
        expect(parseBudgetForm({ ...base, maxActiveJobs: "2.5" }).ok).toBe(false);
        expect(parseBudgetForm({ ...base, alertPercent: "120" }).ok).toBe(false);
        expect(parseBudgetForm({ ...base, maxActiveJobs: "10001" }).ok).toBe(false);
        expect(budgetFormOf({ monthlyMicro: 0, alertPercent: 90 })).toMatchObject({ monthlyMicro: "", alertPercent: "90" });
    });
    test("no limit is not shown as 0%", () => {
        expect(usagePercent(100, 0)).toBeNull();
        expect(usagePercent(360000, 400000)).toBe(90);
    });
});

describe("cost report", () => {
    test("unmetered components are labelled, not zero", () => {
        expect(costLineText({ component: "tts", source: "provider not connected", metered: false, costMicro: 0, quantity: 0, unit: "characters" })).toBe("未计量");
        expect(costLineText({ component: "storage", source: "edu_artifacts.size", metered: false, costMicro: 0, quantity: 2048, unit: "bytes" })).toContain("2048 bytes");
        expect(costLineText({ component: "open_holds", source: "", metered: true, costMicro: 0, quantity: 120000, unit: "micro_held" })).toBe("持有 0.1200");
        expect(costLineText({ component: "render", source: "", metered: true, costMicro: 2500, quantity: 2500, unit: "compute_ms" })).toBe("0.0025");
    });
    test("period follows the server calendar", () => {
        expect(currentPeriod(new Date("2026-10-31T17:30:00Z"))).toBe("2026-11");
        expect(currentPeriod(new Date("2026-10-31T15:30:00Z"))).toBe("2026-10");
    });
});
