import test from "node:test";
import assert from "node:assert/strict";
import { runInNewContext } from "node:vm";
import { routeClass, safeFailure, memberRoleDiagnostics, revalidateThroughRealTabSwitch, observeRoutineRetention } from "./organization-e2e.mjs";
import { navigationCommitted } from "./browser-navigation.mjs";

test('routine retention requires a real cadence, fresh completed requests, and an unchanged document', async () => {
    for (const fault of ['none', 'lost-draft', 'no-refresh', 'incomplete', 'reloaded', 'old-inflight-response']) {
        let clock = 0, frames = 0;
        const client = {
            statuses: [],
            requestSequence: () => 2,
            async send(method) {
                assert.equal(method, 'Page.getFrameTree');
                return { frameTree: { frame: { id: 'frame', loaderId: fault === 'reloaded' && frames++ ? 'new' : 'old', url: 'http://127.0.0.1/organization' } } };
            },
            async evaluate() {
                if (clock === 30000 && fault !== 'no-refresh') client.statuses.push(
                    { path: '/api/auth/session', status: 200, completed: fault !== 'incomplete', startedSequence: fault === 'old-inflight-response' ? 1 : 3 },
                    { path: '/api/agriculture/workspaces', status: 200, completed: fault !== 'incomplete', startedSequence: fault === 'old-inflight-response' ? 2 : 4 },
                );
                return !(fault === 'lost-draft' && clock >= 30000);
            },
        };
        const run = () => observeRoutineRetention(client, 'private-fixture-token', 'private-fixture-draft', { now: () => clock, pause: async ms => { clock += ms; } });
        if (fault === 'none') {
            const result = await run();
            assert.equal(result.elapsedMs, 35000);
            assert.ok(result.samples > 300);
            assert.equal(result.sameDocument, true);
            assert.ok(!JSON.stringify(result).includes('private-fixture'));
        } else await assert.rejects(run);
    }
});

test('real tab switch requires observed hidden and visible states without a new loader', async () => {
    for (const fault of ['none', 'never-hidden', 'reloaded']) {
        let active = 'original', frameReads = 0;
        const calls = [];
        const client = {
            async send(method, args) {
                calls.push([method, args]);
                if (method === 'Target.getTargetInfo') return { targetInfo: { targetId: 'original' } };
                if (method === 'Page.getFrameTree') return { frameTree: { frame: { id: 'frame', loaderId: fault === 'reloaded' && frameReads++ ? 'new' : 'old', url: 'http://127.0.0.1/organization' } } };
                if (method === 'Target.createTarget') return { targetId: 'alternate' };
                if (method === 'Target.activateTarget') active = args.targetId;
                return {};
            },
            async poll(expression) {
                if (expression.includes("=== 'hidden'")) assert.ok(active === 'alternate' && fault !== 'never-hidden');
                else assert.equal(active, 'original');
            },
        };
        if (fault === 'none') assert.deepEqual(await revalidateThroughRealTabSwitch(client), { hiddenObserved: true, visibleObserved: true, sameDocument: true });
        else await assert.rejects(revalidateThroughRealTabSwitch(client));
        assert.equal(calls.at(-1)[0], 'Target.closeTarget');
        assert.ok(!calls.some(([method]) => method === 'Page.navigate' || method === 'Page.reload' || method === 'Runtime.evaluate'));
    }
});

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
