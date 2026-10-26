import { Component, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

import { clamp, fitBox, nearestFrame, project, stepFrame, type FigureSpec, type ParamFrame } from "./figure-math";

/**
 * 交互图形（FR-S05，AC-25/26）。支持拖动、键盘与表单三种等价操作，均可重置。
 * 文本描述始终先于图形呈现；渲染失败时降级为文字说明与静态预览，不影响作答。
 * 这里只做显示变换；所有几何帧由隔离的数学服务预先计算。
 */
export type FigureUsage = { drags: number; keys: number; formInputs: number; resets: number; fallbacks: number };
const ctl = "inline-flex h-8 items-center rounded-md border border-stone-300 px-2 text-xs disabled:opacity-40 dark:border-stone-600";

type Props = { spec: FigureSpec; onUse?: (k: keyof FigureUsage) => void; disabled?: boolean };

export function InteractiveFigure({ spec, onUse, disabled }: Props) {
    const ex = spec.explore;
    const isParam = ex?.mode === "param";
    const isRotate = spec.kind === "solid3d";
    const r0 = ex?.mode === "rotate" ? ex.reset : { yaw: -35, pitch: 22 };
    const pitchLim = ex?.mode === "rotate" ? ex.limits.pitch : ([-80, 80] as [number, number]);
    const [yaw, setYaw] = useState(r0.yaw);
    const [pitch, setPitch] = useState(r0.pitch);
    const [v, setV] = useState(isParam ? ex.param.default : 0);
    const drag = useRef<{ x: number; y: number; moved: boolean } | null>(null);
    const use = (k: keyof FigureUsage) => onUse?.(k);
    const frame: ParamFrame | undefined = isParam ? nearestFrame(ex.frames, v) : undefined;

    const reset = () => {
        setYaw(r0.yaw);
        setPitch(r0.pitch);
        if (isParam) setV(ex.param.default);
        use("resets");
    };

    const onKey = (e: React.KeyboardEvent) => {
        if (disabled) return;
        const k = e.key;
        if (isParam && (k === "ArrowRight" || k === "ArrowUp" || k === "ArrowLeft" || k === "ArrowDown")) {
            setV(stepFrame(ex.frames, v, k === "ArrowRight" || k === "ArrowUp" ? 1 : -1));
        } else if (isRotate && (k === "ArrowLeft" || k === "ArrowRight")) {
            setYaw((y) => y + (k === "ArrowRight" ? 10 : -10));
        } else if (isRotate && (k === "ArrowUp" || k === "ArrowDown")) {
            setPitch((p) => clamp(p + (k === "ArrowUp" ? 10 : -10), pitchLim[0], pitchLim[1]));
        } else if (k === "Home" || k === "Escape") {
            reset();
            e.preventDefault();
            return;
        } else return;
        e.preventDefault();
        use("keys");
    };

    const onPointerDown = (e: React.PointerEvent) => {
        if (disabled || !isRotate) return;
        drag.current = { x: e.clientX, y: e.clientY, moved: false };
        (e.target as Element).setPointerCapture?.(e.pointerId);
    };
    const onPointerMove = (e: React.PointerEvent) => {
        const d = drag.current;
        if (!d) return;
        const dx = e.clientX - d.x;
        const dy = e.clientY - d.y;
        if (Math.abs(dx) + Math.abs(dy) < 2) return;
        setYaw((y) => y + dx * 0.6);
        setPitch((p) => clamp(p + dy * 0.6, pitchLim[0], pitchLim[1]));
        drag.current = { x: e.clientX, y: e.clientY, moved: true };
    };
    const onPointerUp = () => {
        if (drag.current?.moved) use("drags");
        drag.current = null;
    };

    const geo = useMemo(() => {
        const pts: Record<string, { x: number; y: number; depth: number; label: string }> = {};
        for (const [k, p] of Object.entries(spec.points)) {
            if (spec.kind === "solid3d") pts[k] = { ...project(p.xyz, yaw, pitch), label: p.label };
            else pts[k] = { x: p.xyz[0], y: -p.xyz[1], depth: 0, label: p.label };
        }
        if (frame?.points) for (const [k, [x, y]] of Object.entries(frame.points)) pts[k] = { x, y: -y, depth: 0, label: spec.points[k]?.label || k };
        const curves = (spec.curves || []).map((c) => c.pts.map(([x, y]) => `${x},${-y}`).join(" "));
        let box: [number, number, number, number];
        if (spec.kind === "plane2d" && spec.extent) box = [-spec.extent, -spec.extent, 2 * spec.extent, 2 * spec.extent];
        else box = fitBox(Object.values(pts));
        return { pts, curves, box };
    }, [spec, yaw, pitch, frame]);

    const s = geo.box[2];
    const font = s * 0.045;
    const seg = (a: string, b: string) => (geo.pts[a] && geo.pts[b] ? { x1: geo.pts[a].x, y1: geo.pts[a].y, x2: geo.pts[b].x, y2: geo.pts[b].y } : null);
    const degenerate = frame?.degenerate;
    const hideAux = Boolean(degenerate);

    return (
        <div data-testid="interactive-figure">
            {ex?.description ? (
                <p className="mb-2 text-sm leading-6" data-testid="figure-description">
                    {ex.description}
                </p>
            ) : null}
            <svg
                role="application"
                aria-label={ex?.description || "图形"}
                aria-roledescription={isRotate ? "可旋转图形，方向键旋转，Home 重置" : "可调参数图形，方向键改变参数，Home 重置"}
                tabIndex={disabled ? -1 : 0}
                viewBox={geo.box.join(" ")}
                className="h-72 w-full touch-none select-none rounded-md border bg-white outline-none focus-visible:ring-2 focus-visible:ring-sky-500 dark:bg-stone-950"
                onKeyDown={onKey}
                onPointerDown={onPointerDown}
                onPointerMove={onPointerMove}
                onPointerUp={onPointerUp}
                onPointerCancel={onPointerUp}
                data-testid="figure-svg"
                data-yaw={Math.round(yaw)}
                data-pitch={Math.round(pitch)}
                data-param={isParam ? v : undefined}
            >
                {geo.curves.map((c, i) => (
                    <polyline key={`c${i}`} points={c} fill="none" stroke="currentColor" strokeWidth={1.5} vectorEffect="non-scaling-stroke" className="text-stone-500" />
                ))}
                {(spec.faces || []).map((f, i) => (
                    <polygon key={`f${i}`} points={f.pts.map((k) => (geo.pts[k] ? `${geo.pts[k].x},${geo.pts[k].y}` : "")).join(" ")} className="fill-sky-300/30 stroke-none" />
                ))}
                {(spec.edges || []).map(([a, b], i) => {
                    const l = seg(a, b);
                    return l ? <line key={`e${i}`} {...l} stroke="currentColor" strokeWidth={1.5} vectorEffect="non-scaling-stroke" /> : null;
                })}
                {hideAux
                    ? null
                    : (spec.aux || []).map((a, i) => {
                          const l = seg(a.seg[0], a.seg[1]);
                          return l ? (
                              <line key={`a${i}`} {...l} stroke="currentColor" strokeWidth={1.5} strokeDasharray={a.style === "dotted" ? "2 3" : "6 4"} vectorEffect="non-scaling-stroke" className={a.role === "line" ? "text-rose-600" : "text-stone-400"} />
                          ) : null;
                      })}
                {Object.entries(geo.pts).map(([k, p]) => (
                    <g key={k}>
                        <circle cx={p.x} cy={p.y} r={s * 0.008} className="fill-current" />
                        <text x={p.x + font * 0.3} y={p.y - font * 0.3} fontSize={font} className="fill-current">
                            {p.label}
                        </text>
                    </g>
                ))}
            </svg>
            {degenerate ? (
                <p className="mt-1 text-xs text-amber-700 dark:text-amber-300" data-testid="figure-degenerate">
                    {degenerate}
                </p>
            ) : null}
            <div className="mt-2 flex flex-wrap items-center gap-2 text-xs">
                {isParam ? (
                    <>
                        <label className="flex items-center gap-2">
                            {ex.param.label}
                            <input
                                type="range"
                                min={ex.param.min}
                                max={ex.param.max}
                                step="any"
                                value={v}
                                disabled={disabled}
                                onChange={(e) => {
                                    setV(nearestFrame(ex.frames, Number(e.target.value))?.v ?? v);
                                }}
                                onPointerUp={() => use("drags")}
                                aria-label={ex.param.label}
                                data-testid="figure-slider"
                            />
                        </label>
                        <input
                            type="number"
                            className="h-8 w-20 rounded-md border px-1"
                            min={ex.param.min}
                            max={ex.param.max}
                            value={v}
                            disabled={disabled}
                            aria-label={`${ex.param.label}（输入数值）`}
                            onChange={(e) => {
                                const n = Number(e.target.value);
                                if (Number.isFinite(n)) {
                                    setV(nearestFrame(ex.frames, clamp(n, ex.param.min, ex.param.max))?.v ?? v);
                                    use("formInputs");
                                }
                            }}
                            data-testid="figure-number"
                        />
                        <span>{ex.param.unit}</span>
                    </>
                ) : isRotate ? (
                    <>
                        <label className="flex items-center gap-1">
                            水平角
                            <input type="number" className="h-8 w-16 rounded-md border px-1" value={Math.round(yaw)} disabled={disabled} onChange={(e) => (setYaw(Number(e.target.value) || 0), use("formInputs"))} data-testid="figure-yaw" />
                        </label>
                        <label className="flex items-center gap-1">
                            俯角
                            <input
                                type="number"
                                className="h-8 w-16 rounded-md border px-1"
                                value={Math.round(pitch)}
                                min={pitchLim[0]}
                                max={pitchLim[1]}
                                disabled={disabled}
                                onChange={(e) => (setPitch(clamp(Number(e.target.value) || 0, pitchLim[0], pitchLim[1])), use("formInputs"))}
                                data-testid="figure-pitch"
                            />
                        </label>
                    </>
                ) : null}
                {isParam || isRotate ? (
                    <button type="button" className={ctl} onClick={reset} disabled={disabled} data-testid="figure-reset">
                        重置
                    </button>
                ) : null}
            </div>
            {isParam && ex.domain_note ? <p className="mt-1 text-xs text-stone-500">{ex.domain_note}</p> : null}
        </div>
    );
}

/** 渲染失败时降级为文字说明与静态内容，并上报一次 fallback。 */
export class FigureBoundary extends Component<{ fallback: ReactNode; onFallback?: () => void; children: ReactNode }, { failed: boolean }> {
    state = { failed: false };
    static getDerivedStateFromError() {
        return { failed: true };
    }
    componentDidCatch() {
        this.props.onFallback?.();
    }
    render() {
        return this.state.failed ? this.props.fallback : this.props.children;
    }
}

/** 先预测再探索：首次选择即记录，之后不可修改；“说不准”同样是有效选择。 */
export function PredictionPanel({ prompt, options, chosen, onChoose, disabled }: { prompt: string; options: string[]; chosen?: number; onChoose: (i: number) => void; disabled?: boolean }) {
    return (
        <fieldset className="mb-3 rounded-md border border-dashed border-stone-300 p-3 text-sm dark:border-stone-600" data-testid="prediction-panel">
            <legend className="px-1 font-medium">{prompt}</legend>
            <div className="flex flex-wrap gap-2">
                {options.map((o, i) => (
                    <button
                        key={o}
                        type="button"
                        className={`${ctl} ${chosen === i ? "border-sky-600 bg-sky-50 dark:bg-sky-950" : ""}`}
                        disabled={disabled || chosen !== undefined}
                        aria-pressed={chosen === i}
                        onClick={() => onChoose(i)}
                        data-testid={`prediction-option-${i}`}
                    >
                        {o}
                    </button>
                ))}
            </div>
            {chosen !== undefined ? (
                <p className="mt-2 text-xs text-stone-500" data-testid="prediction-recorded">
                    已记录你的预测，现在可以动手探索验证。
                </p>
            ) : (
                <p className="mt-2 text-xs text-stone-500">先选一个预测，再操作图形。</p>
            )}
        </fieldset>
    );
}

/** 每 15 秒及离开页面时汇总上报图形使用次数（仅计数，不作为学习证据）。 */
export function useFigureUsage(report: (u: FigureUsage) => void) {
    const acc = useRef<FigureUsage>({ drags: 0, keys: 0, formInputs: 0, resets: 0, fallbacks: 0 });
    const rep = useRef(report);
    rep.current = report;
    useEffect(() => {
        const flush = () => {
            const u = acc.current;
            if (u.drags + u.keys + u.formInputs + u.resets + u.fallbacks === 0) return;
            acc.current = { drags: 0, keys: 0, formInputs: 0, resets: 0, fallbacks: 0 };
            rep.current(u);
        };
        const t = setInterval(flush, 15000);
        return () => (clearInterval(t), flush());
    }, []);
    return (k: keyof FigureUsage) => {
        acc.current[k] = Math.min(500, acc.current[k] + 1);
    };
}
