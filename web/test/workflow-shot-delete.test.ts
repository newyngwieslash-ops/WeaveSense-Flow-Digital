import { expect, test } from "bun:test";
import "./helpers/source-contract";
import { code, codeSlice } from "./helpers/source-contract";

test("镜头编辑器在保存按钮左侧提供带确认的删除操作", async () => {
    const source = await Bun.file(new URL("../src/pages/projects/detail/workflow-production-workbench.tsx", import.meta.url)).text();
    const deleteButton = code(source).indexOf(code(">删除镜头</Button>"));
    const saveButton = code(source).indexOf(code(">保存脚本</Button>"));

    expect(source).toContainCode("deleteProjectShot(projectId, shotId)");
    expect(source).toContainCode("脚本版本、资产引用和生成产物都会被删除");
    expect(source).toContainCode("okButtonProps: { danger: true }");
    expect(deleteButton).toBeGreaterThan(-1);
    expect(saveButton).toBeGreaterThan(deleteButton);
});

test("镜头删除 API 使用项目和镜头双重路径作用域", async () => {
    const source = await Bun.file(new URL("../src/services/api/projects.ts", import.meta.url)).text();

    expect(source).toContainCode("http.delete<{ deleted: boolean }>(`/projects/${encodeURIComponent(projectId)}/shots/${encodeURIComponent(shotId)}`)");
});
