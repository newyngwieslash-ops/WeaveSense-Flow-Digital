import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { moduleGroupSource } from "./helpers/module-group-source";
import "./helpers/source-contract";
import { code, codeSlice } from "./helpers/source-contract";

/**
 * 生产接线回归：模板与模式的领域函数正确，不代表真实入口用上了。
 * 这里锁住「新建必须选模板」「已有场景不弹模板」「时间轴只在动画模式」三条链路。
 */
const workbench = moduleGroupSource("components/canvas/previs/canvas-previs-workbench.tsx");
const dock = readFileSync(resolve(import.meta.dir, "../src/components/canvas/previs/previs-viewport-dock.tsx"), "utf8");
const viewport = moduleGroupSource("components/canvas/previs/previs-viewport.tsx");
const hook = readFileSync(resolve(import.meta.dir, "../src/pages/canvas/use-canvas-previs.ts"), "utf8");
const project = moduleGroupSource("pages/canvas/project.tsx");
const modal = readFileSync(resolve(import.meta.dir, "../src/components/canvas/previs/canvas-previs-template-modal.tsx"), "utf8");
const store = readFileSync(resolve(import.meta.dir, "../src/stores/canvas/use-previs-workbench-store.ts"), "utf8");
const styles = readFileSync(resolve(import.meta.dir, "../src/styles/globals.css"), "utf8");

const slice = codeSlice;

describe("新建场景必须显式选模板", () => {
    test("createPrevisShot 第一个参数是 templateId，没有默认值", () => {
        expect(hook).toContainCode("const createPrevisShot = useCallback((templateId: PrevisTemplateId, position?: Position) => {");
        // 有默认模板等于又回到「无条件塞演员」。
        expect(hook).not.toContainCode("templateId: PrevisTemplateId = ");
    });

    test("新建走模板工厂，不再走带默认演员的 createPrevisScene", () => {
        expect(hook).toContainCode("createPrevisSceneFromTemplate(templateId, `镜头 ${shotIndex}`)");
        expect(hook).not.toContainCode("createPrevisScene(");
    });

    test("两个新建入口都先开模板选择，而不是直接建场景", () => {
        expect(project).toContainCode("onOpenPrevis={() => setPrevisTemplateRequest({})}");
        expect(project).toContainCode("onOpenPrevis={(position) => setPrevisTemplateRequest({ position })}");
        expect(project).not.toContainCode("onOpenPrevis={() => createPrevisShot()}");
        expect(project).not.toContainCode("onOpenPrevis={createPrevisShot}");
    });

    test("选中模板后带着 position 建场景", () => {
        const element = slice(project, "<CanvasPrevisTemplateModal", "/>");
        expect(element).toContainCode("open={Boolean(previsTemplateRequest)}");
        expect(element).toContainCode("onSelect={(templateId) => createPrevisShot(templateId, previsTemplateRequest?.position)}");
    });

    test("模板弹窗把 5 个模板全列出来，没有「默认」快捷项", () => {
        expect(modal).toContainCode("PREVIS_TEMPLATES.map");
        expect(modal).toContainCode("onSelect(template.id)");
        expect(modal).not.toContainCode("默认模板");
    });
});

describe("已有场景不触发模板选择", () => {
    test("openPrevisWorkbench 命中已存在场景时不建新场景、不弹模板", () => {
        const opener = slice(hook, "const openPrevisWorkbench = useCallback", "/** 每次保存都基于 store");
        // 只有找不到场景（孤儿节点修复）才补建。
        expect(opener).toContainCode("if (!scene) {");
        expect(opener).toContainCode('createPrevisSceneFromTemplate("empty"');
        expect(opener).not.toContainCode("setPrevisTemplateRequest");
    });

    test("孤儿修复用空场景兜底：用户没选过就不许塞演员", () => {
        const opener = slice(hook, "const openPrevisWorkbench = useCallback", "/** 每次保存都基于 store");
        expect(opener).not.toContainCode('createPrevisSceneFromTemplate("monologue"');
        expect(opener).not.toContainCode("createPrevisActor");
    });

    test("workbench 自身不含模板选择逻辑：打开已保存场景不改写内容", () => {
        expect(workbench).not.toContainCode("PREVIS_TEMPLATES");
        expect(workbench).not.toContainCode("createPrevisSceneFromTemplate");
    });
});

