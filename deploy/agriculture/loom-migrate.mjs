// Separate migration process; the long-lived Loom process has no DDL role.
import { pgDb, migrate } from '/app/flow/db.ts';
const db = await pgDb(process.env.DATABASE_URL);
try { console.log(JSON.stringify({ migrations: await migrate(db) })); }
finally { await db.close(); }
