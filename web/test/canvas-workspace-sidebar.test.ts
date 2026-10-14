import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { canvasDockStyle } from "../src/lib/canvas/canvas-aceternity-style";
import { canvasThemes } from "../src/lib/canvas-theme";
import "./helpers/source-contract";
import { code, codeSlice } from "./helpers/source-contract";

const component = (name: string) => readFileSync(new URL(`../src/components/canvas/${name}`, import.meta.url), "utf8");

test("canvas removes the standalone asset tray while retaining sidebar assets and zoom controls", () => {
    const page = readFileSync(new URL("../src/pages/canvas/project.tsx", import.meta.url), "utf8");
    expect(page).not.toContainCode("CanvasAssetTray");
    expect(page).toContainCode("<CanvasZoomControls");
    expect(page).toContainCode("<CanvasWorkspacePanel");
    expect(component("canvas-workspace-panel.tsx")).toContainCode("<CanvasWorkspaceAssetPanel");
});

test("workspace keeps a permanent rail and independent task/history entries", () => {
    const source = component("canvas-workspace-panel.tsx");
    for (const label of ["主页", "节点", "资产", "任务", "历史"]) expect(source).toContainCode(label);
    expect(source).not.toContainCode('id: "tools"');
    expect(source).not.toContainCode("CanvasWorkspaceToolPanel");
    expect(source).not.toContainCode("onInsertTool");
    expect(source).toContainCode('aria-label="画布左侧菜单"');
    expect(source).toContainCode("aria-pressed={open && tab === id}");
    expect(source).toContainCode("<CanvasWorkspaceHistoryPanel");
    expect(source).toContainCode("<CanvasWorkspaceAssetPanel");
    expect(source).toContainCode("<AppDrawer");
});

test("asset panel uses scoped paginated data and existing insertion contract", () => {
    const source = component("canvas-workspace-asset-panel.tsx");
    expect(source).toContainCode('["canvas-workspace-assets", userId, category, kind, search]');
    expect(source).toContainCode("loadAssetLibraryPage");
    expect(source).toContainCode("assetPickerItemsToInsertPayloads");
    expect(source).toContainCode("query.fetchNextPage()");
    expect(source).toContainCode('role="alert"');
    expect(source).toContainCode("disabled={!insertable || Boolean(inserting)}");
});

test("image style and presets remain visible without the prompt template picker", () => {
    const source = component("canvas-node-prompt-panel.tsx");
    expect(source).toContainCode("canvas-node-tool-controls-inline");
    expect(source).toContainCode("<CanvasChooseImageStylePicker");
    expect(source).toContainCode("<CanvasNineGridPicker");
    expect(source).toContainCode('dense appearance="quiet"');
    expect(source).toContainCode('const showPromptTemplates = !simpleMode && mode !== "image"');
    expect(source).toContainCode("{showPromptTemplates ? <CanvasPresetPicker");
    expect(source).toContainCode("if (showPromptTemplates &&");
    expect(source).not.toContainCode('aria-label="生成工具"');
    expect(component("canvas-choose-image-style-picker.tsx")).toContainCode('resolvedLabel || "风格"');
});

test("history explicitly describes its bounded dataset", () => {
    expect(component("canvas-workspace-history-panel.tsx")).toContainCode("最近 30 条（含 Agent）");
});

test("cleared tool references do not fall back to the original generation prompt", () => {
    const source = readFileSync(new URL("../src/pages/canvas/use-canvas-render-model.ts", import.meta.url), "utf8");
    const toolReferences = source.split("const toolMentionReferencesByNodeId = useMemo")[1].split("const tokens =")[0];
    expect(toolReferences).toContainCode("const text = nodeGenerationPrompt(node);");
    expect(toolReferences).not.toContainCode("||");
});

