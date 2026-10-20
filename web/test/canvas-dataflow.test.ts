import { describe, expect, it } from "bun:test";

import { buildNodeGenerationContext } from "@/components/canvas/canvas-node-generation";
import {
    evaluateCanvasDataflow,
    findBlockedLogicBranchInput,
    isLogicDualInput,
    isLogicDualOutput,
    logicNodeHandleAtY,
    logicNodeHandleY,
    parseDataSourceNode,
} from "@/lib/canvas/dataflow-evaluator";
import { CanvasNodeType, type CanvasConnection, type CanvasNodeData } from "@/types/canvas";

function makeNode(partial: Partial<CanvasNodeData> & { id: string; type: CanvasNodeData["type"] }): CanvasNodeData {
    return {
        title: partial.id,
        position: { x: 0, y: 0 },
        width: 400,
        height: 320,
        ...partial,
    };
}

describe("canvas low-code dataflow & logic evaluator", () => {
    it("parses JSON, CSV, Markdown, TXT, and Intel data source formats", () => {
        const jsonNode = makeNode({
            id: "ds-json",
            type: CanvasNodeType.DataSource,
            metadata: {
                dataSource: {
                    format: "json",
                    rawText: JSON.stringify([
                        { id: "a1", title: "赛博朋克雨夜", score: 92 },
                        { id: "a2", title: "古风客栈", score: 75 },
                    ]),
                },
            },
        });
        const jsonParsed = parseDataSourceNode(jsonNode);
        expect(jsonParsed.error).toBeUndefined();
        expect(jsonParsed.items).toHaveLength(2);
        expect(jsonParsed.items[0]).toEqual({ id: "a1", title: "赛博朋克雨夜", score: 92 });

        const csvNode = makeNode({
            id: "ds-csv",
            type: CanvasNodeType.DataSource,
            metadata: {
                dataSource: {
                    format: "csv",
                    rawText: 'id,title,score,featured\nb1,"霓虹街角, 特写",88,true\nb2,废土公路,61,false',
                },
            },
        });
        const csvParsed = parseDataSourceNode(csvNode);
        expect(csvParsed.items).toEqual([
            { id: "b1", title: "霓虹街角, 特写", score: 88, featured: true },
            { id: "b2", title: "废土公路", score: 61, featured: false },
        ]);

        const mdNode = makeNode({
            id: "ds-md",
            type: CanvasNodeType.DataSource,
            metadata: {
                dataSource: {
                    format: "markdown",
                    rawText: "- 镜头一：主角推门而入\n- 镜头二：俯拍长廊追逐",
                },
            },
        });
        const mdParsed = parseDataSourceNode(mdNode);
        expect(mdParsed.items).toEqual([
            { id: "1", text: "镜头一：主角推门而入", title: "镜头一：主角推门而入" },
            { id: "2", text: "镜头二：俯拍长廊追逐", title: "镜头二：俯拍长廊追逐" },
        ]);

        const txtNode = makeNode({
            id: "ds-txt",
            type: CanvasNodeType.DataSource,
            metadata: {
                dataSource: {
                    format: "txt",
                    rawText: "第一条线索\n\n第二条线索",
                },
            },
        });
        expect(parseDataSourceNode(txtNode).items).toEqual(["第一条线索", "第二条线索"]);
    });

    it("evaluates set operations (intersect, union, difference) across input_a and input_b", () => {
        const sourceA = makeNode({
            id: "source-a",
            type: CanvasNodeType.DataSource,
            metadata: {
                dataSource: {
                    format: "json",
                    rawText: JSON.stringify([
                        { id: "k1", name: "角色甲" },
                        { id: "k2", name: "角色乙" },
                    ]),
                },
            },
        });
        const sourceB = makeNode({
            id: "source-b",
            type: CanvasNodeType.DataSource,
            metadata: {
                dataSource: {
                    format: "json",
                    rawText: JSON.stringify([
                        { id: "k2", name: "角色乙（B源）" },
                        { id: "k3", name: "角色丙" },
                    ]),
                },
            },
        });
        const intersectNode = makeNode({
            id: "logic-intersect",
            type: CanvasNodeType.Logic,
            metadata: { logic: { operator: "intersect", matchKey: "id" } },
        });
        const unionNode = makeNode({
            id: "logic-union",
            type: CanvasNodeType.Logic,
            metadata: { logic: { operator: "union", matchKey: "id" } },
        });
        const diffNode = makeNode({
            id: "logic-diff",
            type: CanvasNodeType.Logic,
            metadata: { logic: { operator: "difference", matchKey: "id" } },
        });

        const connections: CanvasConnection[] = [
            { id: "c1", fromNodeId: "source-a", toNodeId: "logic-intersect", toHandleId: "input_a" },
            { id: "c2", fromNodeId: "source-b", toNodeId: "logic-intersect", toHandleId: "input_b" },
            { id: "c3", fromNodeId: "source-a", toNodeId: "logic-union", toHandleId: "input_a" },
            { id: "c4", fromNodeId: "source-b", toNodeId: "logic-union", toHandleId: "input_b" },
            { id: "c5", fromNodeId: "source-a", toNodeId: "logic-diff", toHandleId: "input_a" },
            { id: "c6", fromNodeId: "source-b", toNodeId: "logic-diff", toHandleId: "input_b" },
        ];

        const evalResult = evaluateCanvasDataflow([sourceA, sourceB, intersectNode, unionNode, diffNode], connections);
        expect(evalResult.nodes.get("logic-intersect")?.outputItems).toEqual([{ id: "k2", name: "角色乙" }]);
        expect(evalResult.nodes.get("logic-union")?.outputItems).toEqual([
            { id: "k1", name: "角色甲" },
            { id: "k2", name: "角色乙" },
            { id: "k3", name: "角色丙" },
        ]);
        expect(evalResult.nodes.get("logic-diff")?.outputItems).toEqual([{ id: "k1", name: "角色甲" }]);
    });

    it("splits if_else into True/False ports, extracts nested fields, and gates downstream generation", () => {
        const source = makeNode({
            id: "source-1",
            type: CanvasNodeType.DataSource,
            metadata: {
                dataSource: {
                    format: "json",
                    rawText: JSON.stringify([
                        { id: "1", prompt: "高分机甲战斗镜头", meta: { score: 95 } },
                        { id: "2", prompt: "高分霓虹追逐镜头", meta: { score: 88 } },
                    ]),
                },
            },
        });
        const ifNode = makeNode({
            id: "logic-if",
            title: "高分筛选",
            type: CanvasNodeType.Logic,
            metadata: {
                logic: {
                    operator: "if_else",
                    fieldPath: "meta.score",
                    comparator: "gt",
                    compareValue: "80",
                },
            },
        });
        const extractNode = makeNode({
            id: "logic-extract",
            type: CanvasNodeType.Logic,
            metadata: {
                logic: {
                    operator: "json_extract",
                    fieldPath: "prompt",
                },
            },
        });
        const trueImageNode = makeNode({
            id: "img-true",
            type: CanvasNodeType.Image,
            metadata: { prompt: "请按命中数据生成画面" },
        });
        const falseImageNode = makeNode({
            id: "img-false",
            type: CanvasNodeType.Image,
            metadata: { prompt: "未命中备选分支" },
        });

        const nodes = [source, ifNode, extractNode, trueImageNode, falseImageNode];
        const connections: CanvasConnection[] = [
            { id: "e1", fromNodeId: "source-1", toNodeId: "logic-if" },
            { id: "e2", fromNodeId: "logic-if", fromHandleId: "true", toNodeId: "logic-extract" },
            { id: "e3", fromNodeId: "logic-extract", toNodeId: "img-true" },
            { id: "e4", fromNodeId: "logic-if", fromHandleId: "false", toNodeId: "img-false" },
        ];

        const dataflow = evaluateCanvasDataflow(nodes, connections);
        expect(dataflow.getEdgeItems("logic-if", "true").items).toHaveLength(2);
        expect(dataflow.getEdgeItems("logic-if", "false").items).toHaveLength(0);
        expect(dataflow.getEdgeText("logic-extract")).toBe("高分机甲战斗镜头\n高分霓虹追逐镜头");

        // True 分支激活：下游生成节点自动接收提取出的文本
        const trueContext = buildNodeGenerationContext("img-true", nodes, connections, "请按命中数据生成画面", []);
        expect(trueContext.prompt).toContain("高分机甲战斗镜头\n高分霓虹追逐镜头");
        expect(findBlockedLogicBranchInput("img-true", nodes, connections)).toBeNull();

        // False 分支为空（0 条命中）：门控拦截下游生成
        const blocked = findBlockedLogicBranchInput("img-false", nodes, connections);
        expect(blocked).not.toBeNull();
        expect(blocked?.reason).toContain("False 分支当前未命中");
        expect(() => buildNodeGenerationContext("img-false", nodes, connections, "未命中备选分支", [])).toThrow(
            "逻辑节点「高分筛选」False 分支当前未命中（0 条数据），已跳过下游生成",
        );
    });

    it("detects cyclic connections safely without infinite recursion", () => {
        const logicA = makeNode({
            id: "logic-a",
            type: CanvasNodeType.Logic,
            metadata: { logic: { operator: "filter", comparator: "not_empty" } },
        });
        const logicB = makeNode({
            id: "logic-b",
            type: CanvasNodeType.Logic,
            metadata: { logic: { operator: "filter", comparator: "not_empty" } },
        });
        const connections: CanvasConnection[] = [
            { id: "c-ab", fromNodeId: "logic-a", toNodeId: "logic-b" },
            { id: "c-ba", fromNodeId: "logic-b", toNodeId: "logic-a" },
        ];

        const res = evaluateCanvasDataflow([logicA, logicB], connections);
        expect(res.nodes.get("logic-a")?.cycleDetected).toBe(true);
        expect(res.nodes.get("logic-a")?.error).toContain("循环连线");
    });

    it("computes dual input/output port geometry for logic nodes", () => {
        const intersectNode = makeNode({
            id: "logic-set",
            type: CanvasNodeType.Logic,
            position: { x: 100, y: 200 },
            width: 400,
            height: 300,
            metadata: { logic: { operator: "intersect" } },
        });
        const branchNode = makeNode({
            id: "logic-branch",
            type: CanvasNodeType.Logic,
            position: { x: 100, y: 200 },
            width: 400,
            height: 300,
            metadata: { logic: { operator: "if_else" } },
        });

        expect(isLogicDualInput(intersectNode)).toBe(true);
        expect(isLogicDualOutput(intersectNode)).toBe(false);
        expect(isLogicDualInput(branchNode)).toBe(false);
        expect(isLogicDualOutput(branchNode)).toBe(true);

        const topY = logicNodeHandleY(intersectNode, "input_a")!;
        const bottomY = logicNodeHandleY(intersectNode, "input_b")!;
        expect(topY).toBeCloseTo(200 + 300 * 0.38);
        expect(bottomY).toBeCloseTo(200 + 300 * 0.72);
        expect(logicNodeHandleAtY(intersectNode, "target", topY + 5, 40)).toBe("input_a");
        expect(logicNodeHandleAtY(intersectNode, "target", bottomY - 5, 40)).toBe("input_b");
        expect(logicNodeHandleAtY(branchNode, "source", topY + 4, 40)).toBe("true");
        expect(logicNodeHandleAtY(branchNode, "source", bottomY - 4, 40)).toBe("false");
    });

    it("evaluates data-source -> logic -> skill -> image pipeline and gates empty branch through skill", () => {
        const source = makeNode({
            id: "ds-intel",
            type: CanvasNodeType.DataSource,
            metadata: {
                dataSource: {
                    format: "json",
                    rawText: JSON.stringify([
                        { id: "doc_1", title: "固态电池量产突破", score: 92 },
                    ]),
                },
            },
        });
        const logicNode = makeNode({
            id: "logic-filter",
            title: "高优情报分流",
            type: CanvasNodeType.Logic,
            metadata: {
                logic: {
                    operator: "if_else",
                    fieldPath: "score",
                    comparator: "gt",
                    compareValue: "85",
                },
            },
        });
        const trueSkillNode = makeNode({
            id: "skill-true",
            title: "分镜视觉提炼",
            type: CanvasNodeType.Skill,
            metadata: {
                content: "将输入情报转化为赛博朋克风格实验室特写镜头提示词",
                skillName: "分镜视觉提炼",
                skillDescription: "提炼高张力商业分镜画面",
                skillPrompts: ["将输入情报转化为赛博朋克风格实验室特写镜头提示词"],
            },
        });
        const falseSkillNode = makeNode({
            id: "skill-false",
            title: "次要情报摘要",
            type: CanvasNodeType.Skill,
            metadata: {
                content: "生成简短文字海报背景",
                skillName: "次要情报摘要",
            },
        });
        const trueImage = makeNode({
            id: "img-from-skill-true",
            type: CanvasNodeType.Image,
            metadata: { prompt: "生成主视觉海报" },
        });
        const falseImage = makeNode({
            id: "img-from-skill-false",
            type: CanvasNodeType.Image,
            metadata: { prompt: "生成备选海报" },
        });

        const nodes = [source, logicNode, trueSkillNode, falseSkillNode, trueImage, falseImage];
        const connections: CanvasConnection[] = [
            { id: "c1", fromNodeId: "ds-intel", toNodeId: "logic-filter" },
            { id: "c2", fromNodeId: "logic-filter", fromHandleId: "true", toNodeId: "skill-true" },
            { id: "c3", fromNodeId: "skill-true", toNodeId: "img-from-skill-true" },
            { id: "c4", fromNodeId: "logic-filter", fromHandleId: "false", toNodeId: "skill-false" },
            { id: "c5", fromNodeId: "skill-false", toNodeId: "img-from-skill-false" },
        ];

        const evalResult = evaluateCanvasDataflow(nodes, connections);
        const skillTrueText = evalResult.getEdgeText("skill-true");
        expect(skillTrueText).toContain("【技能：分镜视觉提炼】");
        expect(skillTrueText).toContain("将输入情报转化为赛博朋克风格实验室特写镜头提示词");
        expect(skillTrueText).toContain("固态电池量产突破");

        const trueContext = buildNodeGenerationContext("img-from-skill-true", nodes, connections, "生成主视觉海报", []);
        expect(trueContext.prompt).toContain("【技能：分镜视觉提炼】");
        expect(trueContext.prompt).toContain("固态电池量产突破");

        // False branch (0 items) -> falseSkillNode -> falseImage must be gated
        expect(evalResult.getEdgeItems("skill-false").active).toBe(false);
        const blocked = findBlockedLogicBranchInput("img-from-skill-false", nodes, connections);
        expect(blocked).not.toBeNull();
        expect(blocked?.reason).toContain("False 分支当前未命中");
    });
});
