// Explicit operator entrypoint; runtime never receives this credential.
import { readFileSync } from "node:fs";
import { pgMaintenanceDb, migrate } from "../db.ts";

const db = await pgMaintenanceDb(process.env.LOOM_MIGRATION_DATABASE_URL ?? "");
try {
  const applied = await migrate(db);
  await db.query(readFileSync(new URL("../install-system-role.sql", import.meta.url), "utf8"));
  console.log(JSON.stringify({ migrations: applied, system_dispatch_installed: true }));
} finally { await db.close(); }
