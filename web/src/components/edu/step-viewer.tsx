import { useEffect, useState } from "react";

import { readStep } from "./figure-math";

/**
 * 逐步讲解（AC-26）：上一步/下一步与步骤列表，字幕取自讲解文字。
 * 当前没有语音合成，默认静音模式：所有内容以文字与字幕呈现，音频控件不可用并注明原因。
 * 续看只在本地保存步骤序号。
 */
export type Beat = { id: string; title: string; board?: string | string[]; say?: string };
const ctl = "inline-flex h-8 items-center rounded-md border border-stone-300 px-2 text-xs disabled:opacity-40 dark:border-stone-600";

export function StepViewer({ beats, storageKey }: { beats: Beat[]; storageKey: string }) {
    const [i, setI] = useState(() => (typeof localStorage === "undefined" ? 0 : readStep(localStorage, storageKey, beats.length)));
    useEffect(() => {
        try {
            localStorage.setItem(storageKey, String(i));
        } catch {
            /* 本地存储不可用时只是不能续看 */
        }
    }, [i, storageKey]);
    if (!beats.length) return null;
    const b = beats[Math.min(i, beats.length - 1)];
    const board = Array.isArray(b.board) ? b.board : b.board ? [b.board] : [];
    return (
        <div className="mt-2 rounded-md border border-stone-200 p-3 dark:border-stone-700" data-testid="step-viewer" data-step={i}>
            <div className="mb-2 flex flex-wrap items-center gap-2 text-xs">
                <span className="rounded bg-stone-100 px-2 py-1 dark:bg-stone-800" data-testid="silent-mode">
                    静音模式
                </span>
                <button type="button" className={ctl} disabled title="暂无可用语音，内容已全部以文字和字幕呈现">
                    播放语音（暂不可用）
                </button>
                <span className="text-stone-500">
                    第 {i + 1} / {beats.length} 步
                </span>
            </div>
            <h3 className="font-medium" data-testid="step-title">
                {b.title}
            </h3>
            {board.map((line, k) => (
                <p key={k} className="whitespace-pre-wrap text-sm leading-7">
                    {line}
                </p>
            ))}
            {b.say ? (
                <p className="mt-2 rounded bg-stone-900/80 px-2 py-1 text-sm text-white" role="note" aria-label="字幕" data-testid="step-caption">
                    {b.say}
                </p>
            ) : null}
            <div className="mt-3 flex gap-2">
                <button type="button" className={ctl} disabled={i === 0} onClick={() => setI(i - 1)} data-testid="step-prev">
                    上一步
                </button>
                <button type="button" className={ctl} disabled={i >= beats.length - 1} onClick={() => setI(i + 1)} data-testid="step-next">
                    下一步
                </button>
            </div>
            <ol className="mt-3 list-decimal space-y-1 pl-5 text-xs">
                {beats.map((x, k) => (
                    <li key={x.id}>
                        <button type="button" className={k === i ? "font-semibold underline" : "underline-offset-2 hover:underline"} aria-current={k === i ? "step" : undefined} onClick={() => setI(k)}>
                            {x.title}
                        </button>
                    </li>
                ))}
            </ol>
        </div>
    );
}
