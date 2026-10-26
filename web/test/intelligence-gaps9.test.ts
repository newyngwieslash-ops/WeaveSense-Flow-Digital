import { describe, expect, test } from "bun:test";

import { expansionFromError } from "../src/pages/intelligence/monitor-editor";
import { credits } from "../src/pages/intelligence/plan-card";
import { cachedConversations, conversationCacheKey, writeCachedConversations, type FlowConversation } from "../src/services/api/intelligence";
import { useUserStore } from "../src/stores/use-user-store";

describe("intelligence audit-8 UI helpers", () => {
    test("re-approval expansion is parsed from the server 409 message", () => {
        expect(expansionFromError(new Error("reapproval_required: sources,budget"))).toEqual(["sources", "budget"]);
        expect(expansionFromError({ message: "monitor version conflict" })).toBeUndefined();
    });
    test("micro-credits render as credits", () => {
        expect(credits(1_500_000)).toContain("1.5");
        expect(credits(undefined)).toBe("—");
    });
    test("conversation warm-start cache is isolated per logged-in user and never shared when logged out", () => {
        const store = new Map<string, string>();
        const prevStorage = globalThis.localStorage;
        Object.defineProperty(globalThis, "localStorage", {
            configurable: true,
            value: {
                getItem: (k: string) => store.get(k) ?? null,
                setItem: (k: string, v: string) => {
                    store.set(k, v);
                },
                removeItem: (k: string) => {
                    store.delete(k);
                },
                clear: () => store.clear(),
            },
        });
        try {
            useUserStore.getState().clearSession();
            expect(conversationCacheKey()).toBeNull();
            const aliceConv: FlowConversation = {
                id: "c_alice",
                title: "Alice secret topic",
                subjectKind: "intelligence_workspace",
                subjectId: "ws_1",
                workspaceId: "ws_1",
                updatedAt: "2026-10-19T00:00:00Z",
                lastSeq: 1,
            };
            writeCachedConversations([aliceConv]);
            expect(store.size).toBe(0);
            expect(cachedConversations()).toEqual([]);

            useUserStore.getState().setUser({
                id: "u_alice",
                username: "alice",
                displayName: "Alice",
                role: "user",
                status: "active",
            });
            writeCachedConversations([aliceConv]);
            expect(cachedConversations().map((c) => c.id)).toEqual(["c_alice"]);

            useUserStore.getState().setUser({
                id: "u_bob",
                username: "bob",
                displayName: "Bob",
                role: "user",
                status: "active",
            });
            expect(cachedConversations()).toEqual([]);

            useUserStore.getState().clearSession();
            expect(cachedConversations()).toEqual([]);
        } finally {
            useUserStore.getState().clearSession();
            Object.defineProperty(globalThis, "localStorage", {
                configurable: true,
                value: prevStorage,
            });
        }
    });
    test("intelligence pages use UI-Kit components (AppDrawer/AppModal/Callout/StatusBadge) and accurate Chinese copy", async () => {
        const dir = `${import.meta.dir}/../src/pages/intelligence`;
        const files = ["index.tsx", "views.tsx", "sources-panel.tsx", "reports-panel.tsx", "topics-panel.tsx", "monitor-editor.tsx", "workspace-admin-panel.tsx", "mcp.tsx", "plan-card.tsx"];
        for (const f of files) {
            const text = await Bun.file(`${dir}/${f}`).text();
            const antdImport = /import\s*\{([^}]+)\}\s*from\s*["']antd["']/.exec(text)?.[1] ?? "";
            const specs = antdImport.split(",").map((s) => s.trim());
            expect(specs).not.toContain("Alert");
            expect(specs).not.toContain("Badge");
            expect(specs).not.toContain("Modal");
            expect(specs).not.toContain("Drawer");
        }
        const indexSrc = await Bun.file(`${dir}/index.tsx`).text();
        expect(indexSrc).toContain("AppDrawer");
        expect(indexSrc).toContain("情报助手");
        expect(indexSrc).toContain("onOpenAgent={() => setAgentDrawerOpen(true)}");
        expect(indexSrc).toContain("onEditMonitor={setEditingMonitor}");
        const viewsSrc = await Bun.file(`${dir}/views.tsx`).text();
        expect(viewsSrc).toContain("JOB_LABEL");
        expect(viewsSrc).toContain("credits(s.budget_micro_per_window)");
        expect(viewsSrc).toContain("唤起情报助手");
        const sourcesSrc = await Bun.file(`${dir}/sources-panel.tsx`).text();
        expect(sourcesSrc).not.toContain("Text only: media attachments");
        expect(sourcesSrc).toContain("支持公开频道消息与受管大小内的媒体附件采集");
        expect(sourcesSrc).toContain("SOURCE_STATUS_LABEL");
        const adminSrc = await Bun.file(`${dir}/workspace-admin-panel.tsx`).text();
        expect(adminSrc).toContain("StatusBadge");
        expect(adminSrc).toContain('value: "editor"');
        expect(adminSrc).toContain("RESERVATION_STATUS_LABEL");
        expect(adminSrc).toContain("ROLE_LABEL");
        const reportsSrc = await Bun.file(`${dir}/reports-panel.tsx`).text();
        expect(reportsSrc).toContain('"pdf"');
        expect(reportsSrc).toContain('"docx"');
    });
});
