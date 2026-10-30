// Display continuity only. A digest never authorizes a request or proves GIS ownership.
export function authoritySnapshot(user: { id: string; role: string; status: string } | null,
  workspaces: Array<{ id: string; authorityFingerprint: string }>): string {
  if (user !== null && (!user || typeof user.id !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(user.id) || !['admin', 'user'].includes(user.role) || user.status !== 'active')) throw new Error('Invalid session identity')
  if (!Array.isArray(workspaces) || workspaces.some(row => !row || typeof row.id !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(row.id) || typeof row.authorityFingerprint !== 'string' || !/^[a-f0-9]{64}$/.test(row.authorityFingerprint)) || new Set(workspaces.map(row => row.id)).size !== workspaces.length) throw new Error('Invalid authority fingerprint')
  return JSON.stringify({ user: user ? { id: user.id, role: user.role, status: user.status } : null,
    workspaces: workspaces.map(row => ({ id: row.id, authorityFingerprint: row.authorityFingerprint })).sort((a, b) => a.id.localeCompare(b.id)) })
}
