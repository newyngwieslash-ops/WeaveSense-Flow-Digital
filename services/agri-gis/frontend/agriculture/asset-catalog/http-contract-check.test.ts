import { expect, test } from 'bun:test'
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { asset, context, intent, operation, preview } from './tests/fixtures'
const runner = new URL('./http-contract-check.mjs', import.meta.url).pathname
const envelope = (data: unknown) => ({ data, meta: { request_id: 'r1', schema_version: '1.0' } })
test('private asset HTTP checker exercises original parsers on synthetic contract fixture, not actual service acceptance', () => {
  const directory = mkdtempSync(join(tmpdir(), 'asset-http-parser-')), file = join(directory, 'snapshot.json')
  try {
    chmodSync(directory, 0o700)
    const fixture = { context: envelope(context), list: { ...envelope([asset]), next_cursor: '', has_more: false }, detail: envelope(asset), historical: envelope(asset), preview: envelope(preview), operation: envelope(operation), preview_input: { workspace_id: 'w1', ...intent } }
    writeFileSync(file, JSON.stringify(fixture), { mode: 0o600 })
    const valid = spawnSync(process.execPath, [runner, file], { encoding: 'utf8' }); expect(valid.status).toBe(0); expect(valid.stderr).toBe(''); expect(JSON.parse(valid.stdout)).toEqual({ schema_version: '1.0', passed: true, checks: 7 })
    writeFileSync(file, JSON.stringify({ ...fixture, detail: envelope({ ...asset, metadata: { ...asset.metadata, secret: 'private@example.com' } }) }))
    const invalid = spawnSync(process.execPath, [runner, file], { encoding: 'utf8' }); expect(invalid.status).toBe(1); expect(invalid.stderr).toBe(''); expect(JSON.parse(invalid.stdout)).toEqual({ schema_version: '1.0', passed: false, error_code: 'ASSET_HTTP_CONTRACT_INVALID' }); expect(invalid.stdout).not.toContain('private@example.com'); expect(invalid.stdout).not.toContain(directory)
    chmodSync(file, 0o644); const publicFile = spawnSync(process.execPath, [runner, file], { encoding: 'utf8' }); expect(publicFile.status).toBe(1); expect(publicFile.stdout).toBe(invalid.stdout)
  } finally { rmSync(directory, { recursive: true, force: true }) }
})
