import { expect, spyOn, test } from "bun:test";
import { agricultureAPI, validateAgricultureWorkspaces } from "../src/services/api/agriculture";
import { http } from "../src/services/api/request";

test("agriculture workspace contract rejects legacy projections and unknown role authority", () => {
    const row = { id: "w", tenantId: "t", name: "Farm", slug: "default", tenantName: "Org", tenantStatus: "active", roles: ["billing_admin"], canManageMembers: false, canManageOrganization: false, authorityFingerprint: 'a'.repeat(64) };
    expect(validateAgricultureWorkspaces([row])).toEqual([row]);
    expect(validateAgricultureWorkspaces([{ ...row, tenantStatus: "disabled", roles: [], canManageOrganization: true }])[0].tenantStatus).toBe("disabled");
    for (const bad of [null, [{ id: "old", name: "Film", slug: "default" }], [{ ...row, roles: ["owner"] }], [{ ...row, roles: ["viewer", "viewer"] }], [{ ...row, canManageMembers: "true" }], [row, row]]) expect(() => validateAgricultureWorkspaces(bad)).toThrow();
});

test("agriculture client uses shared transport, encoded resource IDs and no caller authority fields", async () => {
    const get = spyOn(http, "get").mockResolvedValue([] as never);
    const post = spyOn(http, "post").mockResolvedValue({ ok: true } as never);
    const patch = spyOn(http, "patch").mockResolvedValue({ ok: true } as never);
    try {
        const controller = new AbortController();
        await agricultureAPI.workspaces(controller.signal);
        await agricultureAPI.members("ws/other", controller.signal);
        expect(get.mock.calls).toEqual([["/agriculture/workspaces", { signal: controller.signal }], ["/agriculture/workspaces/ws%2Fother/members", { signal: controller.signal }]]);
        await agricultureAPI.create("Org", "fixture-key");
        await agricultureAPI.invite("ws", "test@example.invalid", "viewer");
        await agricultureAPI.accept("fixture-token");
        await agricultureAPI.revoke("ws", "inv/id");
        expect(post.mock.calls).toEqual([
            ["/agriculture/organizations", { name: "Org" }, { headers: { "X-Idempotency-Key": "fixture-key" } }],
            ["/agriculture/workspaces/ws/invites", { email: "test@example.invalid", role: "viewer" }],
            ["/agriculture/invites/accept", { token: "fixture-token" }],
            ["/agriculture/workspaces/ws/invites/inv%2Fid/revoke", {}],
        ]);
        await agricultureAPI.member("ws", "user/id", { roles: ["billing_admin"] });
        await agricultureAPI.member("ws", "user/id", { status: "suspended" });
        await agricultureAPI.organization("ws", "disabled");
        expect(patch.mock.calls).toEqual([
            ["/agriculture/workspaces/ws/members/user%2Fid", { roles: ["billing_admin"] }],
            ["/agriculture/workspaces/ws/members/user%2Fid", { status: "suspended" }],
            ["/agriculture/workspaces/ws/organization", { status: "disabled" }],
        ]);
    } finally { get.mockRestore(); post.mockRestore(); patch.mockRestore(); }
});

test("agriculture client does not turn write rejection into success", async () => {
    const error = new Error("authoritative rejection");
    const post = spyOn(http, "post").mockRejectedValue(error);
    try { await expect(agricultureAPI.accept("fixture-token")).rejects.toBe(error); }
    finally { post.mockRestore(); }
});
