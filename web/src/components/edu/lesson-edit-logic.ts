/**
 * 讲解结构化编辑的纯逻辑（FR-T05/T06，AC-17/18），便于单元测试。
 * 只生成字段级编辑；是否可编辑、影响范围和冲突都以服务端判定为准。
 */
export type EditableBeat = { id: string; title: string; say: string; board: string[] | null; reveals_answer?: boolean };
export type EditableHint = { level: number; kind: string; text: string };
export type Editable = {
    lessonRevisionId: string;
    lessonHash: string;
    state: string;
    parentLessonRevisionId?: string;
    beats: EditableBeat[];
    hints: EditableHint[];
    pronunciations: Record<string, string> | null;
    quality?: { edited_unverified?: string[]; math_text?: string; pedagogy?: string } | null;
    editReviewInherit: string;
    latestRevisionId: string;
    limits: { maxEdits: number; maxText: number; maxBoardLines: number; maxLine: number };
};
export type Draft = {
    beats: Record<string, { title: string; say: string; board: string }>;
    hints: Record<number, string>;
    pronunciations: { term: string; reading: string }[];
};
export type LessonEdit =
    | { target: "beat"; id: string; field: "title" | "say"; value: string }
    | { target: "beat"; id: string; field: "board"; value: string[] }
    | { target: "hint"; level: number; field: "text"; value: string }
    | { target: "pronunciation"; term: string; reading: string };

export const EDIT_CLASS_LABEL: Record<string, string> = { reasoning: "推理（板书）", wording: "措辞", pronunciation: "读音" };
export const INVALIDATES_LABEL: Record<string, string> = {
    review: "审核结论",
    student_projection: "学生版讲解",
    preview: "预览",
    media: "配音与视频",
    captions: "字幕",
};
export const INHERIT_LABEL: Record<string, string> = {
    none: "所有修改都需重新审核",
    pronunciation_only: "仅改读音时继承上一版审核结论",
};

const boardLines = (s: string) =>
    s
        .split("\n")
        .map((x) => x.trim())
        .filter(Boolean);

export function draftOf(e: Editable): Draft {
    const beats: Draft["beats"] = {};
    for (const b of e.beats) beats[b.id] = { title: b.title, say: b.say, board: (b.board || []).join("\n") };
    const hints: Draft["hints"] = {};
    for (const h of e.hints) hints[h.level] = h.text;
    const pronunciations = Object.entries(e.pronunciations || {}).map(([term, reading]) => ({ term, reading }));
    return { beats, hints, pronunciations };
}

/** diffEdits 只输出与原修订不同的字段；删除读音以空 reading 表示。 */
export function diffEdits(e: Editable, d: Draft): LessonEdit[] {
    const out: LessonEdit[] = [];
    for (const b of e.beats) {
        const x = d.beats[b.id];
        if (!x) continue;
        if (x.title.trim() !== b.title) out.push({ target: "beat", id: b.id, field: "title", value: x.title.trim() });
        if (x.say.trim() !== b.say) out.push({ target: "beat", id: b.id, field: "say", value: x.say.trim() });
        const lines = boardLines(x.board);
        if (lines.join("\n") !== (b.board || []).join("\n")) out.push({ target: "beat", id: b.id, field: "board", value: lines });
    }
    for (const h of e.hints) {
        const t = (d.hints[h.level] ?? h.text).trim();
        if (t !== h.text) out.push({ target: "hint", level: h.level, field: "text", value: t });
    }
    const before = e.pronunciations || {};
    const after: Record<string, string> = {};
    for (const p of d.pronunciations) if (p.term.trim()) after[p.term.trim()] = p.reading.trim();
    for (const [term, reading] of Object.entries(after)) if (before[term] !== reading) out.push({ target: "pronunciation", term, reading });
    for (const term of Object.keys(before)) if (!(term in after)) out.push({ target: "pronunciation", term, reading: "" });
    return out;
}

/** 本地校验：只做提示，服务端仍会再校验。 */
export function draftProblems(e: Editable, edits: LessonEdit[]): string[] {
    const p: string[] = [];
    if (edits.length > e.limits.maxEdits) p.push(`一次最多 ${e.limits.maxEdits} 处修改`);
    for (const x of edits) {
        if (x.target === "beat" && x.field === "board") {
            if (x.value.length < 1 || x.value.length > e.limits.maxBoardLines) p.push(`板书需 1 到 ${e.limits.maxBoardLines} 行`);
            if (x.value.some((l) => l.length > e.limits.maxLine)) p.push(`板书每行不超过 ${e.limits.maxLine} 字`);
        } else if (x.target !== "pronunciation" && (!x.value || x.value.length > e.limits.maxText)) {
            p.push(`文字需 1 到 ${e.limits.maxText} 字`);
        }
    }
    return [...new Set(p)];
}

export const draftKey = (lessonId: string, hash: string) => `edu-edit-draft:${lessonId}:${hash}`;

/** 冲突响应里服务端回传的信息（AC-17）：最新修订与本人的修改。 */
export function conflictOf(detail: unknown): { reason: string; latestRevisionId: string; yourEdits: LessonEdit[] } | null {
    const d = detail as { conflict?: { reason?: string; latestRevisionId?: string }; yourEdits?: LessonEdit[] } | null;
    if (!d?.conflict) return null;
    return { reason: d.conflict.reason || "", latestRevisionId: d.conflict.latestRevisionId || "", yourEdits: d.yourEdits || [] };
}

export function estimateText(est: { maxComputeMs?: number; maxCostMicro?: number; unitMicro?: number; priceVersion?: string } | null | undefined): string {
    if (!est) return "";
    if (!est.unitMicro) return `最多 ${Math.round((est.maxComputeMs || 0) / 1000)} 秒计算；当前价目 ${est.priceVersion || ""} 未对计算计费`;
    return `最多 ${Math.round((est.maxComputeMs || 0) / 1000)} 秒计算，费用上限 ${est.maxCostMicro} 微额度（价目 ${est.priceVersion}，按实际用量结算）`;
}
