import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createConnection } from "node:net";
import test from "node:test";
import { resolveStatic, startProxy, startLocalSMTPSink } from "./session-e2e.mjs";
import { navigateDocument } from "./browser-navigation.mjs";

test("shared document barrier never evaluates old same-URL DOM and waits for new readiness", async () => {
    let frameReads = 0, evaluates = 0, navigations = 0;
    const commits = [];
    await navigateDocument({ destination: "http://127.0.0.1/organization?workspace_id=fixture",
        send: async method => {
            if (method === "Page.navigate") { navigations++; return { frameId: "main", loaderId: "new" }; }
            assert.equal(method, "Page.getFrameTree");
            frameReads++;
            return { frameTree: { frame: { id: "main", loaderId: frameReads === 1 ? "old" : "new" } } };
        },
        evaluate: async () => { assert.ok(frameReads > 1, "old document must not satisfy readiness"); return ++evaluates === 2; },
        onCommit: value => commits.push(value),
    });
    assert.equal(navigations, 1);
    assert.equal(frameReads, 3);
    assert.equal(evaluates, 2);
    assert.deepEqual(commits, [false, false, true, true]);
});

test("shared document barrier fails closed without a new loader or on navigation failure", async () => {
    for (const response of [{ frameId: "main" }, { frameId: "main", loaderId: "new", errorText: "private URL" }]) {
        let evaluations = 0, calls = 0;
        await assert.rejects(navigateDocument({ destination: "http://127.0.0.1/",
            send: async method => { calls++; assert.equal(method, "Page.navigate"); return response; },
            evaluate: async () => { evaluations++; return true; },
        }), /^Error: browser document navigation did not return an expected loader$/);
        assert.equal(evaluations, 0);
        assert.equal(calls, 1);
    }
});

function smtpExchange(port, commands) {
    return new Promise((resolve, reject) => {
        const socket = createConnection({ host: "127.0.0.1", port });
        let received = "";
        socket.setEncoding("utf8");
        socket.setTimeout(3000, () => { socket.destroy(); reject(new Error("local SMTP test timeout")); });
        socket.on("connect", () => socket.write(commands));
        socket.on("data", chunk => { received += chunk; });
        socket.on("end", () => resolve(received));
        socket.on("error", reject);
    });
}

test("SMTP sink receives real protocol bytes only on loopback and clears memory", async () => {
    const sink = await startLocalSMTPSink("reset_fixture@fixture.invalid");
    try {
        assert.equal(sink.host, "127.0.0.1");
        const replies = await smtpExchange(sink.port, "EHLO fixture\r\nMAIL FROM:<noreply@fixture.invalid> BODY=8BITMIME\r\nRCPT TO:<reset_fixture@fixture.invalid>\r\nDATA\r\nSubject: fixture\r\n\r\n验证码：123456\r\n..dot\r\n.\r\nQUIT\r\n");
        assert.match(replies, /250 accepted into local memory only/);
        assert.equal(sink.messages.length, 1);
        assert.equal(sink.messages[0].recipient, "reset_fixture@fixture.invalid");
        assert.match(sink.messages[0].content, /验证码：123456\n\.dot/);
    } finally { await sink.close(); }
    assert.equal(sink.messages.length, 0);
});

test("SMTP sink refuses non-fixture relay recipients and DATA", async () => {
    const sink = await startLocalSMTPSink("reset_fixture@fixture.invalid");
    try {
        const replies = await smtpExchange(sink.port, "EHLO fixture\r\nMAIL FROM:<noreply@fixture.invalid>\r\nRCPT TO:<outsider@example.com>\r\nDATA\r\nQUIT\r\n");
        assert.match(replies, /550 no relay/);
        assert.match(replies, /503 fixture recipient required/);
        assert.equal(sink.messages.length, 0);
    } finally { await sink.close(); }
});

test("SMTP sink rejects oversized messages without retaining content", async () => {
    const sink = await startLocalSMTPSink("reset_fixture@fixture.invalid");
    try {
        const replies = await smtpExchange(sink.port, "EHLO fixture\r\nMAIL FROM:<noreply@fixture.invalid>\r\nRCPT TO:<reset_fixture@fixture.invalid>\r\nDATA\r\n" + "x".repeat(140 * 1024));
        assert.match(replies, /552 fixture message size limit/);
        assert.equal(sink.messages.length, 0);
    } finally { await sink.close(); }
});

test("SMTP fixture cannot be configured with an external recipient", async () => {
    await assert.rejects(startLocalSMTPSink("someone@example.com"), /reserved fixture recipient/);
});

test("static harness separates React and Vue SPA roots and refuses traversal", () => {
    const root = mkdtempSync(join(tmpdir(), "agri-static-test-"));
    try {
        const react = join(root, "react"), vue = join(root, "vue");
        mkdirSync(react); mkdirSync(vue);
        writeFileSync(join(react, "index.html"), "react-fixture");
        writeFileSync(join(vue, "index.html"), "vue-fixture");
        assert.equal(resolveStatic("/login", react, vue), join(react, "index.html"));
        assert.equal(resolveStatic("/agri/", react, vue), join(vue, "index.html"));
        assert.equal(resolveStatic("/agri/workspace", react, vue), join(vue, "index.html"));
        for (const path of ["/%2e%2e/private", "/agri/../private", "/%5cprivate", "/%00", "/%ZZ", "/assets/missing.js"]) {
            assert.equal(resolveStatic(path, react, vue), null);
        }
    } finally { rmSync(root, { recursive: true, force: true }); }
});

test("same-origin server serves distinct production roots without asset fallback", async () => {
    const root = mkdtempSync(join(tmpdir(), "agri-static-http-test-"));
    let server;
    try {
        const react = join(root, "react"), vue = join(root, "vue");
        mkdirSync(react); mkdirSync(vue);
        writeFileSync(join(react, "index.html"), "react-fixture");
        writeFileSync(join(vue, "index.html"), "vue-fixture");
        server = await startProxy({ apiPort: 1, reactRoot: react, vueRoot: vue });
        const base = `http://127.0.0.1:${server.address().port}`;
        assert.equal(await (await fetch(base + "/login")).text(), "react-fixture");
        assert.equal(await (await fetch(base + "/agri/")).text(), "vue-fixture");
        assert.equal((await fetch(base + "/agri/assets/missing.js")).status, 404);
    } finally {
        if (server) { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }
        rmSync(root, { recursive: true, force: true });
    }
});
