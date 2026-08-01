#!/usr/bin/env node

import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { createInterface } from 'node:readline/promises'
import { stdin, stdout } from 'node:process'

import { bumpPatch, compareVersions, parseVersion, readVersion, writeVersion } from './lib/version.mjs'

const repositoryRoot = fileURLToPath(new URL('..', import.meta.url))
const versionFile = fileURLToPath(new URL('../version.json', import.meta.url))

function git(args, options = {}) {
  const output = execFileSync('git', args, {
    cwd: repositoryRoot,
    encoding: 'utf8',
    stdio: options.inherit ? 'inherit' : ['ignore', 'pipe', 'pipe'],
  })
  return typeof output === 'string' ? output.trim() : ''
}

function fail(message) {
  console.error(`release: ${message}`)
  process.exit(1)
}

function localTagExists(tag) {
  try {
    git(['rev-parse', '--quiet', '--verify', `refs/tags/${tag}`])
    return true
  } catch {
    return false
  }
}

async function main() {
  if (git(['status', '--porcelain']) !== '') {
    fail('the working tree must be clean')
  }
  if (git(['branch', '--show-current']) !== 'main') {
    fail('releases must be created from main')
  }

  git(['fetch', 'origin', 'main', '--tags'], { inherit: true })
  if (git(['rev-parse', 'HEAD']) !== git(['rev-parse', 'origin/main'])) {
    fail('local main must exactly match origin/main before releasing')
  }

  const current = await readVersion(versionFile)
  const suggested = bumpPatch(current)
  const args = process.argv.slice(2).filter((arg) => arg !== '--')
  const assumeYes = args.includes('--yes')
  const requested = args.find((arg) => !arg.startsWith('-'))
  let next = requested
  let answer = ''
  const prompt = assumeYes ? null : createInterface({ input: stdin, output: stdout })
  try {
    if (!next) {
      if (assumeYes) {
        next = suggested
      } else {
        answer = await prompt.question(`Next version (${suggested}): `)
        next = answer.trim() || suggested
      }
    }
    parseVersion(next)
    if (compareVersions(next, current) <= 0) {
      fail(`next version ${next} must be greater than current version ${current}`)
    }

    const tag = `v${next}`
    if (localTagExists(tag)) {
      fail(`tag ${tag} already exists`)
    }

    console.log(`Current version: ${current}`)
    console.log(`Next version:    ${next}`)
    if (!assumeYes) {
      answer = await prompt.question(`Release ${tag}? (Y/n) `)
      if (answer.trim() !== '' && answer.trim().toLowerCase() !== 'y') {
        console.log('Release cancelled.')
        return
      }
    }

    await writeVersion(versionFile, next)
    git(['add', '--', 'version.json'], { inherit: true })
    git(['commit', '-m', `chore(release): ${tag}`], { inherit: true })
    git(['tag', '-a', tag, '-m', tag], { inherit: true })
    git(['push', '--atomic', 'origin', 'main', tag], { inherit: true })
    console.log(`Released ${tag}. GitHub Actions will publish the image and GitHub Release.`)
  } finally {
    prompt?.close()
  }
}

main().catch((error) => fail(error instanceof Error ? error.message : String(error)))
