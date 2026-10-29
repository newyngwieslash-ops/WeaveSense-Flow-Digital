import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { assertAgricultureModule } from "../scripts/agriculture-build-policy";
import { applyUserSession, beginSessionRevalidation } from "../src/features/agriculture/session";
import { appQueryClient } from "../src/lib/query-client";
import { useUserStore } from "../src/stores/use-user-store";

describe("agriculture production profile", () => {
    test("anonymous focus revalidation preserves hydration but hides authenticated state", async () => {
        await applyUserSession({ user: null });
        const hydration: boolean[] = [];
        const unsubscribe = useUserStore.subscribe((state) => { hydration.push(state.hydrated); });
        try {
            beginSessionRevalidation();
            await applyUserSession({ user: null });
            expect(hydration.every(Boolean)).toBe(true);
            expect(useUserStore.getState().hydrated).toBe(true);
            useUserStore.getState().setHydrated(false);
            beginSessionRevalidation();
            expect(useUserStore.getState().hydrated).toBe(false);
            await applyUserSession({ user: { id: "focus-test", username: "a", displayName: "A", role: "user", status: "active", createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" } });
            beginSessionRevalidation();
            expect(useUserStore.getState().hydrated).toBe(false);
        } finally {
            unsubscribe();
            await applyUserSession({ user: null });
        }
    });
    test("rejects licensed drawing implementations rather than concealing menus", () => {
        for (const id of ["tldraw", "@tldraw/editor", "/app/node_modules/tldraw/dist/index.js", "C:\\app\\node_modules\\@tldraw\\editor\\dist.js", "/app/src/pages/canvas/index.tsx", "/app/src/components/canvas/drawing/tldraw-editor.tsx"]) {
            expect(() => assertAgricultureModule(id)).toThrow();
        }
        for (const id of ["/app/node_modules/react/index.js", "/app/src/services/api/auth.ts", "/app/src/features/agriculture/main.tsx"]) expect(() => assertAgricultureModule(id)).not.toThrow();
    });
    test("uses shared auth pages and API, excludes legacy routing/hydration", () => {
        const entry = readFileSync(new URL("../src/features/agriculture/main.tsx", import.meta.url), "utf8");
        expect(entry).toContain('@/pages/auth/login');
        expect(entry).toContain('@/services/api/auth');
        expect(entry).not.toContain('@/router');
        expect(entry).not.toContain('ClientRootInit');
        const config = readFileSync(new URL("../vite.agriculture.config.ts", import.meta.url), "utf8");
        expect(config).toContain('publicDir: false');
        expect(config).toContain('src/features/agriculture/session.ts');
    });
    test("user switch and logout clear query data; API is authoritative", async () => {
        const user = { id: "profile-test-a", username: "a", displayName: "A", role: "user" as const, status: "active" as const, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" };
        await applyUserSession({ user });
        appQueryClient.setQueryData(["agriculture", "workspaces", user.id], [{ id: "revoked-workspace" }]);
        await applyUserSession({ user });
        expect(appQueryClient.getQueryData(["agriculture", "workspaces", user.id])).toBeUndefined();
        appQueryClient.setQueryData(["private-farm"], { secret: "tenant-a-fixture" });
        await applyUserSession({ user: { ...user, id: "profile-test-b" } });
        expect(appQueryClient.getQueryData(["private-farm"])).toBeUndefined();
        expect(useUserStore.getState().user?.id).toBe("profile-test-b");
        appQueryClient.setQueryData(["private-farm"], { secret: "tenant-b-fixture" });
        await applyUserSession({ user: null });
        expect(appQueryClient.getQueryData(["private-farm"])).toBeUndefined();
        expect(useUserStore.getState().user).toBeNull();
        expect(useUserStore.getState().hydrated).toBe(true);
    });
});
