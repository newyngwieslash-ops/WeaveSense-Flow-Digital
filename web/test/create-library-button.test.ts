import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { removeCreationAttachment } from "../src/pages/create/creation-assets";
import { moduleGroupSource } from "./helpers/module-group-source";
import "./helpers/source-contract";
import { code, codeSlice } from "./helpers/source-contract";

function compactSource(source: string) {
    return source.replace(/\s+/g, " ").trim();
}

function readCreateSource() {
    return readFileSync(resolve(import.meta.dir, "../src/pages/create/index.tsx"), "utf8");
}

function readCreateWorkspaceSource() {
    return moduleGroupSource("pages/create/creation-workspace.tsx");
}

describe("creation library button", () => {
    test("keeps library selection in the reference area instead of the bottom dock", () => {
        const source = readCreateWorkspaceSource();
        const dockStart = source.indexOf('<footer className="creation-chat-dock">');
        const dockEnd = source.indexOf("</footer>", dockStart);

        expect(dockStart).toBeGreaterThanOrEqual(0);
        expect(dockEnd).toBeGreaterThan(dockStart);
        const dockSource = compactSource(source.slice(dockStart, dockEnd));
        const modePickerIndex = dockSource.indexOf("<ModePicker mode={props.mode}");

        expect(modePickerIndex).toBeGreaterThanOrEqual(0);
        expect(source).not.toContainCode("creation-composer-mode-row");
        expect(dockSource).not.toContainCode('aria-label="打开素材库选择参考内容"');
        expect(dockSource).not.toContainCode('aria-label="从本机上传附件"');
        expect(source).toContainCode("onClick={props.onOpenLibrary}");
        expect(source).toContainCode("creation-reference-add-button");
        expect(source).toContainCode('showSelectedPrice={false} showOptionPrices variant="creation"');
        expect(source).toContainCode("creation-submit-cost");
    });

    test("uploads from the library without adding a reference before confirmation", () => {
        const source = readCreateSource();
        const pickerSource = readFileSync(resolve(import.meta.dir, "../src/components/assets/asset-library-picker-modal.tsx"), "utf8");
        const uploadStart = source.indexOf("const uploadLibraryAssets = async");
        const uploadEnd = source.indexOf("const handleLibrarySelect", uploadStart);

        expect(uploadStart).toBeGreaterThanOrEqual(0);
        expect(uploadEnd).toBeGreaterThan(uploadStart);
        expect(source.slice(uploadStart, uploadEnd)).not.toContainCode("setAttachments");
        expect(source).toContainCode("onUpload: uploadLibraryAssets");
        expect(source).not.toContainCode("onUpload={() => fileInputRef.current?.click()}");
        expect(source).toContainCode("上传后保存到素材库");
        expect(pickerSource).toContainCode("保存完成后会自动选中");
        expect(source).toContainCode("个素材已上传到素材库并自动选中");
    });

    test("视频创作使用同名模型组的全部参考能力开放素材入口", () => {
        const source = readCreateSource();
        const workspace = readCreateWorkspaceSource();

        expect(source).toContainCode('modelGroupReferenceLimits(config, preferredModel || selectedModel, "video")');
        expect(source).toContainCode("reconcileCreationAttachmentLimits(attachments, mentionReferences, videoReferenceLimits)");
        expect(workspace).toContainCode('props.mode !== "video" || props.maxReferences > 0');
        expect(workspace).not.toContainCode('props.videoProfile.operations.includes("image_to_video")');
    });

    test("previews prompt reference images without removing them", () => {
        const createSource = readCreateWorkspaceSource();
        const canvasSource = moduleGroupSource("components/canvas/canvas-node-prompt-panel.tsx");

        expect(createSource).toContainCode('className="creation-user-message-attachments"');
        expect(createSource).toContainCode('setPreviewType(kind === "video" ? "video" : "image")');
        expect(createSource).toContainCode("<CreationMediaPreviewModal url={previewUrl} type={previewType}");
        expect(canvasSource).toContainCode("canPreview ? setImagePreview(reference) : onInsert(reference)");
        expect(canvasSource).toContainCode("<AntImage");
        expect(canvasSource).toContainCode("onClick={() => onInsert(reference)}");
    });

    test("参考内容层叠轨道支持折叠、展开和 Reorder 排序", () => {
        const source = readCreateWorkspaceSource();
        const styles = readFileSync(resolve(import.meta.dir, "../src/styles/globals.css"), "utf8");

        expect(source).toContainCode('import { Reorder, LayoutGroup, motion, useReducedMotion } from "motion/react"');
        expect(source).toContainCode("<Reorder.Group");
        expect(source).toContainCode('axis="x"');
        expect(source).toContainCode("values={visibleAttachments}");
        expect(source).toContainCode("onReorder={reorderVisibleAttachments}");
        expect(source).toContainCode('className="creation-reference-card-remove"');
        expect(source).toContainCode("onPointerDownCapture");
        expect(source).toContainCode("event.stopPropagation()");
        expect(source).toContainCode("<Reorder.Item");
        expect(source).toContainCode('layout="position"');
        expect(source).toContainCode("isExpanded");
        expect(source).toContainCode("setReferencePanelExpanded");
        expect(source).toContainCode("aria-label={`查看全部 ${props.attachments.length} 个参考内容`}");
        expect(source).toContainCode('aria-label="收起素材面板"');
        expect(source).toContainCode("清空全部素材");
        expect(source).toContainCode('role="group"');
        expect(source).toContainCode("aria-pressed={referenceFilter === filter.id}");
        expect(source).toContainCode('{ id: "file", label: "文件", count: referenceCounts.file }');
        expect(source).toContainCode("canDragReferences");
        expect(source).toContainCode("creation-reference-track-wrapper");
        expect(source).toContainCode("creation-reference-stack-card");
        expect(source).toContainCode("creation-reference-add-button");
        expect(source).toContainCode("addReferenceLabel");
        expect(source).toContainCode("aria-busy={interactionBusy}");
        expect(source).toContainCode("disabled={interactionBusy || !canAddMoreReferences}");
        expect(source).toContainCode("creation-reference-track-button");
        expect(source).toContainCode("imageReferenceAtPoint");
        expect(source).toContainCode("setDropTargetReferenceId");
        expect(source).toContainCode("props.onReplaceAttachment(target.attachmentId, item)");
        expect(source).toContainCode("onReferenceFilesDrop=");
        expect(source).toContainCode("CanvasPromptOptimizerDrawer");
        expect(source).toContainCode("promptOptimizerOpen");
        expect(source).toContainCode("provider={props.promptOptimizerProvider}");
        expect(styles).toContainCode(".creation-reference-track");
        expect(styles).toContainCode(".creation-reference-stack-card");
        expect(styles).toContainCode("--stack-rotate: -7deg");
        expect(styles).toContainCode(".creation-reference-track.is-expanded");
        expect(styles).toContainCode(".creation-reference-stack-card:is(:hover, :focus-within) .creation-reference-card-content");
        expect(styles).toContainCode("@media (hover: none)");
        expect(styles).toContainCode(".creation-reference-card-remove { opacity: 1; }");
        expect(styles).not.toContainCode(".creation-reference-track:not(.is-expanded) .creation-reference-stack-card:nth-child(n+5) { display: block; }");
        expect(source).toContainCode("isExpanded: true");
        expect(source).toContainCode("else if (!hadAttachments) setReferencePanelExpanded(true)");
        expect(styles).toContainCode("creation-reference-filter-tabs button.is-active::after");
    });

    test("resolves remote asset images from their stable resource key", () => {
        const assets = readFileSync(resolve(import.meta.dir, "../src/pages/create/creation-assets.ts"), "utf8");
        const createSource = readCreateWorkspaceSource();

        expect(assets).toContainCode("resolveResourceUrl(asset.data.storageKey");
        expect(createSource.replace(/\s+/g, " ")).toContainCode("<CachedResourceImage storageKey={item.storageKey}");
        expect(createSource).toContainCode("resolveResourceUrl(item.storageKey, item.previewUrl)");
    });

    test("首个、中间和末尾参考内容都按稳定 id 独立删除并保留顺序", () => {
        const attachments = [
            { id: "first", name: "首个" },
            { id: "middle", name: "中间" },
            { id: "last", name: "末尾" },
        ];

        expect(removeCreationAttachment(attachments, "first").map((item) => item.id)).toEqual(["middle", "last"]);
        expect(removeCreationAttachment(attachments, "middle").map((item) => item.id)).toEqual(["first", "last"]);
        expect(removeCreationAttachment(attachments, "last").map((item) => item.id)).toEqual(["first", "middle"]);
    });

    test("删除按钮在指针按下阶段隔离拖拽，素材库入口职责独立", () => {
        const source = compactSource(readCreateWorkspaceSource());

        expect(source).toContainCode("onPointerDownCapture={(event) => event.stopPropagation()}");
        expect(source).toContainCode("onRemove(item.id)");
        expect(source).toContainCode("onClick={props.onOpenLibrary}");
        expect(source).not.toContainCode("onClick={() => props.fileInputRef.current?.click()}");
    });
});