describe("模式接线", () => {
    test("workbench 从 store 读 mode，并按 capabilities 派生显示", () => {
        expect(workbench).toContainCode("const mode = usePrevisWorkbenchStore((state) => state.mode);");
        expect(workbench).toContainCode("const capabilities = previsModeCapabilities(mode);");
    });

    test("时间轴只在 capabilities.timeline 为真时渲染", () => {
        const timeline = slice(workbench, "{capabilities.timeline ?", "</section>) : null}");
        expect(timeline).toContainCode("<PrevisSequencer");
        expect(workbench.match(/<PrevisSequencer\b/g)).toHaveLength(1);
    });

    test("动画模式把 Transform 轨迹接入视口，隐藏演员和零长度轨迹不显示", () => {
        expect(workbench).toContainCode("showMotionPaths={capabilities.timeline}");
        expect(viewport).toContainCode('object.visible && (object.kind === "actor" || object.primitive === "character")');
        expect(viewport).toContainCode("previsTransformPathLength(object.keyframes) > 0.001");
        expect(viewport).toContainCode("<Line points={points}");
        expect(viewport).toContainCode("interpolatePrevisTransform(sorted[0].transform, sorted, playhead).position");
    });

    test("骨骼/姿势入口只对演员开放，且由 bones 把关", () => {
        const normalizedWorkbench = workbench.replace(/\s+/g, " ");
        expect(normalizedWorkbench).toContainCode('{capabilities.bones && (object.kind === "actor" || object.primitive === "character") ? ( <>');
        // motionClips 不得再作为放行条件：带动画的普通模型不是演员。
        expect(workbench).not.toContainCode('object.primitive === "character" || motionClips.length) ? <>');
    });

    test("姿态模式提供全身与当前骨骼重置，不删除动画轨道", () => {
        const inspector = slice(workbench, "function ObjectInspector(", "function LightInspector(");
        const normalizedInspector = inspector.replace(/\s+/g, " ");
        expect(normalizedInspector).toContainCode('onClick={() => applyPose("stand")}> 重置姿态 </Button>');
        expect(inspector).toContainCode("delete boneOverrides[selectedBoneId]");
        expect(normalizedInspector).toContainCode("> 重置当前骨骼 </Button>");
        expect(inspector).not.toContainCode("boneTracks: []");
    });

    test("动作片段与骨骼入口解耦：任何带 Clip 的对象都能调播放速度/循环", () => {
        expect(workbench.replace(/\s+/g, " ")).toContainCode('{motionClips.length ? ( <> <Field label="动作片段">');
    });

    test("关键帧入口由 keyframes 把关", () => {
        expect(workbench.replace(/\s+/g, " ")).toContainCode("{capabilities.keyframes ? ( <>");
    });

    test("渲染视图下拉按当前模式过滤，而不是写死五项", () => {
        expect(workbench).toContainCode("PREVIS_RENDER_MODE_LABELS.filter((option) => capabilities.renderModes.includes(option.value))");
        expect(workbench).toContainCode("options={renderModeOptions}");
    });

    test("dock 不是绕过模式门控的第二条路径：渲染视图按钮同样按 renderModes 过滤", () => {
        expect(dock).toContainCode("renderModes: PrevisRenderMode[];");
        expect(dock).toContainCode("RENDER_VIEW_BUTTONS.filter((item) => renderModes.includes(item.mode))");
        // 写死的按钮会绕过门控。
        expect(dock).not.toContainCode('onClick={() => onRenderModeChange("pose")}');
        // 工作台当前挂的是 PrevisCanvasDock：它不提供渲染视图入口，唯一入口是已按模式过滤的顶栏下拉。
        const canvasDock = readFileSync(resolve(import.meta.dir, "../src/components/canvas/previs/previs-canvas-dock.tsx"), "utf8");
        expect(workbench).toContainCode("<PrevisCanvasDock");
        expect(canvasDock).not.toContainCode("onRenderModeChange");
        expect(workbench).not.toContainCode("<PrevisViewportDock");
    });

    test("store 层夹住 renderMode：任何路径都无法设置当前模式不允许的视图", () => {
        expect(store).toContainCode("setRenderMode: (renderMode) => set((state) => (previsModeCapabilities(state.mode).renderModes.includes(renderMode) ? { renderMode } : {})),");
    });

    test("摄影机模式固定显示 shot/camera 检查器", () => {
        expect(workbench).toContainCode("selectedObject && !capabilities.cameraTools ?");
        expect(workbench).toContainCode("selectedLight && !capabilities.cameraTools ?");
    });

    test("运镜生成只更新首尾帧并提示到动画模式继续编辑，不清空手工关键帧", () => {
        expect(workbench).toContainCode("resolvePrevisCameraMoveKeyframes(item.keyframes");
        expect(workbench).toContainCode("已更新运镜首尾关键帧，可在动画模式继续编辑");
        expect(workbench).not.toContainCode("keyframes: [{ id: nanoid(), time: 0, transform: start }");
    });

    test("小屏通过 dock 开合属性检查器，姿态与骨骼入口仍可达", () => {
        const workbenchCss = readFileSync(resolve(import.meta.dir, "../src/components/canvas/previs/canvas-previs-workbench.css"), "utf8");
        expect(workbench).toContainCode('className={`pv-panel pv-panel--right thin-scrollbar ${inspectorOpen ? "is-open" : ""}`}');
        expect(workbench).toMatch(/onToggleInspector=\{\(\) =>\s*\(?\s*compactLayout\s*\?\s*setInspectorOpen\(\(value\) => !value\)\s*:\s*setInspectorDocked\(\(value\) => !value\)\s*\)?\s*\}/);
        expect(workbenchCss).toMatch(/\.pv-panel--right\.is-open\s*\{\s*transform:\s*translateX\(0\);\s*\}/);
        expect(workbench).not.toContainCode("border-l max-lg:hidden");
    });

    test("store 的 setMode 走 resolvePrevisModeTransition，清理不靠组件自觉", () => {
        expect(store).toContainCode("setMode: (mode) => set((state) => resolvePrevisModeTransition({ mode, playing: state.playing, autoKey: state.autoKey, renderMode: state.renderMode })),");
    });

    test("mode 不写进 PrevisScene：类型文件里没有 mode 字段", () => {
        const types = readFileSync(resolve(import.meta.dir, "../src/types/previs.ts"), "utf8");
        const sceneType = slice(types, "export type PrevisScene = {", "};");
        expect(sceneType).not.toContainCode("mode");
    });

    test("切模式不重建会话：初始化 effect 的依赖里没有 mode", () => {
        const initEffect = slice(workbench, "// 会话初始化只认 scene id", "// 打开会话时检查合法本地恢复候选");
        // 依赖里出现 mode 就意味着换视图会 resetWorkbench + 清空 history。
        expect(initEffect).toContainCode("}, [open, resetWorkbench, scene, writeDraft]);");
        expect(initEffect).not.toContainCode("mode");
    });

    test("draft/history/save 的生命周期 effect 一律不依赖 mode", () => {
        const recovery = slice(workbench, "// 打开会话时检查合法本地恢复候选", "const activeShot =");
        const pageHide = slice(workbench, "// 离开页面：", "// 卸载兜底：");
        const unmount = slice(workbench, "// 卸载兜底：", "const undo =");
        const shortcuts = slice(workbench, "// 预演台是全屏浮层", "/** 对象 transform 编辑的唯一入口");
        // 锁住各自的依赖数组，同时允许格式器换行和尾逗号。
        expect(recovery).toMatch(/\},\s*\[message,\s*modal,\s*open,\s*scene,\s*writeDraft\],?\s*\);/);
        expect(pageHide).toMatch(/\},\s*\[mirrorDraft,\s*stagedTransaction\],?\s*\);/);
        expect(unmount).toMatch(/\},\s*\[mirrorDraft\],?\s*\);/);
        expect(shortcuts).toMatch(/\},\s*\[open\],?\s*\);/);
    });
});

