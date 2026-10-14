import { expect, test } from "bun:test";
import "./helpers/source-contract";
import { code, codeSlice } from "./helpers/source-contract";

test("章节页直接生成到分镜制作，并通过共享选择器执行技能", async () => {
    const source = await Bun.file(new URL("../src/pages/projects/detail/chapters.tsx", import.meta.url)).text();
    expect(source).not.toContainCode("导入分镜");
    expect(source).not.toContainCode("importStoryboardToCanvas");
    expect(source).not.toContainCode("在画布中分镜");
    expect(source).toContainCode("生成到分镜制作");
    expect(source).toContainCode("replaceProjectUnitShots");
    expect(source).toContainCode("/storyboard`");
    expect(source).toContainCode('<SkillRuntimePicker profile="shortDrama"');
    expect(code(source).match(/<ModelPickerconfig=\{effectiveConfig\}capability="text"/g)).toHaveLength(2);
    expect(source).toContainCode("chapterAnalysisInput(selectedTextModel)");
    expect(source).toContainCode("const textModel = selectedTextModel");
    expect(source).toContainCode("generateChapterStoryboard");
});

test("镜头画面使用已绑定资产的 @ 引用编辑器", async () => {
    const source = await Bun.file(new URL("../src/pages/projects/detail/workflow-production-workbench.tsx", import.meta.url)).text();
    expect(source).toContainCode('name="plotDescription"');
    expect(source).toContainCode('<ShotAssetMentionTextarea variant="scene"');
    expect(source).toContainCode("resolveShotAssetMentionPrompt(basePrompt, shotAssetReferenceContext");
    expect(source).toContainCode("<Image.PreviewGroup>");
});

test("技能选择器占满表单宽度", async () => {
    const source = await Bun.file(new URL("../src/components/skills/skill-runtime-picker.tsx", import.meta.url)).text();
    expect(source).toContainCode('style={{ width: "100%" }}');
});

test("画布分镜生成统一经过 Skill Runtime", async () => {
    const source = await Bun.file(new URL("../src/pages/canvas/use-canvas-storyboard.ts", import.meta.url)).text();
    expect(source).toContainCode('profile: "shortDrama"');
    expect(source).toContainCode("skillRuntime.prepare");
    expect(source).toContainCode("...skillExecution.metadata");
    expect(source).not.toContainCode("getSkillFile");
    expect(source).not.toContainCode("getSkillBundle");
});
