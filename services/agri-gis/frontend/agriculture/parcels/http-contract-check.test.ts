import { expect, test } from 'bun:test'
import { chmodSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
const runner = new URL('./http-contract-check.mjs', import.meta.url).pathname
test('private parcel HTTP checker emits only fixed diagnostic without reflecting malformed snapshots or paths', () => {
  const directory = mkdtempSync(join(tmpdir(), 'parcel-http-parser-')), file = join(directory, 'private.json')
  try {
    chmodSync(directory, 0o700); writeFileSync(file, '{"secret":"never print contact@example.com"}', { mode: 0o600 })
    const child = spawnSync(process.execPath, [runner, file], { encoding: 'utf8' })
    expect(child.status).toBe(1); expect(child.stderr).toBe('')
    expect(JSON.parse(child.stdout)).toEqual({ schema_version: '1.0', passed: false, error_code: 'PARCEL_HTTP_CONTRACT_INVALID' })
    expect(child.stdout).not.toContain('contact@example.com'); expect(child.stdout).not.toContain(directory)
    chmodSync(file, 0o644)
    const publicFile = spawnSync(process.execPath, [runner, file], { encoding: 'utf8' })
    expect(publicFile.status).toBe(1); expect(publicFile.stdout).toBe(child.stdout)
  } finally { rmSync(directory, { recursive: true, force: true }) }
})
