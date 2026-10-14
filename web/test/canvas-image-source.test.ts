import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { createPortraitTextureNode, isCanvasImageSourceNode } from "../src/lib/canvas/canvas-image-source";
import { canGenerateImageInPlace } from "../src/lib/canvas/canvas-generation-layout";
import { CanvasNodeType, type CanvasNodeData } from "../src/types/canvas";
import { moduleGroupSource } from "./helpers/module-group-source";
import "./helpers/source-contract";
import { code, codeSlice } from "./helpers/source-contract";

const source: CanvasNodeData = {
    id: "uploaded",
    type: CanvasNodeType.Image,
    title: "原图",
    position: { x: 10, y: 20 },
    width: 300,
    height: 200,
    metadata: { content: "original-image", storageKey: "original-key", status: "success" },
};
const read = (path: string) => readFileSync(resolve(import.meta.dir, "../src", path), "utf8");
// 断言源码时忽略换行与缩进：长条件被拆行属于排版变化，不应让契约测试失效。
const flat = (text: string) => text.replace(/\s+/g, " ");

describe("上传图片是输入素材", () => {
    test("上传与导入图片没有生成框，空节点与生成结果仍可配置", () => {
        expect(isCanvasImageSourceNode(source)).toBe(true);
        expect(isCanvasImageSourceNode({ ...source, metadata: {} })).toBe(false);
        expect(isCanvasImageSourceNode({ ...source, metadata: { ...source.metadata, generationType: "edit" } })).toBe(false);
        expect(isCanvasImageSourceNode({ ...source, metadata: { ...source.metadata, taskId: "task" } })).toBe(false);
        expect(isCanvasImageSourceNode({ ...source, type: CanvasNodeType.Video })).toBe(false);
        expect(isCanvasImageSourceNode(null)).toBe(false);
    });
    test("显式复制/版本生成入口保持可用", () => {
        expect(isCanvasImageSourceNode({ ...source, metadata: { ...source.metadata, generationResultPlacement: "replace-node" } })).toBe(false);
        expect(isCanvasImageSourceNode({ ...source, metadata: { ...source.metadata, copiedFromNodeId: "original" } })).toBe(false);
    });
    test("人物质感创建独立编辑节点，不复制原图输出和任务状态", () => {
        const before = structuredClone(source);
        const child = createPortraitTextureNode(source, "texture");
        expect(source).toEqual(before);
        expect(child.id).toBe("texture");
        expect(child.position).toEqual({ x: 406, y: 20 });
        expect(child.metadata?.composerContent).toBe("@图片1");
        expect(child.metadata?.generationType).toBe("edit");
        expect(child.metadata?.content).toBeUndefined();
        expect(child.metadata?.storageKey).toBeUndefined();
        expect(child.metadata?.taskId).toBeUndefined();
        expect(isCanvasImageSourceNode(child)).toBe(false);
        expect(canGenerateImageInPlace(child)).toBe(true);
        expect(canGenerateImageInPlace(source)).toBe(false);
    });
    test("页面和工具入口共享素材判定，人物质感接入原图连线", () => {
        expect(flat(moduleGroupSource("pages/canvas/project.tsx"))).toContainCode("dialogNode && !isCanvasImageSourceNode(dialogNode)");
        expect(read("lib/canvas/tool-registry/definitions/node-hover-tools.tsx")).toContainCode("!isCanvasImageSourceNode(ctx.node)");
        const mediaTools = read("pages/canvas/use-canvas-media-tools.ts");
        const portrait = mediaTools.slice(mediaTools.indexOf("const openPortraitTextureEditor"), mediaTools.indexOf("const cropImageNode"));
        expect(portrait).toContainCode("fromNodeId: node.id, toNodeId: child.id");
        expect(portrait).toContainCode("setDialogNodeId(child.id)");
        expect(portrait).not.toContainCode("current.map");
        expect(mediaTools).toContainCode("payload.generationConfig");
        expect(mediaTools).toContainCode("imageBatchExpanded: requestedCount > 1 ? true : undefined");
        const dialog = read("components/canvas/canvas-node-mask-edit-dialog.tsx");
        expect(dialog).toContainCode("ModelPicker");
        expect(dialog).toContainCode("ImageSettingsPanel");
    });
});

test("底部菜单使用画布浮层管理并响应打开、鼠标和键盘交互", () => {
    const toolbar = read("components/canvas/canvas-toolbar.tsx");
    expect(toolbar).toContainCode('useCanvasOverlayLayer("main-toolbar", "var(--z-toolbar)")');
    expect(toolbar).toContainCode("if (addOpen || appearanceOpen) bringToFront()");
    expect(toolbar).toContainCode("style={{ zIndex }} onPointerDownCapture={bringToFront} onFocusCapture={bringToFront}");
});
