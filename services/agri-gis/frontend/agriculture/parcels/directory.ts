import type { ParcelAPI } from './api'
import type { ParcelContext } from './types'
export const contextKey = (context: ParcelContext): string => `${context.workspace_id}:${context.farm_id}:${[...context.allowed_actions].sort().join(',')}`
export async function selectedContext(api: Pick<ParcelAPI, 'context'>, workspace: string, selected: ParcelContext | null, items: ParcelContext[], more: boolean, signal: AbortSignal): Promise<ParcelContext | null> {
  signal.throwIfAborted()
  if (!selected) return null
  const listed = items.find(v => v.farm_id === selected.farm_id)
  if (listed) return listed
  if (more) return selected
  // An item omitted from the first page may still be authorized on later pages.
  const result = await api.context(workspace, selected.farm_id, signal)
  signal.throwIfAborted()
  return result
}
