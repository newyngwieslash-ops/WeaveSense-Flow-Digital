import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";

import { fitBox, nearestFrame, project, readStep, stepFrame, stepKey, type FigureSpec } from "../src/components/edu/figure-math";
import { InteractiveFigure, PredictionPanel } from "../src/components/edu/interactive-figure";
import { StepViewer } from "../src/components/edu/step-viewer";

const frames = [
    { v: 0, degenerate: "此时直线不存在" },
    { v: 5, points: { A: [1, 1] as [number, number] } },
    { v: 10, points: { A: [2, 2] as [number, number] } },
];

const plane: FigureSpec = {
    kind: "plane2d",
    extent: 3,
    points: { O: { xyz: [0, 0, 0], label: "O" }, A: { xyz: [1, 0, 0], label: "A" } },
    curves: [
        {
            pts: [
                [0, 0],
                [1, 1],
            ],
            role: "ellipse",
        },
    ],
    aux: [{ seg: ["O", "A"], style: "dashed", role: "line" }],
    explore: {
        mode: "param",
        param: { name: "t", label: "参数", unit: "°", default: 5, min: 0, max: 10 },
        frames,
        description: "说明文字",
        domain_note: "定义域说明",
        prediction: { id: "p1", prompt: "先预测", options: ["甲", "说不准"] },
    },
};

describe("figure math", () => {
    test("projection keeps vertical axis at yaw 0 pitch 0", () => {
        const p = project([0, 0, 2], 0, 0);
        expect(p.x).toBeCloseTo(0);
        expect(p.y).toBeCloseTo(-2);
    });
    test("rotation by 90 degrees maps x onto depth", () => {
        const p = project([1, 0, 0], 90, 0);
        expect(p.x).toBeCloseTo(0);
        expect(p.depth).toBeCloseTo(1);
    });
    test("nearest frame and keyboard stepping stay inside the sampled domain", () => {
        expect(nearestFrame(frames, 6)?.v).toBe(5);
        expect(nearestFrame(frames, 99)?.v).toBe(10);
        expect(stepFrame(frames, 10, 1)).toBe(10);
        expect(stepFrame(frames, 5, -1)).toBe(0);
    });
    test("fitBox pads and never collapses", () => {
        const [x, y, w, h] = fitBox([{ x: 0, y: 0 }]);
        expect(w).toBeGreaterThan(0);
        expect(h).toBeGreaterThan(0);
        expect(x).toBeLessThanOrEqual(0);
        expect(y).toBeLessThanOrEqual(0);
    });
    test("step resume stores only an index and clamps it", () => {
        const k = stepKey("u1", "as1");
        expect(k).toBe("edu.step.v1:u1:as1");
        expect(readStep({ getItem: () => "7" }, k, 3)).toBe(2);
        expect(readStep({ getItem: () => "x" }, k, 3)).toBe(0);
        expect(readStep({ getItem: () => null }, k, 3)).toBe(0);
    });
});

describe("figure components", () => {
    test("description renders before the figure and controls are present", () => {
        const html = renderToStaticMarkup(<InteractiveFigure spec={plane} />);
        expect(html.indexOf("说明文字")).toBeLessThan(html.indexOf("<svg"));
        expect(html).toContain('data-testid="figure-slider"');
        expect(html).toContain('data-testid="figure-number"');
        expect(html).toContain('data-testid="figure-reset"');
        expect(html).toContain("定义域说明");
        expect(html).toContain('tabindex="0"');
    });
    test("degenerate frame shows its reason and hides auxiliary lines", () => {
        const spec = { ...plane, explore: { ...plane.explore!, param: { ...(plane.explore as { param: object }).param, default: 0 } } } as FigureSpec;
        const html = renderToStaticMarkup(<InteractiveFigure spec={spec} />);
        expect(html).toContain("此时直线不存在");
        expect(html).not.toContain("stroke-dasharray");
    });
    test("solid figure offers rotation by form and reset", () => {
        const solid: FigureSpec = {
            kind: "solid3d",
            points: { A: { xyz: [0, 0, 0], label: "A" }, B: { xyz: [1, 0, 0], label: "B" } },
            edges: [["A", "B"]],
            explore: { mode: "rotate", reset: { yaw: -35, pitch: 22 }, limits: { pitch: [-80, 80] }, description: "立体说明", prediction: { id: "x", prompt: "?", options: ["a", "b"] } },
        };
        const html = renderToStaticMarkup(<InteractiveFigure spec={solid} />);
        expect(html).toContain('data-testid="figure-yaw"');
        expect(html).toContain('data-testid="figure-pitch"');
        expect(html).toContain('data-yaw="-35"');
    });
    test("disabled figure is not focusable until a prediction is made", () => {
        const html = renderToStaticMarkup(<InteractiveFigure spec={plane} disabled />);
        expect(html).toContain('tabindex="-1"');
    });
    test("prediction panel locks after the first choice", () => {
        const open = renderToStaticMarkup(<PredictionPanel prompt="先预测" options={["甲", "乙"]} onChoose={() => {}} />);
        expect(open).not.toContain('disabled=""');
        const done = renderToStaticMarkup(<PredictionPanel prompt="先预测" options={["甲", "乙"]} chosen={1} onChoose={() => {}} />);
        expect(done).toContain('aria-pressed="true"');
        expect(done).toContain('disabled=""');
        expect(done).toContain("已记录你的预测");
    });
    test("step viewer is silent by default with captions and disabled audio", () => {
        const html = renderToStaticMarkup(
            <StepViewer
                storageKey="k"
                beats={[
                    { id: "s1", title: "第一步", board: ["板书"], say: "字幕内容" },
                    { id: "s2", title: "第二步" },
                ]}
            />,
        );
        expect(html).toContain("静音模式");
        expect(html).toContain("字幕内容");
        expect(html).toMatch(/<button[^>]*disabled[^>]*>播放语音/);
        expect(html.indexOf("板书")).toBeLessThan(html.indexOf("下一步"));
        expect(html).toContain("第 1 / 2 步");
    });
});
