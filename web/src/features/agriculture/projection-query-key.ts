// Workspace enumeration is an actor's cross-tenant membership directory, not
// a selected tenant's content query. Its trusted session snapshot contains the
// exact workspace authority fingerprints; the URL never supplies this scope.
export function workspaceDirectoryQueryKey(actorId: string | undefined, authority: string | null, generation: number) {
    if (!Number.isSafeInteger(generation) || generation < 0) throw new Error('Invalid projection generation');
    return ['agriculture', 'workspaces', 'actor-membership-directory:v1', actorId ?? null, authority, generation] as const;
}
