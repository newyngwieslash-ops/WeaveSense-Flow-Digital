/** Process role is startup-only, never HTTP input. Scanner is a private typed capability. */
export function runtimeRole(env: Record<string, string | undefined>) {
  const role = env.LOOM_ROLE?.trim() || "api";
  if (!["api", "worker", "scheduler", "enrich", "research", "all"].includes(role)) {
    throw new Error("invalid LOOM_ROLE");
  }
  if (env.LOOM_AUTO_MIGRATE === "true" || env.LOOM_MIGRATION_DATABASE_URL) {
    throw new Error("run explicit migration entrypoint separately from Loom runtime");
  }
  if (!env.LOOM_SYSTEM_DATABASE_URL?.trim()) throw new Error("background role requires system database credentials");
  const split = env.LOOM_SPLIT_ROLES === "true";
  return { role, runEnrich: role === "enrich" || role === "all" || ((role === "api" || role === "worker") && !split),
    runBriefs: role === "research" || role === "all" || (role === "scheduler" && !split) };
}
