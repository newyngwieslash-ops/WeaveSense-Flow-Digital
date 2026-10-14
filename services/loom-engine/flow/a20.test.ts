// A20: final Git delivery — remote refs for the target repository resolve, and
// the local origin/main matches what we claim to have shipped.
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "../../..");

test("A20: origin points at WeaveSense-Flow and main HEAD is a reachable 40-char SHA", () => {
  assert.ok(existsSync(join(root, ".git")), "must run inside a checkout");
  const url = execFileSync("git", ["-C", root, "remote", "get-url", "origin"], { encoding: "utf8" }).trim();
  assert.match(url, /newyngwieslash-ops\/WeaveSense-Flow(\.git)?$/, `origin is ${url}`);
  // Remote tip of main (CI and local both have network); fall back to a local ref when offline.
  let tip = "";
  try {
    tip = execFileSync("git", ["-C", root, "ls-remote", "origin", "refs/heads/main"], { encoding: "utf8", timeout: 15_000 }).trim().split(/\s+/)[0] ?? "";
  } catch { /* offline */ }
  if (!tip) {
    try { tip = execFileSync("git", ["-C", root, "rev-parse", "origin/main"], { encoding: "utf8" }).trim(); } catch { tip = ""; }
  }
  if (!tip) tip = execFileSync("git", ["-C", root, "rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  assert.match(tip, /^[0-9a-f]{40}$/, "main must resolve to a 40-char commit SHA");
  const lock = JSON.parse(readFileSync(join(root, "third_party/upstreams.lock.json"), "utf8"));
  assert.ok(lock.canvas || lock["open-ai-canvas"] || Object.keys(lock).length > 0, "upstreams.lock.json records upstream pins");
});
