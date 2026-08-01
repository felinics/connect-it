import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { cp, mkdir, mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

const repositoryRoot = fileURLToPath(new URL('..', import.meta.url))

function run(command, args, cwd) {
  return execFileSync(command, args, { cwd, encoding: 'utf8' }).trim()
}

test('release script commits, tags, and atomically pushes the next version', async () => {
  const fixture = await mkdtemp(join(tmpdir(), 'connect-it-release-'))
  const worktree = join(fixture, 'worktree')
  const remote = join(fixture, 'origin.git')
  try {
    await mkdir(join(worktree, 'scripts'), { recursive: true })
    await cp(join(repositoryRoot, 'scripts', 'lib'), join(worktree, 'scripts', 'lib'), {
      recursive: true,
    })
    await cp(join(repositoryRoot, 'scripts', 'release.mjs'), join(worktree, 'scripts', 'release.mjs'))
    await cp(join(repositoryRoot, 'version.json'), join(worktree, 'version.json'))

    run('git', ['init', '--bare', remote], fixture)
    run('git', ['init', '-b', 'main'], worktree)
    run('git', ['config', 'user.name', 'Release Test'], worktree)
    run('git', ['config', 'user.email', 'release-test@example.com'], worktree)
    run('git', ['remote', 'add', 'origin', remote], worktree)
    run('git', ['add', 'version.json', 'scripts'], worktree)
    run('git', ['commit', '-m', 'chore: initialize release test'], worktree)
    run('git', ['push', '-u', 'origin', 'main'], worktree)

    run(process.execPath, ['scripts/release.mjs', '--yes'], worktree)

    const version = JSON.parse(await readFile(join(worktree, 'version.json'), 'utf8')).version
    assert.equal(version, '0.1.1')
    assert.equal(run('git', ['describe', '--tags', '--exact-match'], worktree), 'v0.1.1')
    assert.equal(run('git', [`--git-dir=${remote}`, 'tag', '--list', 'v0.1.1'], fixture), 'v0.1.1')
    assert.equal(
      run('git', [`--git-dir=${remote}`, 'log', '-1', '--format=%s', 'main'], fixture),
      'chore(release): v0.1.1',
    )
  } finally {
    await rm(fixture, { recursive: true })
  }
})
