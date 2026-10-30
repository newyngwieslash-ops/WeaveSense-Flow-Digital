#!/usr/bin/env node
// Real two-account governance UI. No API mocks or external delivery.
import { spawn, spawnSync } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { csrfFetch, sessionCookieFrom } from "./csrf-fixture.mjs";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer as createPortServer } from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { startProxy, startLocalSMTPSink } from "./session-e2e.mjs";
import { navigateDocument } from "./browser-navigation.mjs";
const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const COOKIE = "open_ai_canvas_session";
const sleep = ms => new Promise(r => setTimeout(r, ms));
const hashFile = file => createHash("sha256").update(readFileSync(file)).digest("hex");

// Only fixed route classes enter reports; never record ids, queries or bodies.
export function routeClass(path) {
    if (["/api/auth/session", "/api/auth/login", "/api/auth/register", "/api/agriculture/workspaces", "/api/agriculture/organizations", "/api/agriculture/invites/accept"].includes(path)) return path;
    if (/^\/api\/agriculture\/workspaces\/[^/]+\/members(?:\/[^/]+)?$/.test(path)) return "agriculture_members";
    if (/^\/api\/agriculture\/workspaces\/[^/]+\/invites(?:\/[^/]+\/revoke)?$/.test(path)) return "agriculture_invites";
    if (/^\/api\/agriculture\/workspaces\/[^/]+\/organization$/.test(path)) return "agriculture_organization";
    return null;
}
export function safeFailure(error) {
    // Errors from dependencies may contain secrets. Emit only our fixed labels.
    return /^assertion failed: [a-z0-9_]+$/.test(error?.message || "") ? error.message : "organization browser operation failed; inspect fixed stage and sanitized statuses";
}

// Diagnostic booleans only: never return actor ids, selectors, input values,
// invitation text or arbitrary DOM content. This does not alter UI state.
export function memberRoleDiagnostics(control) {
    const input = control?.querySelector('input[role="combobox"]');
    const doc = control?.ownerDocument;
    const row = control?.closest('li');
    const submit = row?.querySelector('[data-testid="org-member-roles-submit"]');
    const option = doc?.querySelector('.ant-select-dropdown:not(.ant-select-dropdown-hidden) [data-testid="org-member-role-option-tenant_admin"]');
    const current = (control?.dataset.currentRoles || '').split(',');
    const draft = (control?.dataset.draftRoles || '').split(',');
    return {
        controlPresent: !!control, inputPresent: !!input, inputDisabled: input?.disabled === true,
        documentFocused: doc?.hasFocus() === true, inputFocused: !!input && doc?.activeElement === input,
        expanded: input?.getAttribute('aria-expanded') === 'true',
        serverViewer: current.includes('viewer'), serverTenantAdmin: current.includes('tenant_admin'),
        draftViewer: draft.includes('viewer'), draftTenantAdmin: draft.includes('tenant_admin'),
        submitPresent: !!submit, submitEnabled: !!submit && !submit.disabled,
        targetOptionPresent: !!option, targetOptionVisible: !!option?.getClientRects().length,
        confirmPresent: !!doc?.querySelector('[data-testid="org-confirm"]'),
    };
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
        if (child.exitCode !== null || child.signalCode !== null) throw new Error("owned process exited before readiness");
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
    const statuses = [];
    const statusRequests = new Map();
    let outboundBlocked = 0;
    let committedNavigation = false;
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
        } else if (message.method === "Network.responseReceived") {
            const response = message.params.response;
            const url = new URL(response.url);
            if (url.origin === origin && routeClass(url.pathname)) {
                const status = { path: routeClass(url.pathname), status: response.status, completed: false };
                statuses.push(status);
                statusRequests.set(message.params.requestId, status);
            }
        } else if (message.method === "Network.loadingFinished") {
            const status = statusRequests.get(message.params.requestId);
            if (status) { status.completed = true; statusRequests.delete(message.params.requestId); }
        } else if (message.method === "Fetch.requestPaused") {
            const { requestId, request: req } = message.params;
            const allowed = req.url.startsWith(origin + "/") || /^(data|blob):/.test(req.url);
            if (!allowed) outboundBlocked++;
            void send(allowed ? "Fetch.continueRequest" : "Fetch.failRequest", allowed ? { requestId } : { requestId, errorReason: "BlockedByClient" }).catch(() => {});
        }
    });
    for (const method of ["Runtime.enable", "Page.enable", "Network.enable"]) await send(method);
    await send("Fetch.enable", { patterns: [{ urlPattern: "*", requestStage: "Request" }] });
    const evaluate = async (expression, label = "evaluation") => {
        const result = await send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
        if (result.exceptionDetails) throw new Error(`browser ${label} failed (details withheld)`);
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
    const navigate = async path => {
        await send("Page.bringToFront");
        await poll("document.hasFocus()", "active account browser foreground");
        const destination = origin + path;
        await navigateDocument({ send, evaluate, destination, onCommit: value => { committedNavigation = value; } });
    };
    const fill = async (selector, value, label) => {
        const query = JSON.stringify(selector);
        await poll(`(() => { const el = document.querySelector(${query}); if (!el || el.disabled) return false; el.focus(); return document.activeElement === el; })()`, `${label} focus`);
        await send("Input.insertText", { text: value });
        await poll(`document.querySelector(${query})?.value === ${JSON.stringify(value)}`, `${label} input retained`);
    };
    // A CDP-created headless page can be backgrounded. Activate the real tab before
    // loading application code, rather than letting the first field focus also
    // become a delayed window-focus/session-revalidation event mid-keystroke.
    await send("Page.bringToFront");
    await poll("document.hasFocus()", "browser foreground activation");
    return { send, evaluate, poll, navigate, fill, exceptions, statuses, outboundBlocked: () => outboundBlocked, navigationCommitted: () => committedNavigation, close: () => socket.close() };
}