describe("异步预演台输出使用最新权威状态", () => {
    test("上传后重新核验节点与场景，并把预览引用合入最新 scene", () => {
        expect(hook).toContainCode("const sourceNodeAtStart = nodesRef.current.find");
        expect(hook).toContainCode("const outputProjectId = projectId;");
        expect(hook).toContainCode("projectIdRef.current !== outputProjectId");
        expect(hook).toContainCode("const outputProject = useCanvasStore.getState().projects.find((item) => item.id === outputProjectId);");
        expect(hook).toContainCode("const sourceNode = nodesRef.current.find((item) => item.id === sourceNodeId);");
        expect(hook).toContainCode("const latestScene = outputProject?.previsScenes.find");
        expect(hook).toContainCode("mergePrevisOutputPreview(latestScene");
        expect(hook).toContainCode("savePrevisScene(mergedScene);");
        expect(hook).not.toContainCode("savePrevisScene({ ...output.scene");
    });
});

describe("模式控件可发现、可键盘、小屏可达", () => {
    const nav = slice(workbench, '<nav className="previs-mode-switch"', "</nav>");

    test("是真实 button 且用 aria-pressed 表达当前模式", () => {
        expect(nav).toContainCode('type="button"');
        expect(nav).toContainCode("aria-pressed={mode === item.mode}");
    });

    test("不用 primary 表示普通选中，只加 is-active class", () => {
        expect(nav).toContainCode('className={`previs-mode-switch-button ${mode === item.mode ? "is-active" : ""}`}');
        expect(nav).not.toContainCode('type="primary"');
    });

    test("小屏不隐藏：整个开关没有 max-lg:hidden", () => {
        expect(nav).not.toContainCode("max-lg:hidden");
    });

    test("鼠标点选后释放焦点：否则交互控件守卫会吃掉 W/E/R/Delete", () => {
        expect(nav).toContainCode("releasePrevisFocusAfterPointer(event)");
    });

    test("焦点释放规则集中在共享 helper，不在按钮里各写一遍 blur", () => {
        // 无条件 blur 会破坏键盘 Tab 序列，判据必须留在 helper 内部。
        expect(nav).not.toContainCode("event.currentTarget.blur()");
    });

    test("有可见文字标签与 hint title，不是纯图标", () => {
        expect(nav).toContainCode("{item.label}");
        expect(nav).toContainCode("title={item.hint}");
        expect(nav).toContainCode('aria-label="预演台模式"');
    });

    test("带 data-mode 测试锚点：E2E 不靠中文文案定位模式按钮", () => {
        expect(nav).toContainCode("data-mode={item.mode}");
    });

    test("模式与模板样式只用主题感知语义 token，不新增硬编码颜色", () => {
        const block = slice(styles, "/* 一级模式切换。", ".previs-actor-colors {");
        expect(block).toContainCode("var(--control-selected-bg)");
        expect(block).toContainCode("var(--control-focus-ring)");
        expect(block).not.toMatch(/rgba?\(/);
        expect(block).not.toMatch(/#[0-9a-fA-F]{3,8}/);
    });
});
