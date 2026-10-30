// Separate migration process; the long-lived Loom process has no DDL role.
// Shared operator entrypoint owns migration admission and the dispatcher ACL
// installer. Never import it into the long-lived API or background dispatcher.
await import('/app/flow/tools/migrate.ts');
