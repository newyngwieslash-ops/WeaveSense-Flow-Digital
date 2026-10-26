/**
 * 交互图形的纯函数部分：只做显示变换（旋转投影、取帧），不在浏览器里做任何数学求解。
 * 所有几何帧都由隔离的数学服务在已校验定义域内预先采样。
 */
export type Xyz = [number, number, number];
export type FigurePoint = { xyz: Xyz; label: string };
export type FigureSpec = {
    kind: "plane2d" | "solid3d";
    extent?: number;
    points: Record<string, FigurePoint>;
    curves?: { pts: [number, number][]; role: string }[];
    edges?: [string, string][];
    aux?: { seg: [string, string]; style: string; role: string }[];
    faces?: { pts: string[]; role: string; label?: string }[];
    explore?: ExploreSpec;
};
export type Prediction = { id: string; prompt: string; options: string[] };
export type ParamFrame = { v: number; points?: Record<string, [number, number]>; degenerate?: string };
export type ExploreSpec =
    | { mode: "rotate"; reset: { yaw: number; pitch: number }; limits: { pitch: [number, number] }; description: string; prediction: Prediction }
    | {
          mode: "param";
          param: { name: string; label: string; unit: string; default: number; min: number; max: number };
          frames: ParamFrame[];
          description: string;
          domain_note?: string;
          prediction: Prediction;
      };

const rad = (d: number) => (d * Math.PI) / 180;

/** 正交投影：先绕竖直轴转 yaw，再按 pitch 俯视；返回屏幕坐标（y 向下）与深度。 */
export function project([x, y, z]: Xyz, yaw: number, pitch: number): { x: number; y: number; depth: number } {
    const cy = Math.cos(rad(yaw));
    const sy = Math.sin(rad(yaw));
    const cp = Math.cos(rad(pitch));
    const sp = Math.sin(rad(pitch));
    const sx = x * cy - y * sy;
    const d = x * sy + y * cy;
    return { x: sx, y: -(z * cp - d * sp), depth: d * cp + z * sp };
}

export function clamp(v: number, lo: number, hi: number) {
    return Math.min(hi, Math.max(lo, v));
}

/** 取与参数值最近的预采样帧；越界值夹到定义域内。 */
export function nearestFrame(frames: ParamFrame[], v: number): ParamFrame | undefined {
    let best: ParamFrame | undefined;
    for (const f of frames) if (!best || Math.abs(f.v - v) < Math.abs(best.v - v)) best = f;
    return best;
}

/** 键盘步进：按帧序移动到相邻帧。 */
export function stepFrame(frames: ParamFrame[], v: number, dir: 1 | -1): number {
    const sorted = [...frames].sort((a, b) => a.v - b.v);
    const cur = nearestFrame(sorted, v);
    const i = cur ? sorted.indexOf(cur) : 0;
    return sorted[clamp(i + dir, 0, sorted.length - 1)]?.v ?? v;
}

/** 计算 viewBox，留出边距；空集合给一个单位框。 */
export function fitBox(pts: { x: number; y: number }[], pad = 0.15): [number, number, number, number] {
    if (!pts.length) return [-1, -1, 2, 2];
    const xs = pts.map((p) => p.x);
    const ys = pts.map((p) => p.y);
    const [x0, x1, y0, y1] = [Math.min(...xs), Math.max(...xs), Math.min(...ys), Math.max(...ys)];
    const s = Math.max(x1 - x0, y1 - y0, 1e-6);
    const m = s * pad;
    return [x0 - m, y0 - m, x1 - x0 + 2 * m, y1 - y0 + 2 * m];
}

/** 逐步讲解的续看位置只保存步骤序号，不保存任何内容。 */
export function stepKey(userId: string, assignmentId: string) {
    return `edu.step.v1:${userId || "anon"}:${assignmentId}`;
}

export function readStep(store: Pick<Storage, "getItem">, key: string, total: number): number {
    const n = Number.parseInt(store.getItem(key) || "0", 10);
    return Number.isFinite(n) ? clamp(n, 0, Math.max(0, total - 1)) : 0;
}