async function main() {
    const binary = resolve(process.env.AGRI_E2E_BINARY || join(ROOT, ".local/bin/agriculture-platform"));
    const reactRoot = resolve(process.env.AGRI_E2E_REACT_DIST || join(ROOT, "web/dist-agriculture"));
    const vueRoot = resolve(process.env.AGRI_E2E_VUE_DIST || join(ROOT, "services/agri-gis/frontend/dist-agriculture"));
    const chrome = process.env.CHROME_BIN || "/usr/bin/chromium";
    for (const file of [binary, join(reactRoot, "index.html"), join(vueRoot, "index.html"), chrome]) if (!existsSync(file)) throw new Error("missing build");
    const temp = mkdtempSync(join(tmpdir(), "digitaltwin-organization-e2e-"));
    const processes = [], clients = [], results = [];
    let proxy, smtp, memberRoleSelector;
    const check = (ok, name) => { if (!ok) throw new Error("assertion failed: " + name); results.push({ name, result: "pass" }); };
    const manifestFile = join(ROOT, ".local/evidence/session-ci-provenance.json");
    const manifest = existsSync(manifestFile) ? JSON.parse(readFileSync(manifestFile, "utf8")) : null;
    const composite = manifest?.mode === "read_only_pinned_sources_plus_allowlisted_overlay";
    const report = { schema_version: "1.0.0", scope: "real Go SQLite and production React/Vue; two browser profiles; no mocks",
        source_commit: process.env.AGRI_E2E_SOURCE_COMMIT || spawnSync("git", ["rev-parse", "HEAD"], { cwd: ROOT, encoding: "utf8" }).stdout.trim(),
        source_kind: composite ? "scaffold_commit_plus_pinned_upstreams_and_overlay" : "actual_product_checkout", source_manifest: manifest,
        working_tree_modified: composite ? null : Boolean(spawnSync("git", ["status", "--porcelain"], { cwd: ROOT, encoding: "utf8" }).stdout.trim()),
        environment: { node: process.version, chrome: spawnSync(chrome, ["--version"], { encoding: "utf8" }).stdout.trim(), platform: process.platform, arch: process.arch },
        backend_sha256: hashFile(binary), react_index_sha256: hashFile(join(reactRoot, "index.html")), vue_index_sha256: hashFile(join(vueRoot, "index.html")),
        command: "node scripts/agriculture/organization-e2e.mjs", started_at: new Date().toISOString(), results,
        limitations: ["SQLite fixture is not PostgreSQL RLS acceptance", "cross-organization workspace checks do not prove same-tenant multi-workspace isolation", "no GIS resource access, external email, production deployment or real farm acceptance"] };
    try {
        report.stage = "fixture_startup";
        const apiPort = await freePort();
        proxy = await startProxy({ apiPort, reactRoot, vueRoot });
        const origin = "http://127.0.0.1:" + proxy.address().port;
        const csrfKey = join(temp, 'csrf.key');
        writeFileSync(csrfKey, randomBytes(32), { mode: 0o600 });
        const backend = spawn(binary, [], { cwd: join(ROOT, "backend"), env: {
            PATH: process.env.PATH, HOME: temp, GIN_MODE: "release", CANVAS_DATABASE_DRIVER: "sqlite", CANVAS_BACKEND_DATA_DIR: temp,
            CANVAS_BACKEND_ADDR: "127.0.0.1:" + apiPort, CANVAS_AUTO_MIGRATE: "true", CANVAS_CORS_ORIGINS: origin,
            FLOW_AGRICULTURE_ENABLED: "true", FLOW_AGRICULTURE_ALLOW_SQLITE_DEV: "true",
            FLOW_AGRICULTURE_EXTERNAL_ORIGIN: origin, FLOW_AGRICULTURE_ALLOW_HTTP_LOOPBACK_DEV: "true", FLOW_AGRICULTURE_CSRF_KEY_FILE: csrfKey,
        }, stdio: "ignore" }); processes.push(backend);
        await ready(origin + "/api/auth/settings", backend);
        const api = async (path, cookie, method = "GET", body) => {
            const response = await csrfFetch(origin + "/api" + path, { method, headers: { "content-type": "application/json", Origin: origin, ...(cookie ? { Cookie: COOKIE + "=" + cookie } : {}) }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
            let payload; try { payload = await response.json(); } catch { payload = null; }
            return { status: response.status, data: payload?.data, code: payload?.code, cookie: sessionCookieFrom(response) };
        };
        const suffix = randomBytes(6).toString("hex"), password = randomBytes(24).toString("base64url");
        const accountA = "owner_" + suffix, accountB = "member_" + suffix, email = "member_" + suffix + "@fixture.invalid";
        const first = await api("/auth/register", null, "POST", { username: accountA, password, displayName: "Organization Owner Fixture", acceptedTerms: true });
        check(first.status === 200 && first.data?.user?.id && first.cookie, "real_bootstrap_account");
        smtp = await startLocalSMTPSink(email);
        check((await api("/admin/settings/email", first.cookie, "PATCH", { enabled: true, host: "127.0.0.1", port: smtp.port, encryption: "none", username: "", password: "", fromEmail: "noreply@fixture.invalid", fromName: "Local Test Fixture", registrationAllowedDomains: ["fixture.invalid"] })).status === 200, "loopback_only_registration_email_configured");
        check((await api("/admin/settings/registration", first.cookie, "PATCH", { enabled: true })).status === 200, "isolated_registration_enabled");
        check((await api("/auth/email-code", null, "POST", { email })).status === 200 && smtp.messages.length === 1, "real_registration_smtp_message");
        const emailCode = smtp.messages[0].content.match(/验证码：(\d{6})/)?.[1];
        check(Boolean(emailCode), "real_registration_code_in_memory");
        const second = await api("/auth/register", null, "POST", { username: accountB, password, displayName: "Organization Member Fixture", email, emailCode, acceptedTerms: true });
        check(second.status === 200 && second.data?.user?.id, "real_verified_second_account");
        const actorA = first.data.user.id, actorB = second.data.user.id;
        check((await api("/agriculture/workspaces")).status === 401, "anonymous_agriculture_http_401");
        report.stage = "browser_startup";
        const browserFor = async name => {
            const port = await freePort();
            const process = spawn(chrome, ["--headless=new", "--no-sandbox", "--disable-dev-shm-usage", "--disable-background-networking", "--no-first-run", "--remote-debugging-port=" + port, "--user-data-dir=" + join(temp, name), "about:blank"], { env: { ...globalThis.process.env, HOME: temp, XDG_CONFIG_HOME: join(temp, name + "-config"), XDG_CACHE_HOME: join(temp, name + "-cache") }, stdio: "ignore" });
            processes.push(process); await ready("http://127.0.0.1:" + port + "/json/version", process, 30000);
            const client = await connect(port, origin); clients.push(client); return client;
        };
        const a = await browserFor("owner"), b = await browserFor("member");
        const selector = id => '[data-testid="' + id + '"]';
        const click = async (c, query) => {
            await c.poll("(() => { const e=document.querySelector(" + JSON.stringify(query) + "); return !!e && !e.disabled && !!e.getClientRects().length; })()", "action available");
            await c.evaluate("document.querySelector(" + JSON.stringify(query) + ").scrollIntoView({block:'center',behavior:'instant'})");
            await c.evaluate("new Promise(r => requestAnimationFrame(() => requestAnimationFrame(r)))");
            await c.poll("(() => {const e=document.querySelector(" + JSON.stringify(query) + "); if(!e) return false; const r=e.getBoundingClientRect(), hit=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2); return !!hit && (e===hit || e.contains(hit));})()", "real pointer target unobstructed");
            const point = await c.evaluate("(() => {const r=document.querySelector(" + JSON.stringify(query) + ").getBoundingClientRect(); return {x:r.x+r.width/2,y:r.y+r.height/2};})()");
            await c.send("Input.dispatchMouseEvent", { type: "mousePressed", button: "left", clickCount: 1, ...point });
            await c.send("Input.dispatchMouseEvent", { type: "mouseReleased", button: "left", clickCount: 1, ...point });
        };
        const openSelect = async (c, wrapper) => {
            const input = JSON.stringify(wrapper + ' input[role="combobox"]');
            await c.poll("(() => {const e=document.querySelector(" + input + "); if(!e || e.disabled) return false; e.focus(); return document.activeElement===e;})()", "real select keyboard focus");
            await c.send("Input.dispatchKeyEvent", { type: "keyDown", key: "ArrowDown", code: "ArrowDown", windowsVirtualKeyCode: 40 });
            await c.send("Input.dispatchKeyEvent", { type: "keyUp", key: "ArrowDown", code: "ArrowDown", windowsVirtualKeyCode: 40 });
            await c.poll("document.querySelector(" + input + ")?.getAttribute('aria-expanded')==='true'", "real select expanded");
        };
        const login = async (c, username) => {
            await c.navigate("/login?next=%2Forganization");
            await c.fill("#login-account", username, "login account"); await c.fill("#login-password", password, "login password");
            await click(c, "form button[type=submit]");
            await c.poll("location.pathname === '/organization' && !!document.querySelector('[data-testid=organization-management]')", "real organization handoff");
            await c.poll("!document.querySelector('[data-testid=org-create-name]')?.disabled", "organization ready");
            return (await c.send("Network.getCookies", { urls: [origin] })).cookies.find(x => x.name === COOKIE)?.value;
        };
        report.stage = "real_browser_logins";
        const cookieA = await login(a, accountA), cookieB = await login(b, accountB);
        check(Boolean(cookieA && cookieB && cookieA !== cookieB), "two_isolated_real_form_sessions");
        const list = async cookie => { const r = await api("/agriculture/workspaces", cookie); check(r.status === 200 && Array.isArray(r.data), "authoritative_workspace_list"); return r.data; };
        const eventual = async (fn, label) => { const until = Date.now() + 20000; while (Date.now() < until) { if (await fn()) return; await sleep(100); } check(false, label); };
        const openOrg = async (c, id) => { await c.navigate("/organization" + (id ? "?workspace_id=" + encodeURIComponent(id) : "")); await c.poll("!!document.querySelector('[data-testid=organization-management]') && !document.querySelector('[data-testid=org-create-name]')?.disabled", "organization reload"); };
        const createOrg = async (c, cookie, name) => {
            await openOrg(c);
            await c.fill(selector("org-create-name"), name, "organization name"); await click(c, selector("org-create-submit"));
            let workspace; await eventual(async () => { workspace = (await api("/agriculture/workspaces", cookie)).data?.find(x => x.tenantName === name); return Boolean(workspace); }, "organization_created_by_ui");
            await openOrg(c, workspace.id); return workspace;
        };
        report.stage = "create_organizations_ui";
        const orgA = await createOrg(a, cookieA, "Owner Farm " + suffix), orgB = await createOrg(b, cookieB, "Member Farm " + suffix);
        check(orgA.id !== orgB.id && orgA.tenantId !== orgB.tenantId, "two_real_ui_created_organizations");
        const memberPath = "/agriculture/workspaces/" + orgA.id + "/members";
        check((await api("/agriculture/workspaces/" + orgB.id + "/members", cookieA)).status === 404, "platform_admin_has_no_cross_tenant_backdoor");
        check((await api(memberPath, cookieB)).status === 404, "uninvited_workspace_http_404");
        const confirm = async () => { await click(a, selector("org-confirm")); await a.poll("!document.querySelector('[data-testid=org-confirm]')", "confirmation closed"); };
        const ownerMember = async () => (await api(memberPath, cookieA)).data?.members?.find(x => x.userId === actorA);
        report.stage = "owner_explicit_business_role_ui";
        const initialOwner = await ownerMember();
        check(initialOwner?.isOwner && initialOwner.canChangeRoles && initialOwner.roles.length === 0, "owner_has_governance_without_implicit_business_role");
        check(initialOwner.assignableRoles.includes("editor") && !initialOwner.assignableRoles.includes("tenant_admin") && !initialOwner.assignableRoles.includes("billing_admin"), "owner_self_edit_has_server_limited_business_roles");
        const ownerRow = 'li[data-user-id="' + actorA + '"] ';
        for (const granting of [true, false]) {
            report.stage = granting ? "owner_business_role_grant_ui" : "owner_business_role_clear_ui";
            await openOrg(a, orgA.id); await openSelect(a, ownerRow + selector("org-member-roles"));
            await click(a, '.ant-select-dropdown:not(.ant-select-dropdown-hidden) ' + selector("org-member-role-option-editor"));
            await a.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 });
            await a.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 });
            await click(a, ownerRow + selector("org-member-roles-submit")); await confirm();
            await eventual(async () => { const m = await ownerMember(); return m?.isOwner && (granting ? m.roles.length === 1 && m.roles[0] === "editor" : m.roles.length === 0); }, granting ? "owner_explicit_grant_effective" : "owner_explicit_revoke_effective");
            check((await api(memberPath, cookieA)).status === 200, granting ? "owner_ui_business_grant_preserves_governance" : "owner_ui_empty_business_roles_preserves_governance");
        }
        const invite = async () => {
            report.stage = "invite_reload"; await openOrg(a, orgA.id);
            report.stage = "invite_email_input"; await a.fill(selector("org-invite-email"), email, "invite recipient");
            report.stage = "invite_role_expand"; await openSelect(a, selector("org-invite-role"));
            report.stage = "invite_role_choose"; await click(a, '.ant-select-dropdown:not(.ant-select-dropdown-hidden) ' + selector("org-invite-role-option-viewer"));
            report.stage = "invite_submit";
            await click(a, selector("org-invite-create"));
            report.stage = "invite_token_result";
            await a.poll("!!document.querySelector('[data-testid=org-invite-token]')?.value", "one time invitation visible");
            return a.evaluate("document.querySelector('[data-testid=org-invite-token]').value");
        };
        const accept = async token => {
            await openOrg(b, orgB.id); await b.fill(selector("org-accept-token"), token, "manual invitation");
            const before = b.statuses.length; await click(b, selector("org-accept-submit"));
            await eventual(() => b.statuses.slice(before).some(x => x.path === "/api/agriculture/invites/accept" && x.completed), "invite_http_completed");
            return b.statuses.slice(before).find(x => x.path === "/api/agriculture/invites/accept")?.status;
        };
        report.stage = "expired_and_valid_manual_invitation";
        const expired = await invite();
        const expiry = spawnSync("python3", ["-c", "import json,sqlite3,sys,hashlib; x=json.load(sys.stdin); db=sqlite3.connect(x['db']); c=db.execute(\"UPDATE flow_invites SET expires_at='2000-01-01 00:00:00+00:00' WHERE token_hash=?\",(hashlib.sha256(x['token'].encode()).hexdigest(),)); assert c.rowcount==1; db.commit(); db.close()"], { input: JSON.stringify({ db: join(temp, "open_ai_canvas.db"), token: expired }), stdio: ["pipe", "ignore", "ignore"] });
        check(expiry.status === 0, "owned_invite_expiry_fixture_only");
        check(await accept(expired) === 400, "expired_invite_real_ui_http_400");
        const token = await invite();
        check(await accept(token) === 200, "manual_invite_real_ui_acceptance");
        check((await list(cookieB)).some(x => x.id === orgA.id), "accepted_membership_visible_to_original_session");
        check(await accept(token) === 400, "replayed_invite_real_ui_http_400");
        check((await api(memberPath, cookieB)).status === 403, "viewer_governance_http_403");
        check((await api(memberPath + "/" + actorB, cookieB, "PATCH", { roles: ["tenant_admin"] })).status === 403, "viewer_cannot_self_escalate");
        for (const change of [{ status: "suspended" }, { status: "removed" }, { roles: ["tenant_admin"] }, { roles: ["billing_admin"] }]) check((await api(memberPath + "/" + actorA, cookieA, "PATCH", change)).status === 403, "owner_status_or_self_governance_mutation_http_403");
        await b.navigate("/agri/?workspace_id=" + encodeURIComponent(orgA.id));
        await b.poll("!!document.querySelector('main[data-session-status=ready]')", "accepted Vue workspace");
        check(await b.evaluate("!!document.querySelector('[data-testid=session-user]')"), "accepted_workspace_real_vue_entry");
        const row = 'li[data-user-id="' + actorB + '"] ';
        memberRoleSelector = row + selector("org-member-roles");
        report.stage = "role_promotion_reload";
        await openOrg(a, orgA.id);
        report.stage = "role_promotion_expand"; await openSelect(a, memberRoleSelector);
        report.stage = "role_promotion_choose"; await click(a, '.ant-select-dropdown:not(.ant-select-dropdown-hidden) ' + selector("org-member-role-option-tenant_admin"));
        report.stage = "role_promotion_close_dropdown";
        await a.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 }); await a.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 });
        report.stage = "role_promotion_submit"; await click(a, row + selector("org-member-roles-submit"));
        report.stage = "role_promotion_confirm"; await confirm();
        report.stage = "role_promotion_server_effect";
        await eventual(async () => (await api(memberPath, cookieB)).status === 200, "role_promotion_effective");
        check((await api(memberPath, cookieB)).status === 200, "real_ui_promotion_grants_governance");
        await openOrg(b, orgA.id);
        await b.poll("!!document.querySelector('[data-testid=org-invite-create]')", "member has current governance editor");
        await b.fill(selector("org-invite-email"), email, "unsubmitted privileged invitation draft");
        check(await b.evaluate("!!document.querySelector('[data-testid=org-invite-email]')?.value"), "privileged_draft_prepared_before_demotion");
        await click(b, row + selector("org-member-status"));
        await b.poll("!!document.querySelector('[data-testid=org-confirm]')", "pending privileged confirmation");
        check(true, "privileged_confirmation_prepared_without_submission");
        report.stage = "role_demotion_reload";
        await openOrg(a, orgA.id);
        report.stage = "role_demotion_expand"; await openSelect(a, memberRoleSelector);
        report.stage = "role_demotion_choose"; await click(a, '.ant-select-dropdown:not(.ant-select-dropdown-hidden) ' + selector("org-member-role-option-tenant_admin"));
        report.stage = "role_demotion_close_dropdown";
        await a.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 }); await a.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 });
        report.stage = "role_demotion_submit"; await click(a, row + selector("org-member-roles-submit"));
        report.stage = "role_demotion_confirm"; await confirm();
        report.stage = "role_demotion_server_effect";
        await eventual(async () => (await api(memberPath, cookieB)).status === 403, "role_demotion_effective");
        check((await api(memberPath, cookieB)).status === 403, "old_cookie_loses_governance_after_ui_demotion");
        // Submit the captured former privileged operation from the same browser
        // session without reloading its page. Fresh CSRF must not restore roles.
        const staleWrite = await b.evaluate(`(async () => {
            const csrf = await fetch('/api/auth/csrf', { credentials: 'same-origin', cache: 'no-store' });
            const token = (await csrf.json()).data?.token;
            if (!csrf.ok || !/^[a-f0-9]{64}$/.test(token || '')) return { csrf: false };
            const response = await fetch(${JSON.stringify('/api/agriculture/workspaces/' + orgA.id + '/invites')}, {
                method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': token },
                body: JSON.stringify({ email: ${JSON.stringify(email)}, role: 'viewer' }) });
            const body = await response.json(); return { csrf: true, status: response.status, reason: body.reason };
        })()`, "old privileged request after demotion");
        check(staleWrite.csrf === true && staleWrite.status === 403 && staleWrite.reason === 'forbidden', "downgraded_same_session_old_privileged_write_rejected_not_csrf_failure");
        await b.send("Page.bringToFront");
        await b.poll("!!document.querySelector('[data-testid=organization-management]') && !document.querySelector('[data-testid=org-create-name]')?.disabled && !document.querySelector('[data-testid=org-invite-create]')", "same user demotion revalidates and removes old controls");
        check(await b.evaluate("!document.querySelector('[data-testid=org-invite-email]')"), "same_user_demotion_clears_unsubmitted_privileged_draft");
        check(await b.evaluate("!document.querySelector('[data-testid=org-confirm]')"), "session_revalidation_destroys_privileged_confirmation_portal");
        const deniedScope = async label => {
            check((await api(memberPath, cookieB)).status === 404, label + "_old_cookie_http_404");
            check(!(await list(cookieB)).some(x => x.id === orgA.id), label + "_workspace_absent");
            check((await api("/auth/session", cookieB)).data?.user?.id === actorB, label + "_global_session_still_valid");
            check((await api("/agriculture/workspaces/" + orgB.id + "/members", cookieB)).status === 200, label + "_other_organization_unchanged");
            await b.navigate("/agri/?workspace_id=" + encodeURIComponent(orgA.id));
            await b.poll("!!document.querySelector('main[data-session-status=error]')", "revoked Vue workspace rejected");
            check(await b.evaluate("!document.querySelector('[data-testid=session-user]')"), label + "_old_vue_link_rejected");
            await openOrg(b, orgA.id);
            await b.poll("!!document.querySelector('[role=alert]')", "organization old scope rejected");
            check(await b.evaluate("!document.querySelector('[data-testid=org-invite-create]')"), label + "_old_react_link_rejected");
        };
        report.stage = "membership_suspend_restore_ui";
        await openOrg(a, orgA.id); await click(a, row + selector("org-member-status"));
        await click(a, selector("org-cancel"));
        await a.poll("!document.querySelector('[data-testid=org-cancel]')", "cancelled confirmation closed");
        check((await api(memberPath, cookieB)).status === 403 && (await list(cookieB)).some(x => x.id === orgA.id), "cancelled_suspension_does_not_mutate_membership");
        await openOrg(a, orgA.id); await click(a, row + selector("org-member-status")); await confirm();
        await eventual(async () => (await api(memberPath, cookieB)).status === 404, "suspension_effective");
        await deniedScope("suspended");
        await openOrg(a, orgA.id); await click(a, row + selector("org-member-status")); await confirm();
        await eventual(async () => (await api(memberPath, cookieB)).status === 403, "restoration_effective");
        check((await list(cookieB)).some(x => x.id === orgA.id), "real_ui_restore_reinstates_authorized_workspace");
        report.stage = "organization_disable_restore_ui";
        await openOrg(a, orgA.id); await click(a, selector("org-status-toggle")); await confirm();
        await eventual(async () => (await api(memberPath, cookieB)).status === 404, "organization_disable_effective");
        await deniedScope("organization_disabled");
        await openOrg(a, orgA.id); await click(a, selector("org-status-toggle")); await confirm();
        await eventual(async () => (await api(memberPath, cookieB)).status === 403, "organization_restore_effective");
        check((await list(cookieB)).some(x => x.id === orgA.id), "owner_ui_restores_organization");
        report.stage = "remove_member_ui";
        await openOrg(a, orgA.id); await click(a, row + selector("org-member-remove")); await confirm();
        await eventual(async () => (await api(memberPath, cookieB)).status === 404, "removal_effective");
        await deniedScope("removed");
        const reinvite = await invite();
        check(await accept(reinvite) === 400, "ordinary_invite_cannot_restore_removed_member");
        check((await api(memberPath, cookieB)).status === 404, "failed_reinvite_does_not_regrant_scope");
        check(smtp.messages.length === 1, "governance_invites_send_no_external_email");
        check(clients.every(c => c.exceptions.length === 0), "no_browser_runtime_exceptions");
        check(clients.every(c => c.outboundBlocked() === 0), "no_outbound_request_attempts");
        report.result = "pass"; report.stage = "complete";
    } catch (error) {
        report.result = "fail"; report.failure = safeFailure(error);
        report.owned_process_exits = processes.map((p, index) => ({ kind: index === 0 ? "backend" : "browser", exit_code: p.exitCode, signal: p.signalCode }));
        report.browser_statuses = clients.map(c => c.statuses);
        report.browser_runtime_exception_counts = clients.map(c => c.exceptions.length);
        report.outbound_request_counts = clients.map(c => c.outboundBlocked());
        report.expected_loader_committed = clients.map(c => c.navigationCommitted());
        if (clients[0] && memberRoleSelector) {
            try { report.member_role_flags = await clients[0].evaluate('(' + memberRoleDiagnostics.toString() + ')(document.querySelector(' + JSON.stringify(memberRoleSelector) + '))'); }
            catch { report.member_role_flags = { unavailable: true }; }
        }
        report.dom_flags = [];
        for (const c of clients) {
            try { report.dom_flags.push(await c.evaluate("({organization:!!document.querySelector('[data-testid=organization-management]'),invitationEmail:!!document.querySelector('[data-testid=org-invite-email]'),inviteEmailDisabled:document.querySelector('[data-testid=org-invite-email]')?.disabled??null,inviteEmailNonempty:!!document.querySelector('[data-testid=org-invite-email]')?.value,roleControl:!!document.querySelector('[data-testid=org-invite-role]'),roleExpanded:document.querySelector('[data-testid=org-invite-role] input')?.getAttribute('aria-expanded')==='true',viewerOption:!!document.querySelector('.ant-select-dropdown:not(.ant-select-dropdown-hidden) [data-testid=org-invite-role-option-viewer]'),roleSelected:document.querySelector('[data-testid=org-invite-role]')?.dataset.currentRoles==='viewer',inviteSubmitEnabled:document.querySelector('[data-testid=org-invite-create]')?.disabled===false,alert:!!document.querySelector('[role=alert]')})")); }
            catch { report.dom_flags.push({ unavailable: true }); }
        }
        process.exitCode = 1;
    } finally {
        for (const c of clients) c.close();
        for (const p of processes.reverse()) await stop(p);
        await smtp?.close();
        if (proxy) { proxy.closeAllConnections(); await new Promise(r => proxy.close(r)); }
        rmSync(temp, { recursive: true, force: true });
        report.finished_at = new Date().toISOString();
        const output = resolve(process.env.AGRI_E2E_REPORT || join(ROOT, ".local/evidence/organization-browser-e2e.json"));
        mkdirSync(dirname(output), { recursive: true }); writeFileSync(output, JSON.stringify(report, null, 2) + "\n");
        console.log(JSON.stringify({ result: report.result, checks: results.length, failure: report.failure || null }));
    }
}
if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) main().catch(() => { console.error("organization E2E preflight failed; required builds or tools unavailable"); process.exitCode = 1; });

