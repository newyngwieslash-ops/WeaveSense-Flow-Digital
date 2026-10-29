#!/usr/bin/env node
/** Real Go + Chromium + production React/Vue session integration. No API mocks.
 * Only owned temporary DB/profile directories and spawned processes are removed.
 * Passwords, cookies, browser bodies, screenshots and backend logs are never saved.
 */
import { spawn, spawnSync } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { createReadStream, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { createServer, request } from "node:http";
import { createServer as createPortServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, extname, join, resolve, sep } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const sleep = ms => new Promise(r => setTimeout(r, ms));
const hashFile = file => createHash("sha256").update(readFileSync(file)).digest("hex");
const COOKIE = "open_ai_canvas_session";

export function resolveStatic(pathname, reactRoot, vueRoot) {
    let decoded;
    try { decoded = decodeURIComponent(pathname); } catch { return null; }
    if (decoded.includes("\0") || decoded.includes("\\") || decoded.split("/").includes("..")) return null;
    const vue = decoded === "/agri" || decoded.startsWith("/agri/");
    const root = resolve(vue ? vueRoot : reactRoot);
    const relative = vue ? decoded.slice(5) : decoded;
    const target = resolve(root, `.${relative || "/"}`);
    if (target !== root && !target.startsWith(root + sep)) return null;
    if (existsSync(target) && statSync(target).isFile()) return target;
    return extname(target) ? null : join(root, "index.html");
}

export async function startProxy({ apiPort, reactRoot, vueRoot }) {
    const server = createServer((req, res) => {
        const url = new URL(req.url, "http://127.0.0.1");
        if (url.pathname.startsWith("/api/")) {
            const upstream = request({ hostname: "127.0.0.1", port: apiPort, path: req.url, method: req.method, headers: req.headers }, response => {
                res.writeHead(response.statusCode, { ...response.headers, "cache-control": "no-store" });
                response.pipe(res);
            });
            upstream.on("error", () => { if (!res.headersSent) res.writeHead(502); res.end(); });
            req.pipe(upstream);
            return;
        }
        const file = resolveStatic(url.pathname, reactRoot, vueRoot);
        if (!file || !existsSync(file)) { res.writeHead(404); res.end(); return; }
        const types = { ".html": "text/html; charset=utf-8", ".js": "text/javascript", ".css": "text/css", ".json": "application/json", ".svg": "image/svg+xml", ".woff2": "font/woff2" };
        res.writeHead(200, { "content-type": types[extname(file)] || "application/octet-stream", "cache-control": "no-store" });
        createReadStream(file).pipe(res);
    });
    await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
    return server;
}

async function freePort() {
    const server = createPortServer();
    await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
    const { port } = server.address();
    await new Promise(resolve => server.close(resolve));
    return port;
}

async function ready(url, child, timeout = 120000) {
    const deadline = Date.now() + timeout;
    while (Date.now() < deadline) {
        if (child.exitCode !== null) throw new Error("owned process exited before readiness");
        try { if ((await fetch(url)).ok) return; } catch { /* process startup */ }
        await sleep(200);
    }
    throw new Error("owned process readiness timeout");
}

async function stop(child) {
    if (!child || child.exitCode !== null || child.signalCode !== null) return;
    child.kill("SIGTERM");
    const deadline = Date.now() + 5000;
    while (Date.now() < deadline && child.exitCode === null && child.signalCode === null) await sleep(50);
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
}

async function connect(cdpPort, origin) {
    const targets = await (await fetch(`http://127.0.0.1:${cdpPort}/json/list`)).json();
    const socket = new WebSocket(targets.find(t => t.type === "page").webSocketDebuggerUrl);
    await new Promise((resolve, reject) => { socket.addEventListener("open", resolve, { once: true }); socket.addEventListener("error", reject, { once: true }); });
    let sequence = 0;
    const pending = new Map();
    const exceptions = [];
    let outboundBlocked = 0;
    const send = (method, params = {}) => new Promise((resolve, reject) => {
        const id = ++sequence;
        const timeout = setTimeout(() => { pending.delete(id); reject(new Error(`CDP timeout: ${method}`)); }, 20000);
        pending.set(id, { resolve, reject, timeout });
        socket.send(JSON.stringify({ id, method, params }));
    });
    socket.addEventListener("message", event => {
        const message = JSON.parse(event.data);
        if (pending.has(message.id)) {
            const item = pending.get(message.id); pending.delete(message.id); clearTimeout(item.timeout);
            if (message.error) item.reject(new Error("CDP method failed")); else item.resolve(message.result);
        } else if (message.method === "Runtime.exceptionThrown") {
            // Do not store descriptions: exception text can contain request secrets.
            exceptions.push("browser_runtime_exception");
        } else if (message.method === "Fetch.requestPaused") {
            const { requestId, request: req } = message.params;
            const allowed = req.url.startsWith(origin + "/") || /^(data|blob):/.test(req.url);
            if (!allowed) outboundBlocked++;
            void send(allowed ? "Fetch.continueRequest" : "Fetch.failRequest", allowed ? { requestId } : { requestId, errorReason: "BlockedByClient" }).catch(() => {});
        }
    });
    for (const method of ["Runtime.enable", "Page.enable", "Network.enable"]) await send(method);
    await send("Fetch.enable", { patterns: [{ urlPattern: "*", requestStage: "Request" }] });
    const evaluate = async expression => {
        const result = await send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error("browser evaluation failed (details withheld)");
        return result.result.value;
    };
    const poll = async (expression, label, timeout = 20000) => {
        const deadline = Date.now() + timeout;
        while (Date.now() < deadline) {
            try { if (await evaluate(expression)) return; } catch { /* navigation changes JS context */ }
            await sleep(100);
        }
        throw new Error(`browser assertion timeout: ${label}`);
    };
    const navigate = async path => { await send("Page.navigate", { url: origin + path }); };
    return { send, evaluate, poll, navigate, exceptions, outboundBlocked: () => outboundBlocked, close: () => socket.close() };
}

async function main() {
    const binary = resolve(process.env.AGRI_E2E_BINARY || join(ROOT, ".local/bin/agriculture-platform"));
    const reactRoot = resolve(process.env.AGRI_E2E_REACT_DIST || join(ROOT, "web/dist-agriculture"));
    const vueRoot = resolve(process.env.AGRI_E2E_VUE_DIST || join(ROOT, "services/agri-gis/frontend/dist-agriculture"));
    const chrome = process.env.CHROME_BIN || "/usr/bin/chromium";
    for (const file of [binary, join(reactRoot, "index.html"), join(vueRoot, "index.html"), chrome]) if (!existsSync(file)) throw new Error("required executable or production build absent");
    const temp = mkdtempSync(join(tmpdir(), "digitaltwin-session-e2e-"));
    const results = [];
    let backend, browser, proxy, cdp;
    const check = (ok, name) => { if (!ok) throw new Error(`assertion failed: ${name}`); results.push({ name, result: "pass" }); console.log(`PASS ${name}`); };
    const started = new Date().toISOString();
    const manifestPath = join(ROOT, ".local/evidence/session-ci-provenance.json");
    const manifest = existsSync(manifestPath) ? JSON.parse(readFileSync(manifestPath, "utf8")) : null;
    const composite = manifest?.mode === "read_only_pinned_sources_plus_allowlisted_overlay";
    const source = process.env.AGRI_E2E_SOURCE_COMMIT || spawnSync("git", ["rev-parse", "HEAD"], { cwd: ROOT, encoding: "utf8" }).stdout.trim();
    const report = { schema_version: "1.0.0", scope: "local real Go SQLite + Chromium + production React/Vue; no mocked auth", source_commit: source,
        source_kind: composite ? "scaffold_commit_plus_pinned_upstreams_and_overlay" : "actual_product_checkout",
        source_manifest: manifest,
        working_tree_modified: composite ? null : Boolean(spawnSync("git", ["status", "--porcelain"], { cwd: ROOT, encoding: "utf8" }).stdout.trim()),
        environment: { node: process.version, python: spawnSync("python3", ["--version"], { encoding: "utf8" }).stdout.trim(),
            chrome: spawnSync(chrome, ["--version"], { encoding: "utf8" }).stdout.trim(), platform: process.platform, arch: process.arch },
        backend_sha256: hashFile(binary), react_index_sha256: hashFile(join(reactRoot, "index.html")), vue_index_sha256: hashFile(join(vueRoot, "index.html")),
        started_at: started, command: "node scripts/agriculture/session-e2e.mjs", results,
        limitations: ["SQLite fixture is not PostgreSQL RLS acceptance", "does not validate signed GIS service context", "no real farm or external provider", "no production deployment", "history restoration tested, BFCache eligibility not guaranteed"] };
    try {
        const apiPort = await freePort();
        proxy = await startProxy({ apiPort, reactRoot, vueRoot });
        const origin = `http://127.0.0.1:${proxy.address().port}`;
        backend = spawn(binary, [], { cwd: join(ROOT, "backend"), env: {
            PATH: process.env.PATH, HOME: temp, GIN_MODE: "release", CANVAS_DATABASE_DRIVER: "sqlite", CANVAS_BACKEND_DATA_DIR: temp,
            CANVAS_BACKEND_ADDR: `127.0.0.1:${apiPort}`, CANVAS_AUTO_MIGRATE: "true", CANVAS_CORS_ORIGINS: origin,
        }, stdio: "ignore" });
        await ready(`${origin}/api/auth/settings`, backend);
        const username = `e2e_${randomBytes(6).toString("hex")}`;
        const password = randomBytes(24).toString("base64url");
        const displayName = "Agriculture Session Fixture";
        const registered = await fetch(`${origin}/api/auth/register`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ username, password, displayName, acceptedTerms: true }) });
        const registerBody = await registered.json();
        const actor = registerBody.data?.user?.id;
        check(registered.ok && Boolean(actor), "real_fixture_registration");
        const port = await freePort();
        browser = spawn(chrome, ["--headless=new", "--no-sandbox", "--disable-dev-shm-usage", "--disable-background-networking", "--no-first-run", `--remote-debugging-port=${port}`, `--user-data-dir=${join(temp, "chrome")}`, "about:blank"], { env: { ...process.env, HOME: temp, XDG_CONFIG_HOME: join(temp, "config"), XDG_CACHE_HOME: join(temp, "cache") }, stdio: "ignore" });
        await ready(`http://127.0.0.1:${port}/json/version`, browser, 30000);
        cdp = await connect(port, origin);
        const sessionActor = () => cdp.evaluate(`fetch('/api/auth/session',{cache:'no-store'}).then(r=>r.json()).then(x=>x.data?.user?.id||null)`);
        await cdp.navigate("/");
        await cdp.poll("!!document.querySelector('#login-account')", "anonymous React login");
        check(await sessionActor() === null, "anonymous_react_has_no_actor");
        await cdp.evaluate(`localStorage.setItem('tenant_id','forged-tenant');localStorage.setItem('user_id','forged-user');localStorage.setItem('operator_code','admin');localStorage.setItem('user',JSON.stringify({id:'forged-user',role:'admin'}));document.cookie=${JSON.stringify(COOKIE + "=forged-id.forged-token; path=/")}`);
        await cdp.navigate("/agri/");
        await cdp.poll("!!document.querySelector('main[data-session-status=anonymous]')", "anonymous Vue login");
        check(await sessionActor() === null, "forged_cookie_and_storage_do_not_authenticate_vue");
        await cdp.navigate("/login?next=%2Fagri%2F");
        await cdp.poll("!!document.querySelector('#login-account')", "real login form");
        await cdp.evaluate("document.querySelector('#login-account').focus()");
        await cdp.send("Input.insertText", { text: username });
        await cdp.evaluate("document.querySelector('#login-password').focus()");
        await cdp.send("Input.insertText", { text: password });
        await cdp.evaluate("document.querySelector('form button[type=submit]').click()");
        await cdp.poll("location.pathname === '/agri/' && !!document.querySelector('main[data-session-status=ready] [data-testid=session-user]')", "login transfers to Vue document");
        check(await sessionActor() === actor, "real_form_login_shared_cookie_vue_actor");
        check(await cdp.evaluate(`document.body.innerText.includes(${JSON.stringify(displayName)})`), "vue_displays_authoritative_actor");
        check(await cdp.evaluate(`document.querySelector('[data-testid=session-user]').dataset.userId === ${JSON.stringify(actor)}`), "vue_dom_actor_matches_server_identity");
        await cdp.navigate("/agri/?tenant_id=forged-tenant&user_id=forged-user&operator_code=admin");
        await cdp.poll("!!document.querySelector('main[data-session-status=error]')", "forged authority query rejected");
        check(await cdp.evaluate("!document.querySelector('[data-testid=session-user]')"), "forged_authority_query_cannot_publish_actor");
        check(await sessionActor() === actor, "forged_query_does_not_change_actual_session");
        await cdp.navigate("/agri/");
        await cdp.poll("!!document.querySelector('main[data-session-status=ready] [data-testid=session-user]')", "valid Vue context restored");
        const cookies = (await cdp.send("Network.getCookies", { urls: [origin] })).cookies;
        const cookie = cookies.find(c => c.name === COOKIE);
        check(Boolean(cookie?.httpOnly) && cookie.path === "/" && cookie.sameSite === "Lax", "session_cookie_scope_and_http_only");
        const original = cookie.value;
        const wrong = original.split(".")[0] + ".invalid-token";
        const denied = await fetch(`${origin}/api/auth/session`, { headers: { Cookie: `${COOKIE}=${wrong}` } });
        check((await denied.json()).data?.user === null, "wrong_token_is_anonymous");
        await fetch(`${origin}/api/auth/logout`, { method: "POST", headers: { Cookie: `${COOKIE}=${wrong}` } });
        check(await sessionActor() === actor, "wrong_token_logout_does_not_revoke_original");
        await cdp.navigate("/");
        await cdp.poll(`document.body.innerText.includes(${JSON.stringify(displayName)})`, "React sees same actor");
        check(await sessionActor() === actor, "react_and_vue_share_actual_actor");
        check(await cdp.evaluate(`location.pathname === '/' && document.querySelector('[data-testid=session-user]')?.dataset.userId === ${JSON.stringify(actor)}`), "react_dom_actor_matches_server_identity");
        await cdp.navigate("/agri/");
        await cdp.poll("!!document.querySelector('[data-testid=session-logout]')", "Vue logout control");
        await cdp.evaluate("document.querySelector('[data-testid=session-logout]').click()");
        await cdp.poll("!!document.querySelector('main[data-session-status=anonymous]')", "Vue logout clears actor");
        check(await sessionActor() === null, "vue_logout_revokes_session");
        const history = await cdp.send("Page.getNavigationHistory");
        const previous = history.entries[history.currentIndex - 1];
        check(previous?.url === origin + "/", "history_restoration_target_is_prior_react_document");
        await cdp.send("Page.navigateToHistoryEntry", { entryId: previous.id });
        await cdp.poll("!!document.querySelector('#login-account')", "history-restored React must revalidate revoked session");
        check(await cdp.evaluate("!document.querySelector('[data-testid=session-user]')"), "history_restoration_does_not_retain_previous_actor");
        await cdp.navigate("/");
        await cdp.poll("!!document.querySelector('#login-account')", "React refresh after Vue logout");
        const replay = await fetch(`${origin}/api/auth/session`, { headers: { Cookie: `${COOKIE}=${original}` } });
        check((await replay.json()).data?.user === null, "logout_revoked_cookie_cannot_be_replayed");
        // Create a real session via API, then change ONLY this owned SQLite fixture's expiry.
        const relogin = await fetch(`${origin}/api/auth/login`, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ username, password }) });
        const setCookie = relogin.headers.get("set-cookie");
        const expired = setCookie?.split(";")[0].slice(COOKIE.length + 1);
        check(relogin.ok && Boolean(expired), "real_session_created_for_expiry_negative");
        const expire = spawnSync("python3", ["-c", "import json,sqlite3,sys; x=json.load(sys.stdin); db=sqlite3.connect(x['db']); c=db.execute(\"UPDATE auth_sessions SET expires_at='2000-01-01 00:00:00+00:00' WHERE id=?\",(x['id'],)); assert c.rowcount==1; db.commit(); db.close()"], { input: JSON.stringify({ db: join(temp, "open_ai_canvas.db"), id: decodeURIComponent(expired).split(".")[0] }), encoding: "utf8", stdio: ["pipe", "ignore", "ignore"] });
        check(expire.status === 0, "owned_fixture_expiry_mutation");
        await cdp.send("Network.setCookie", { name: COOKIE, value: decodeURIComponent(expired), url: origin, path: "/", httpOnly: true, sameSite: "Lax" });
        await cdp.navigate("/agri/");
        await cdp.poll("!!document.querySelector('main[data-session-status=anonymous]')", "expired Vue session rejected");
        check(await sessionActor() === null, "expired_session_cannot_rehydrate_vue");
        await cdp.navigate("/");
        await cdp.poll("!!document.querySelector('#login-account')", "expired React session rejected");
        check(await sessionActor() === null, "expired_session_cannot_rehydrate_react");
        check(cdp.exceptions.length === 0, "no_browser_runtime_exceptions");
        report.outbound_requests_blocked = cdp.outboundBlocked();
        check(report.outbound_requests_blocked === 0, "no_outbound_request_attempts");
        report.result = "pass";
    } catch (error) {
        report.result = "fail";
        // Our error messages contain only fixed labels, never runtime values.
        report.failure = String(error.message).replace(/https?:\/\/\S+/g, "[URL]").slice(0, 300);
        process.exitCode = 1;
    } finally {
        cdp?.close();
        await stop(browser); await stop(backend);
        if (proxy) { proxy.closeAllConnections(); await new Promise(resolve => proxy.close(resolve)); }
        rmSync(temp, { recursive: true, force: true });
        report.finished_at = new Date().toISOString();
        const output = resolve(process.env.AGRI_E2E_REPORT || join(ROOT, ".local/evidence/session-browser-e2e.json"));
        mkdirSync(dirname(output), { recursive: true });
        writeFileSync(output, JSON.stringify(report, null, 2) + "\n");
        console.log(JSON.stringify({ result: report.result, checks: results.length, failure: report.failure || null, report: output }));
    }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
    main().catch(() => { console.error("session E2E preflight failed; check required builds and local tools"); process.exitCode = 1; });
}
