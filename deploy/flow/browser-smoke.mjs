// Browser smoke against a running stack: registers a throwaway user, opens
// /intelligence and /intelligence/mcp, asserts key panels render, no page errors.
// Usage: CHROME=/usr/bin/google-chrome node deploy/flow/browser-smoke.mjs [base]
// (needs `playwright-core`, e.g. `npm i --no-save playwright-core`)
import { chromium } from "playwright-core";
const base = process.argv[2] || "http://localhost:8088";
const browser = await chromium.launch({ executablePath: process.env.CHROME || "/usr/bin/google-chrome", args: ["--no-sandbox"] });
const page = await browser.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(String(e)));
await page.goto(base + "/");
// SMOKE_USER/SMOKE_PASSWORD: existing account (log in); otherwise register a throwaway user.
const reg = await page.evaluate(async ([u, p]) => {
  const post = (path, body) => fetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, credentials: "include", body: JSON.stringify(body) });
  if (u && p && (await post("/api/auth/login", { username: u, password: p })).status === 200) return 200;
  return (await post("/api/auth/register", { username: u || `bsmoke_${Date.now().toString(36)}`, password: p || "Bs-" + crypto.randomUUID(), displayName: "Browser smoke", acceptedTerms: true })).status;
}, [process.env.SMOKE_USER || "", process.env.SMOKE_PASSWORD || ""]);
if (reg !== 200) throw new Error("login/register " + reg);
const checks = [];
const expectText = async (path, texts) => {
  await page.goto(base + path, { waitUntil: "networkidle" });
  for (const t of texts) {
    await page.getByText(t, { exact: false }).first().waitFor({ timeout: 20000 });
    checks.push(`${path}: "${t}"`);
  }
};
await expectText("/intelligence", ["Conversations", "Intelligence chat", "Monitors", "Members (", "Billing"]);
await page.screenshot({ path: process.env.SHOT || "/tmp/flow-intelligence.png", fullPage: true });
await expectText("/intelligence/mcp", ["Unknown-outcome calls"]);
await browser.close();
if (errors.length) throw new Error("page errors: " + errors.join("; "));
console.log(checks.map((c) => "PASS " + c).join("\n") + `\nbrowser smoke: ${checks.length} passed`);
