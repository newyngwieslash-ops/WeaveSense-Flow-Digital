import { expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { AgentMessageEditControls } from "@/components/canvas/canvas-agent-message-edit-controls";
import { deleteConversationMessage, editConversationMessage, mergeDocuments } from "@/services/cloud-agent-conversations";

test("sidebar messages expose edit and delete controls only when handlers are given", () => {
    const item = { id: "m1", role: "user" as const, text: "hello" };
    const html = renderToStaticMarkup(<AgentMessageEditControls item={item} onEdit={() => {}} onDelete={() => {}} />);
    expect(html).toContain('data-agent-message-actions="m1"');
    expect(html).toContain("编辑");
    expect(html).toContain("删除");
    expect(renderToStaticMarkup(<AgentMessageEditControls item={item} onDelete={() => {}} />)).not.toContain("编辑");
});

test("a sidebar delete is a tombstone that survives a stale server copy; edits win by recency", () => {
    const m = (id: string, text = id) => ({ id, role: "user" as const, text });
    const base = { id: "c", title: "c", run: null, permissionMode: "auto" as const, createdAt: "2026-10-13T00:00:00Z", updatedAt: "2026-10-13T00:00:00Z", messages: [m("m1"), m("dir_x")] };
    const local = editConversationMessage(deleteConversationMessage(base, "dir_x"), "m1", "edited");
    const merged = mergeDocuments({ version: 1, activeId: "c", conversations: [base] }, { version: 1, activeId: "c", conversations: [local] });
    expect(merged.conversations[0].messages.map((x) => x.text)).toEqual(["edited"]);
    expect(merged.conversations[0].deletedIds).toEqual(["dir_x"]);
});
