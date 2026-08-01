import assert from 'node:assert/strict'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

import { bumpPatch, compareVersions, parseVersion, readVersion, writeVersion } from './version.mjs'

test('parseVersion accepts stable semver and rejects ambiguous versions', () => {
  assert.deepEqual(parseVersion('0.1.0'), [0, 1, 0])
  assert.deepEqual(parseVersion('12.34.56'), [12, 34, 56])
  for (const value of ['v1.2.3', '1.2', '1.2.3-beta.1', '01.2.3']) {
    assert.throws(() => parseVersion(value), /stable semver/)
  }
})

test('bumpPatch and compareVersions use numeric semver ordering', () => {
  assert.equal(bumpPatch('1.9.99'), '1.9.100')
  assert.equal(compareVersions('1.10.0', '1.9.9'), 1)
  assert.equal(compareVersions('1.2.3', '1.2.3'), 0)
  assert.equal(compareVersions('0.9.9', '1.0.0'), -1)
})

test('readVersion and writeVersion preserve the canonical file shape', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'connect-it-version-'))
  const path = join(directory, 'version.json')
  try {
    await writeVersion(path, '2.3.4')
    assert.equal(await readVersion(path), '2.3.4')
  } finally {
    await rm(directory, { recursive: true })
  }
})