describe("creation homepage default mode", () => {
    test("opens the empty homepage on image generation instead of video", () => {
        const source = readCreateSource();
        expect(source).toContainCode("import { defaultCreationMode, modeLabels,");
        expect(source).toContainCode("initialComposerPreferences.mode || defaultCreationMode");
        expect(source).toContainCode("saved.mode || defaultCreationMode");
        expect(source).not.toContainCode('mode || "video"');
    });
});

describe("creation thread chrome", () => {
    test("docks the conversation toolbar into the workspace top bar and keeps a compact thread composer", () => {
        const workspace = readCreateWorkspaceSource();
        const topBar = readFileSync(resolve(import.meta.dir, "../src/components/layout/workspace-top-bar.tsx"), "utf8");
        const product = readFileSync(resolve(import.meta.dir, "../src/styles/workspace-product.css"), "utf8");

        expect(workspace).toContainCode("useWorkspaceTopBarMount");
        expect(workspace).toContainCode("createPortal(toolbar, mount)");
        expect(topBar).toContainCode("WorkspaceTopBarExtensionSlot");
        expect(product).toContainCode(".creation-chat-dock .creation-mode-tabs");
        expect(product).not.toContainCode("creation-composer-mode-row");
    });

    test("parameter popovers use the user surface without a hairline stroke", () => {
        const css = readFileSync(resolve(import.meta.dir, "../src/pages/create/creation-product.css"), "utf8");

        expect(css).toContainCode(".creation-control-popover .ant-popover-inner");
        expect(css).toContainCode("background: var(--user-surface-raised) !important");
        expect(css).toContainCode("border: 0 !important");
        expect(css).toContainCode("--border: transparent");
        expect(css).toContainCode(".creation-choice-grid button.is-selected");
        expect(css).toContainCode("background: var(--user-control-pressed) !important");
    });
});
