import test from "node:test";
import assert from "node:assert/strict";
import { runInNewContext } from "node:vm";
import { routeClass, safeFailure, memberRoleDiagnostics } from "./organization-e2e.mjs";
import { navigationCommitted } from "./browser-navigation.mjs";

test("diagnostic classes remove workspace and user identifiers", () => {
    assert.equal(routeClass("/api/agriculture/workspaces/private-id/members/private-user"), "agriculture_members");
    assert.equal(routeClass("/api/agriculture/workspaces/private-id/invites/private-invite/revoke"), "agriculture_invites");
    assert.equal(routeClass("/api/agriculture/workspaces/private-id/organization"), "agriculture_organization");
});
test("unknown paths, tokens and query strings are not diagnostic content", () => {
    for (const path of ["/api/auth/reset?token=secret", "/api/agriculture/invites/secret", "/api/agriculture/workspaces?cookie=secret", "/private/secret"]) assert.equal(routeClass(path), null);
});
test("fixed public paths remain attributable", () => {
    for (const path of ["/api/auth/session", "/api/auth/login", "/api/agriculture/invites/accept"]) assert.equal(routeClass(path), path);
});
test("dependency errors cannot disclose email, credentials or cookie diagnostics", () => {
    for (const message of ["fetch failed https://secret.invalid/token", "password=secret", "member@fixture.invalid", "Cookie: abc", "assertion failed: bad credential=secret"]) assert.equal(safeFailure(new Error(message)), "organization browser operation failed; inspect fixed stage and sanitized statuses");
    assert.equal(safeFailure(new Error("assertion failed: owner_protected")), "assertion failed: owner_protected");
});

test("member diagnostics distinguish persisted role from edited draft without secrets", () => {
    const input = { disabled: false, value: "secret input", getAttribute: () => "true" };
    const submit = { disabled: false };
    const doc = { activeElement: input, hasFocus: () => true, querySelector: query => query.includes("role-option-tenant_admin") ? { getClientRects: () => [{}] } : null };
    const control = { dataset: { currentRoles: "tenant_admin,viewer", draftRoles: "viewer", userId: "private actor" },
        ownerDocument: doc, querySelector: () => input, closest: () => ({ querySelector: () => submit }) };
    const result = memberRoleDiagnostics(control);
    assert.equal(result.serverTenantAdmin, true);
    assert.equal(result.draftTenantAdmin, false);
    assert.equal(result.draftViewer, true);
    assert.equal(result.expanded, true);
    assert.equal(result.inputFocused, true);
    assert.equal(result.submitEnabled, true);
    assert.equal(result.targetOptionVisible, true);
    assert.ok(Object.values(result).every(value => typeof value === "boolean"));
    assert.ok(!JSON.stringify(result).includes("secret"));
    assert.ok(!JSON.stringify(result).includes("private actor"));
    // The exact function serialized for CDP has no hidden module dependency.
    const serialized = runInNewContext('(' + memberRoleDiagnostics.toString() + ')(control)', { control });
    assert.equal(JSON.stringify(serialized), JSON.stringify(result));
});

test("missing member control diagnostics remain boolean and cannot inspect invitation input", () => {
    const result = memberRoleDiagnostics(null);
    assert.ok(Object.values(result).every(value => value === false));
});

test("same-URL reload cannot accept the old complete document loader", () => {
    const destination = "http://127.0.0.1/organization?workspace_id=fixture";
    const oldDocument = { readyState: "complete", navigationName: destination };
    assert.equal(oldDocument.readyState === "complete" && oldDocument.navigationName === destination, true,
        "reproduce the old URL-only readiness bug");
    const expected = { frameId: "main-frame", loaderId: "new-loader" };
    assert.equal(navigationCommitted({ frameTree: { frame: { id: "main-frame", loaderId: "old-loader", url: destination } } }, expected), false);
    assert.equal(navigationCommitted({ frameTree: { frame: { id: "main-frame", loaderId: "new-loader", url: destination } } }, expected), true);
});

test("document barrier rejects missing loader, different frame and incomplete response", () => {
    const tree = { frameTree: { frame: { id: "main-frame", loaderId: "new-loader" } } };
    for (const expected of [null, {}, { frameId: "main-frame" }, { frameId: "main-frame", loaderId: "" }, { frameId: "other-frame", loaderId: "new-loader" }]) {
        assert.equal(navigationCommitted(tree, expected), false);
    }
    assert.equal(navigationCommitted({}, { frameId: "main-frame", loaderId: "new-loader" }), false);
});
