// Node-only fixture CLI. Bundle with esbuild to execute the original TS parsers.
import { closeSync, constants, fstatSync, lstatSync, openSync, readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import process from 'node:process'
import { record } from '../farms/transport.ts'
import { metadataFrom } from './metadata.ts'
import { assetFrom, contextFrom, grantsFrom, id, operationFrom, pageFrom, positive, previewFrom } from './types.ts'
function requireValue(condition) { if (!condition) throw Error('invalid fixture contract') }
function envelope(v) { requireValue(record(v) && 'data' in v && record(v.meta) && v.meta.schema_version === '1.0' && id(v.meta.request_id)); return v }
function main() {
  requireValue(process.argv.length === 3 && typeof process.getuid === 'function')
  const path = resolve(process.argv[2]), directory = lstatSync(dirname(path)), entry = lstatSync(path), uid = process.getuid()
  requireValue(directory.isDirectory() && !directory.isSymbolicLink() && directory.uid === uid && (directory.mode & 0o777) === 0o700 && entry.isFile() && !entry.isSymbolicLink() && entry.uid === uid && entry.nlink === 1 && (entry.mode & 0o777) === 0o600)
  const fd = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW); let input
  try { const stat = fstatSync(fd); requireValue(stat.isFile() && stat.uid === uid && stat.nlink === 1 && (stat.mode & 0o777) === 0o600 && stat.ino === entry.ino && stat.dev === entry.dev && stat.size > 0 && stat.size <= 16 * 1024 * 1024); input = JSON.parse(readFileSync(fd, 'utf8')) } finally { closeSync(fd) }
  requireValue(record(input) && Object.keys(input).sort().join(',') === 'context,detail,historical,list,operation,preview,preview_input')
  const rawContext = envelope(input.context).data; requireValue(record(rawContext) && id(rawContext.workspace_id)); const workspace = rawContext.workspace_id
  contextFrom(rawContext, workspace)
  const list = envelope(input.list); pageFrom(list.data, list.next_cursor, list.has_more, workspace)
  const detail = assetFrom(envelope(input.detail).data, workspace), historical = assetFrom(envelope(input.historical).data, workspace)
  requireValue(historical.asset_id === detail.asset_id && historical.version <= detail.version && historical.revision === detail.revision && detail.version === detail.revision && detail.status === 'validating' && detail.entity === null)
  const raw = input.preview_input; requireValue(record(raw) && raw.workspace_id === workspace)
  const metadata = metadataFrom(raw.metadata); let intent
  if (raw.action === 'create') { requireValue(Object.keys(raw).sort().join(',') === 'action,expected_revision,initial_grants,metadata,workspace_id' && raw.expected_revision === 0); const initial_grants = grantsFrom(raw.initial_grants); requireValue(initial_grants.length > 0); intent = { action: 'create', expected_revision: 0, metadata, initial_grants } }
  else { requireValue(raw.action === 'version' && Object.keys(raw).sort().join(',') === 'action,asset_id,expected_revision,metadata,workspace_id' && raw.asset_id === detail.asset_id && positive(raw.expected_revision)); intent = { action: 'version', asset_id: raw.asset_id, expected_revision: raw.expected_revision, metadata } }
  const preview = previewFrom(envelope(input.preview).data, intent), operation = operationFrom(envelope(input.operation).data, workspace)
  requireValue(operation.operation_id === preview.preview_id && operation.action === intent.action && operation.state === 'completed' && operation.asset_id === detail.asset_id && operation.revision === detail.version && operation.asset_version_id === detail.asset_version_id)
  process.stdout.write(JSON.stringify({ schema_version: '1.0', passed: true, checks: 7 }) + '\n')
}
try { main() } catch { process.stdout.write(JSON.stringify({ schema_version: '1.0', passed: false, error_code: 'ASSET_HTTP_CONTRACT_INVALID' }) + '\n'); process.exitCode = 1 }
