import test from "node:test";
import assert from "node:assert/strict";
import { routeClass, safeFailure } from "./organization-e2e.mjs";

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