test("workspace starts collapsed and keeps separated navigation and bottom toggle", () => {
    const page = readFileSync(new URL("../src/pages/canvas/project.tsx", import.meta.url), "utf8");
    expect(page).toContainCode("const [workspaceOpen, setWorkspaceOpen] = useState(false)");
    const panel = component("canvas-workspace-panel.tsx");
    expect(panel).toContainCode('className="canvas-workspace-rail-items"');
    expect(panel.indexOf("canvas-workspace-rail-toggle")).toBeGreaterThan(panel.indexOf("entries.map"));
    const css = component("canvas-workspace-panel.css");
    expect(css).toContainCode("gap: var(--space-1)");
    expect(css).toContainCode("margin-top: auto");
    expect(css).toContainCode("prefers-reduced-motion: reduce");
});

test("rail inherits canvas dock colors from the workspace shell", () => {
    const panel = component("canvas-workspace-panel.tsx");
    expect(panel).toContainCode("canvasThemes[useActiveTheme()]");
    expect(panel).toContainCode('className="canvas-workspace-shell" style={{ ...canvasDockStyle(theme)');
    expect(panel).toContainCode("background: undefined");
    expect(panel).not.toContainCode('boxShadow: "none"');
    const railCss = component("canvas-workspace-panel.css").split(".canvas-workspace-rail {")[1];
    expect(railCss).not.toContainCode("var(--muted-foreground)");
    expect(railCss).not.toContainCode("var(--workspace-navigation)");
    for (const token of ["--dock-command-hover", "--dock-command-active", "--dock-command-active-text"]) expect(railCss).toContainCode(`var(${token})`);
    for (const theme of Object.values(canvasThemes)) {
        const style = canvasDockStyle(theme);
        expect(style.background).toBe("var(--dock-surface)");
        expect(style.color).toBe(theme.toolbar.item);
    }
});

test("editor rail and expanded panel share one restrained glass surface without hard dividers", () => {
    const page = readFileSync(new URL("../src/pages/canvas/project.tsx", import.meta.url), "utf8");
    const panel = component("canvas-workspace-panel.tsx");
    const styles = component("canvas-workspace-panel.css");
    expect(page).toContainCode("<CanvasViewport");
    expect(page).not.toContainCode("canvas-workspace-backdrop");
    expect(page).toContainCode("canvas-main-with-workspace");
    expect(styles).toContainCode(".canvas-main-with-workspace .canvas-editor-shell {\n    z-index: 0;");
    expect(styles).toContainCode(".canvas-workspace-shell {\n    position: absolute;");
    expect(styles).toContainCode("left: var(--canvas-workspace-inset)");
    expect(styles).toContainCode(".canvas-workspace-zoom-controls");
    expect(styles).toContainCode(".canvas-main-with-workspace > .canvas-project-sidebar {\n        margin-left: var(--space-16);");
    expect(styles).toContainCode(".canvas-main-with-workspace:has(> .canvas-project-sidebar) .canvas-topbar");
    expect(panel).not.toContainCode("canvas-workspace-rail-divider");
    expect(panel).not.toContainCode("<strong>工作区</strong>");
    expect(styles).not.toContainCode(".canvas-workspace-rail-divider");
    expect(styles).not.toContainCode("border-right: 1px solid var(--dock-border)");
    expect(styles).not.toContainCode('canvas-workspace-rail-button[aria-pressed="true"]::before');
    expect(styles).toContainCode("margin: var(--space-2) 0 var(--space-2) var(--space-2)");
    expect(styles).toContainCode("--canvas-workspace-material: color-mix(in srgb, var(--canvas-workspace-base, var(--background)) 72%, transparent);");
    expect(styles).not.toContainCode("backdrop-filter: blur(16px)");
    expect(styles).toContainCode("backdrop-filter: blur(22px) saturate(140%);");
    expect(styles).toContainCode(".canvas-workspace-shell .canvas-workspace-panel {\n    background: transparent;");
    expect(styles).toContainCode(".canvas-workspace-panel > :is(header, .border-b) {\n    border-bottom: 0 !important;");
    expect(styles).toContainCode("border-radius: var(--r-2xl)");
    expect(styles).not.toContainCode("border-radius: calc(var(--radius) * 3.5)");
    expect(page).toContainCode('workspaceOpen ? "canvas-workspace-expanded" : ""');
    expect(styles).not.toContainCode("backdrop-filter: none");
    expect(styles).not.toContainCode("box-shadow: none");
});
