// Test-process CLI: consumes private real HTTP snapshots, never browser code.
import { closeSync, constants, fstatSync, lstatSync, openSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import process from 'node:process'
import { record } from '../farms/transport.ts'
import { boundaryFrom, contextFrom, contextPageFrom, historyFrom, id, operationFrom, pageFrom, parcelFrom, positive, previewFrom, text } from './types.ts'

function requireValue(condition) { if (!condition) throw Error('invalid contract') }
function envelope(value) {
  requireValue(record(value) && 'data' in value && record(value.meta) && value.meta.schema_version === '1.0' && id(value.meta.request_id))
  return value
}
function main() {
  const file = process.argv[2]; requireValue(file && process.argv.length === 3 && typeof process.getuid === 'function')
  const path = resolve(file), parent = lstatSync(dirname(path)), entry = lstatSync(path), owner = process.getuid()
  requireValue(parent.isDirectory() && !parent.isSymbolicLink() && parent.uid === owner && (parent.mode & 0o777) === 0o700)
  requireValue(entry.isFile() && !entry.isSymbolicLink() && entry.uid === owner && entry.nlink === 1 && (entry.mode & 0o777) === 0o600)
  const fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW)
  let input
  try {
    const stat = fstatSync(fd)
    requireValue(stat.isFile() && stat.uid === owner && stat.nlink === 1 && (stat.mode & 0o777) === 0o600 && stat.size > 0 && stat.size <= 16 * 1024 * 1024 && stat.ino === entry.ino && stat.dev === entry.dev)
    input = JSON.parse(readFileSync(fd, 'utf8'))
  } finally { closeSync(fd) }
  requireValue(record(input) && Object.keys(input).sort().join(',') === 'context,detail,history,list,operation,parents,preview,preview_input')
  const contextEnvelope = envelope(input.context); requireValue(record(contextEnvelope.data) && id(contextEnvelope.data.workspace_id) && id(contextEnvelope.data.farm_id))
  const workspace = contextEnvelope.data.workspace_id, parentID = contextEnvelope.data.farm_id
  contextFrom(contextEnvelope.data, workspace, parentID)
  const parents = envelope(input.parents); contextPageFrom(parents.data, parents.next_cursor, parents.has_more, workspace)
  const list = envelope(input.list); pageFrom(list.data, list.next_cursor, list.has_more, workspace, parentID)
  const detail = parcelFrom(envelope(input.detail).data, workspace, parentID)
  const history = envelope(input.history); historyFrom(history.data, history.next_cursor, history.has_more, workspace, parentID, detail.id)
  const raw = input.preview_input
  requireValue(record(raw) && raw.workspace_id === workspace && text(raw.reason) && ['create', 'revise'].includes(String(raw.action)))
  let intent
  if (raw.action === 'create') {
    requireValue(Object.keys(raw).sort().join(',') === 'action,expected_farm_revision,expected_revision,farm_id,parcel,reason,workspace_id' && raw.farm_id === parentID && raw.expected_revision === 0 && positive(raw.expected_farm_revision) && record(raw.parcel) && text(raw.parcel.name, 200) && typeof raw.parcel.external_code === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(raw.parcel.external_code))
    intent = { action: 'create', farm_id: parentID, expected_revision: 0, expected_farm_revision: raw.expected_farm_revision, parcel: { external_code: raw.parcel.external_code, name: raw.parcel.name, ...boundaryFrom(raw.parcel) }, reason: raw.reason }
  } else {
    requireValue(Object.keys(raw).sort().join(',') === 'action,coordinate_order,expected_revision,geometry,parcel_id,reason,source_crs,workspace_id' && raw.parcel_id === detail.id && positive(raw.expected_revision))
    intent = { action: 'revise', parcel_id: detail.id, expected_revision: raw.expected_revision, ...boundaryFrom(raw), reason: raw.reason }
  }
  const preview = previewFrom(envelope(input.preview).data, intent, parentID)
  const operation = operationFrom(envelope(input.operation).data, workspace, parentID)
  requireValue(operation.operation_id === preview.preview_id && operation.action === intent.action && operation.state === 'completed' && operation.parcel_id === detail.id && operation.geometry_revision === detail.geometry_revision)
  process.stdout.write(JSON.stringify({ schema_version: '1.0', passed: true, checks: 8 }) + '\n')
}
try { main() } catch { process.stdout.write(JSON.stringify({ schema_version: '1.0', passed: false, error_code: 'PARCEL_HTTP_CONTRACT_INVALID' }) + '\n'); process.exitCode = 1 }
