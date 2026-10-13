import { describe, expect, it } from "bun:test";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

// The canvas agent conversation directory is stored server-side (cross-device);
// the local store is only a cache. Transport and storage are stubbed modules.
const dir = mkdtempSync(join(tmpdir(), "canvas-conv-server-"));
const root = new URL("../src/", import.meta.url);
writeFileSync(join(dir, "request.ts"), "export const http: any = {};");
writeFileSync(
    join(dir, "storage.ts"),
    "export const data = new Map(); export const localForageStorageForScope = () => ({ getItem: async (k) => data.get(k) ?? null, setItem: async (k,v) => {data.set(k,v);}, removeItem: async (k) => {data.delete(k);} });",
);
writeFileSync(join(dir, "scope.ts"), 'export const getActiveUserScope = () => "test-user";');
writeFileSync(
    join(dir, "conversations.ts"),
    readFileSync(new URL("services/cloud-agent-conversations.ts", root), "utf8")
        .replace('"@/services/api/request"', JSON.stringify(join(dir, "request.ts")))
        .replace('"@/lib/localforage-storage"', JSON.stringify(join(dir, "storage.ts")))
        .replace('"@/lib/user-scope"', JSON.stringify(join(dir, "scope.ts")))
        .replace('"@/lib/markdown-plain-text"', JSON.stringify(fileURLToPath(new URL("lib/markdown-plain-text.ts", root)))),
);
const mod = await import(join(dir, "conversations.ts"));
const { http } = await import(join(dir, "request.ts"));

const conv = (id: string, updatedAt: string) => ({ id, title: id, messages: [], run: null, permissionMode: "auto", createdAt: updatedAt, updatedAt });

describe("canvas agent conversations on the server", () => {
    it("loads the server copy first and falls back to the local cache when offline", async () => {
        http.get = async () => ({ version: 3, document: { version: 1, activeId: "a", conversations: [conv("a", "2026-10-13T00:00:00Z")] } });
        expect((await mod.loadCloudAgentConversations("cv")).conversations[0].id).toBe("a");
        http.get = async () => {
            throw new Error("offline");
        };
        http.put = async () => {
            throw new Error("offline");
        };
        await mod.saveCloudAgentConversations("cv2", "b", [conv("b", "2026-10-13T00:00:00Z")]);
        expect((await mod.loadCloudAgentConversations("cv2")).conversations[0].id).toBe("b");
    });

    it("merges with the other device's copy on a version conflict", async () => {
        const puts: Array<{ baseVersion: number; document: { conversations: Array<{ id: string }> } }> = [];
        http.get = async () => ({ version: 5, document: { version: 1, activeId: "x", conversations: [conv("x", "2026-10-13T01:00:00Z")] } });
        http.put = async (_url: string, body: (typeof puts)[number]) => {
            puts.push(body);
            if (puts.length === 1) throw Object.assign(new Error("version conflict"), { status: 409 });
            return { version: 6 };
        };
        await mod.saveCloudAgentConversations("cv3", "y", [conv("y", "2026-10-13T02:00:00Z")]);
        expect(puts).toHaveLength(2);
        expect(puts[1].baseVersion).toBe(5);
        expect(puts[1].document.conversations.map((c) => c.id).sort()).toEqual(["x", "y"]);
    });

    it("keeps intelligence-page turns (dir_*) when the canvas saves a stale copy", () => {
        const m = (id: string) => ({ id, role: "user", text: id });
        const server = { version: 1, activeId: "c", conversations: [{ ...conv("c", "2026-10-13T03:00:00Z"), messages: [m("m1"), m("m2"), m("dir_a"), m("dir_b")] }] };
        const local = { version: 1, activeId: "c", conversations: [{ ...conv("c", "2026-10-13T04:00:00Z"), messages: [m("m1"), m("m2"), m("m3")] }] };
        const merged = mod.mergeDocuments(server, local);
        expect(merged.conversations[0].messages.map((x: { id: string }) => x.id)).toEqual(["m1", "m2", "dir_a", "dir_b", "m3"]);
    });

    it("honours message tombstones from either side and edits by newer copy", () => {
        const m = (id: string, text = id) => ({ id, role: "user", text });
        const server = { version: 1, activeId: "c", conversations: [{ ...conv("c", "2026-10-13T05:00:00Z"), messages: [m("m1"), m("m2", "m2 edited")], deletedIds: ["dir_x"] }] };
        const stale = { version: 1, activeId: "c", conversations: [{ ...conv("c", "2026-10-13T04:00:00Z"), messages: [m("m1"), m("m2"), m("dir_x")] }] };
        const merged = mod.mergeDocuments(server, stale);
        expect(merged.conversations[0].messages.map((x: { id: string; text: string }) => x.text)).toEqual(["m1", "m2 edited"]);
        const deleted = mod.deleteConversationMessage(merged.conversations[0], "m1");
        expect(deleted.deletedIds).toEqual(["dir_x", "m1"]);
        const again = mod.mergeDocuments(server, { version: 1, activeId: "c", conversations: [deleted] });
        expect(again.conversations[0].messages.map((x: { id: string }) => x.id)).toEqual(["m2"]);
        expect(mod.editConversationMessage(again.conversations[0], "m2", "v3").messages[0].text).toBe("v3");
    });
});
