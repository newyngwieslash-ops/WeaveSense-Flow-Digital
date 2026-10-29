import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { resolveStatic, startProxy } from "./session-e2e.mjs";

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
