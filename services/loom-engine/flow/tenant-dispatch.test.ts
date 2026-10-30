import { test } from "node:test";
import assert from "node:assert/strict";
import { currentTenant, pgDb, withTenant } from "./db.ts";
import { tenantDispatch, type TenantScanner } from "./tenant-dispatch.ts";
import { runtimeRole } from "./runtime-role.ts";

test("scope validation rejects absent or changing tenant and requires explicit database URL", async () => {
    await assert.rejects(pgDb(""), /explicit database URL required/);
    for (const tenant of ["", " ", "a/b", "a\u0000", "a".repeat(129)]) {
      assert.throws(() => withTenant(tenant, async () => 1), /tenant scope required/);
    }
    await withTenant("a", async () => {
      assert.throws(() => withTenant("b", async () => 1), /scope switch denied/);
      assert.equal(await withTenant("a", async () => currentTenant()), "a");
    });
    assert.equal(currentTenant(), "");
});

test("dispatch is bounded, tenant-scoped, resets cursor and preserves other tenants after failure", async () => {
  const afters: string[] = [];
  const scanner: TenantScanner = {
    async scan(after, limit) { afters.push(after); return ["a", "b", "c"].filter(t => t > after).slice(0, limit); },
    async close() {},
  };
  const run = tenantDispatch(scanner, 2);
  const seen: string[] = [];
  await assert.rejects(run(async () => { seen.push(currentTenant()); throw new Error("fixture failure"); }), /fixture failure/);
  await run(async () => { seen.push(currentTenant()); });
  await run(async () => { seen.push(currentTenant()); });
  await run(async () => { seen.push(currentTenant()); });
  assert.deepEqual(afters, ["", "a", "c", ""]);
  assert.deepEqual(seen, ["a", "b", "c", "a", "b"]);
  assert.equal(currentTenant(), "");
});

test("overlapping dispatch and dispatch from a request context are denied", async () => {
  let release!: () => void;
  const waiting = new Promise<void>(resolve => { release = resolve; });
  const run = tenantDispatch({ async scan() { return ["a"]; }, async close() {} });
  const first = run(() => waiting);
  await assert.rejects(run(async () => {}), /context invalid/);
  release(); await first;
  await withTenant("a", async () => {
    await assert.rejects(run(async () => {}), /context invalid/);
  });
});

test("legacy API/all loops remain enabled; startup scanner credential is explicit and migration is separate", () => {
  assert.equal(runtimeRole({ LOOM_SYSTEM_DATABASE_URL: "private" }).role, "api");
  for (const env of [
    { LOOM_ROLE: "invalid" }, { LOOM_ROLE: "worker" },
    {}, { LOOM_AUTO_MIGRATE: "true" },
    { LOOM_MIGRATION_DATABASE_URL: "private" },
  ]) assert.throws(() => runtimeRole(env));
  assert.equal(runtimeRole({ LOOM_ROLE: "worker", LOOM_SYSTEM_DATABASE_URL: "private" }).runEnrich, true);
  assert.equal(runtimeRole({ LOOM_ROLE: "worker", LOOM_SYSTEM_DATABASE_URL: "private", LOOM_SPLIT_ROLES: "true" }).runEnrich, false);
  assert.equal(runtimeRole({ LOOM_ROLE: "scheduler", LOOM_SYSTEM_DATABASE_URL: "private" }).runBriefs, true);
  const all = runtimeRole({ LOOM_ROLE: "all", LOOM_SYSTEM_DATABASE_URL: "private", LOOM_SPLIT_ROLES: "true" });
  assert.equal(all.runEnrich, true);
  assert.equal(all.runBriefs, true);
});
