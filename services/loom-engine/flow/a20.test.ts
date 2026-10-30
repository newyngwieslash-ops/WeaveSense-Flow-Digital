// A20: local product delivery provenance. Remote publication is separately verified;
// a local HEAD fallback must never be described as proof that origin/main shipped.
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "../../..");

test("A20: DigitalTwin target and fixed Flow history are recorded separately", () => {
  assert.ok(existsSync(join(root, ".git")), "must run inside a checkout");
  const url = execFileSync("git", ["-C", root, "remote", "get-url", "origin"], { encoding: "utf8" }).trim();
  assert.match(url, /^https:\/\/github\.com\/newyngwieslash-ops\/WeaveSense-Flow-Digital(?:\.git)?$/);
  const upstream = execFileSync("git", ["-C", root, "remote", "get-url", "upstream-flow"], { encoding: "utf8" }).trim();
  assert.match(upstream, /^https:\/\/github\.com\/newyngwieslash-ops\/WeaveSense-Flow(?:\.git)?$/);
  const tip = execFileSync("git", ["-C", root, "rev-parse", "HEAD"], { encoding: "utf8" }).trim();
  assert.match(tip, /^[0-9a-f]{40}$/);
  execFileSync("git", ["-C", root, "merge-base", "--is-ancestor", "206c82af7ae007f45da4c582f373fbabf027f1b8", "HEAD"]);
  const lock = JSON.parse(readFileSync(join(root, "third_party/upstreams.lock.json"), "utf8"));
  assert.ok(lock.canvas || lock["open-ai-canvas"] || Object.keys(lock).length > 0, "upstreams.lock.json records upstream pins");
});
