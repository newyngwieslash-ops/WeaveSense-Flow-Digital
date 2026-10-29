import { describe, expect, test } from "bun:test";
import { OrganizationController } from "../src/features/agriculture/organization-controller";
import { agricultureAPI, assignableAgricultureRoles, type AgricultureWorkspace, type AgricultureMembers } from "../src/services/api/agriculture";

const workspace = (id: string): AgricultureWorkspace => ({ id, tenantId: `tenant-${id}`, name: id, slug: "default", tenantName: id, tenantStatus: "active", roles: ["tenant_admin"], canManageMembers: true, canManageOrganization: false });
const members = (id: string): AgricultureMembers => ({ members: [{ userId: id, name: id, roles: ["viewer"], status: "active", isOwner: false }], assignableRoles: ["viewer", "analyst"] });
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (reason: unknown) => void; const promise = new Promise<T>((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; }
function api(overrides: Partial<typeof agricultureAPI> = {}): typeof agricultureAPI {
    return { ...agricultureAPI, workspaces: async () => [workspace("a"), workspace("b")], members: async id => members(id), ...overrides };
}
describe("agriculture organization controller", () => {
    test("unknown and legacy roles are never promoted to editor", () => {
        expect(assignableAgricultureRoles(["owner", "root", "member", "admin", "field_worker", "billing_admin", "viewer"])).toEqual(["viewer", "billing_admin"]);
    });
    test("URL workspace hint cannot select absent authorization or call its members API", async () => {
        let calls = 0;
        const c = new OrganizationController(api({ members: async () => { calls++; return members("x"); } }));
        await c.refresh("other-tenant");
        expect(c.state.selectedId).toBe(""); expect(c.state.members).toBeNull(); expect(calls).toBe(0); expect(c.state.error).not.toBe("");
    });
    test("new selection immediately removes old projection and ignores late members", async () => {
        const old = deferred<AgricultureMembers>();
        const c = new OrganizationController(api({ members: id => id === "a" ? old.promise : Promise.resolve(members(id)) }));
        const a = c.refresh("a"); await Promise.resolve();
        const b = c.refresh("b"); expect(c.state.members).toBeNull();
        await b; old.resolve(members("a")); await a;
        expect(c.state.selectedId).toBe("b"); expect(c.state.members?.members[0].userId).toBe("b");
    });
    test("disabled/nonmanager workspace does not fetch privileged membership", async () => {
        let calls = 0;
        const c = new OrganizationController(api({ workspaces: async () => [{ ...workspace("a"), tenantStatus: "disabled", canManageOrganization: true }], members: async () => { calls++; return members("a"); } }));
        await c.refresh(); expect(calls).toBe(0); expect(c.state.members).toBeNull(); expect(c.state.workspaces[0].canManageOrganization).toBe(true);
    });
    test("create retry retains idempotency key; changed logical input gets a new key", async () => {
        const calls: string[] = []; let n = 0;
        const c = new OrganizationController(api({ create: async (name, key) => { calls.push(`${name}:${key}`); throw new Error("offline"); } }), () => `key${++n}`);
        await c.refresh(); await c.create("Org"); await c.create(" Org "); await c.create("Different");
        expect(calls).toEqual(["Org:key1", "Org:key1", "Different:key2"]); expect(c.state.notice).toBe(""); expect(c.state.error).toBe("offline");
    });
    test("write failure propagates as error and clears privilege projection, not success", async () => {
        const c = new OrganizationController(api()); await c.refresh();
        expect(await c.mutate(async () => { throw new Error("forbidden"); }, "should not appear")).toBe(false);
        expect(c.state.notice).toBe(""); expect(c.state.members).toBeNull(); expect(c.state.workspaces).toEqual([]); expect(c.state.error).toBe("forbidden");
    });
    test("successful create followed by failed refresh still retains the same logical request key", async () => {
        const keys: string[] = []; let n = 0;
        const c = new OrganizationController(api({ workspaces: async () => { throw new Error("refresh unavailable"); }, create: async (name, key) => { keys.push(key); return { tenant: { id: "t", name, status: "active" }, workspace: workspace("a") }; } }), () => `key${++n}`);
        await c.create("Org"); await c.create("Org"); expect(keys).toEqual(["key1", "key1"]); expect(c.state.notice).toBe("");
    });
    test("one-time invitation is memory-only and discarded on revalidation/disposal", async () => {
        const c = new OrganizationController(api({ invite: async () => ({ invite: { id: "i", email: "fixture@example.invalid", role: "viewer", expiresAt: "future" }, token: "fixture-only-token" }) }));
        await c.refresh(); await c.invite("fixture@example.invalid", "viewer"); expect(c.state.invitation?.token).toBe("fixture-only-token");
        const refreshing = c.refresh(); expect(c.state.invitation).toBeNull(); await refreshing;
        await c.invite("fixture@example.invalid", "viewer"); c.dispose(); expect(c.state.invitation).toBeNull(); expect(c.state.workspaces).toEqual([]);
    });
    test("disposed or superseded invitation response cannot restore a token", async () => {
        const pending = deferred<Awaited<ReturnType<typeof agricultureAPI.invite>>>();
        const c = new OrganizationController(api({ invite: () => pending.promise })); await c.refresh();
        const invite = c.invite("fixture@example.invalid", "viewer"); await c.refresh("b");
        pending.resolve({ invite: { id: "i", email: "fixture@example.invalid", role: "viewer", expiresAt: "future" }, token: "fixture-token" }); await invite;
        expect(c.state.invitation).toBeNull(); expect(c.state.selectedId).toBe("b");
    });
    test("newer selection cannot be overwritten by a late successful create", async () => {
        const pending = deferred<Awaited<ReturnType<typeof agricultureAPI.create>>>();
        const c = new OrganizationController(api({ create: () => pending.promise })); await c.refresh("a");
        const create = c.create("new"); await c.refresh("b"); pending.resolve({ tenant: { id: "new", name: "new", status: "active" }, workspace: workspace("new") });
        expect(await create).toBe(false); expect(c.state.selectedId).toBe("b"); expect(c.state.notice).toBe("");
    });
});
