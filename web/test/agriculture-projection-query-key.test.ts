import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { workspaceDirectoryQueryKey } from '../src/features/agriculture/projection-query-key';
import { authoritySnapshot } from '../../contracts/agriculture/authority-snapshot';

describe('workspace directory projection cache identity', () => {
    const user = { id: 'actor_a', role: 'user', status: 'active' };
    const snap = (id: string, fingerprint = 'a'.repeat(64)) => authoritySnapshot(user, [{ id, authorityFingerprint: fingerprint }]);
    test('actor, workspace authority, revoked grant and session generation cannot alias', () => {
        const base = workspaceDirectoryQueryKey(user.id, snap('workspace_a'), 1);
        for (const other of [
            workspaceDirectoryQueryKey('actor_b', snap('workspace_a'), 1),
            workspaceDirectoryQueryKey(user.id, snap('workspace_b'), 1),
            workspaceDirectoryQueryKey(user.id, snap('workspace_a', 'b'.repeat(64)), 1),
            workspaceDirectoryQueryKey(user.id, snap('workspace_a'), 2),
            workspaceDirectoryQueryKey(undefined, null, 1),
        ]) expect(other).not.toEqual(base);
        expect(workspaceDirectoryQueryKey(user.id, snap('workspace_a'), 1)).toEqual(base);
        for (const invalid of [-1, NaN, Infinity, 0.5]) expect(() => workspaceDirectoryQueryKey(user.id, snap('workspace_a'), invalid)).toThrow();
    });
    test('actual Home uses trusted boundary and does not retain an unmounted directory', () => {
        const source = readFileSync(new URL('../src/features/agriculture/main.tsx', import.meta.url), 'utf8');
        expect(source).toContain('value={baseline.current}');
        expect(source).toContain('workspaceDirectoryQueryKey(user?.id, authority, currentProjectionGeneration())');
        expect(source).toContain('authority !== null, staleTime: 0, gcTime: 0');
    });
});
