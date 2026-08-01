import { readFile, writeFile } from 'node:fs/promises'

const stableSemver = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/

export function parseVersion(value) {
  const match = stableSemver.exec(value)
  if (!match) {
    throw new Error(`version must be stable semver (MAJOR.MINOR.PATCH), got ${JSON.stringify(value)}`)
  }
  return match.slice(1).map(Number)
}

export function compareVersions(left, right) {
  const a = parseVersion(left)
  const b = parseVersion(right)
  for (let index = 0; index < a.length; index += 1) {
    if (a[index] !== b[index]) return a[index] < b[index] ? -1 : 1
  }
  return 0
}

export function bumpPatch(version) {
  const [major, minor, patch] = parseVersion(version)
  return `${major}.${minor}.${patch + 1}`
}

export async function readVersion(path) {
  const contents = JSON.parse(await readFile(path, 'utf8'))
  if (typeof contents.version !== 'string') {
    throw new Error('version.json must contain a string property named version')
  }
  parseVersion(contents.version)
  return contents.version
}

export async function writeVersion(path, version) {
  parseVersion(version)
  await writeFile(path, `${JSON.stringify({ version }, null, 2)}\n`)
}
