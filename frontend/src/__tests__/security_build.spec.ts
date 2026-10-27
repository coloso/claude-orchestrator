// @vitest-environment node
// Security regression tests (S04-08): what the source may contain, how the dev server and the
// build are configured, and what the dependency files may look like. All checks run offline.
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { join, relative } from 'node:path'
import { describe, expect, it } from 'vitest'

import viteConfig from '../../vite.config'

// Vitest runs with the frontend folder as its root.
const root = process.cwd()
const srcDir = join(root, 'src')

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) return entry.name === '__tests__' ? [] : sourceFiles(path)
    if (entry.name === 'schema.d.ts') return []
    return /\.(ts|vue)$/.test(entry.name) ? [path] : []
  })
}

// Each pattern names a way to turn data into markup, a URL, code or a navigation.
const FORBIDDEN: ReadonlyArray<readonly [string, RegExp]> = [
  ['raw HTML directive', new RegExp('v-' + 'html')],
  ['raw HTML property', /\b(innerHTML|outerHTML|insertAdjacentHTML|srcdoc)\b|document\.write/],
  ['code from a string', /\beval\s*\(|new\s+Function\b|\bset(Timeout|Interval)\s*\(\s*['"`]/],
  ['bound URL or style attribute', /(?:^|[\s<])(?::|v-bind:)(?:href|src|srcdoc|style|action|formaction|data|xlink:href)\s*=/m],
  ['v-bind object (can bind any attribute)', /\sv-bind\s*=/],
  ['script URL', /javascript\s*:/i],
  ['runtime template compilation', /\bcompile\s*\(|\btemplate\s*:|vue\/dist\/vue\.(esm-browser|esm-bundler)|createApp\(\s*\{/],
  ['browser storage', /\b(localStorage|sessionStorage|indexedDB)\b|document\.cookie/],
  ['navigation outside the router', /\bwindow\.open\b|\blocation\.(assign|replace)\s*\(|\blocation(\.href)?\s*=[^=]|\.postMessage\b/],
  ['hard-coded host', /https?:\/\//],
  ['cross-origin or credentialed fetch', /\bcredentials\s*:|\bmode\s*:\s*['"]cors/],
  ['environment variable other than BASE_URL', /import\.meta\.env\.(?!BASE_URL\b)/],
]

function violations(source: string): string[] {
  const found = FORBIDDEN.filter(([, pattern]) => pattern.test(source)).map(([name]) => name)
  // Every tag with a target needs rel="noopener noreferrer".
  for (const tag of source.match(/<[^<>]*\starget\s*=[^<>]*>/g) ?? []) {
    if (!/rel\s*=\s*["'][^"']*\bnoopener\b[^"']*\bnoreferrer\b|rel\s*=\s*["'][^"']*\bnoreferrer\b[^"']*\bnoopener\b/.test(tag)) {
      found.push('target without rel="noopener noreferrer"')
    }
  }
  return found
}

describe('security: source', () => {
  it('has no way to turn data into markup, code, a script URL or a navigation', () => {
    const files = sourceFiles(srcDir)
    expect(files.length).toBeGreaterThan(5)
    const offenders = files.flatMap((file) =>
      violations(readFileSync(file, 'utf8')).map((v) => `${relative(srcDir, file)}: ${v}`),
    )
    expect(offenders).toEqual([])
  })

  describe('matcher self-check', () => {
    it.each([
      '<div v-' + 'html="x"></div>',
      'el.innerHTML = x',
      'el.insertAdjacentHTML("beforeend", x)',
      'eval(x)',
      'new Function("return 1")',
      'setTimeout("alert(1)", 1)',
      '<a :href="url">x</a>',
      '<img :src="url">',
      '<p :style="s">x</p>',
      '<a v-bind:href="u">x</a>',
      '<a v-bind="attrs">x</a>',
      'const u = "javascript:alert(1)"',
      'createApp({ template: "<p/>" })',
      'compile(x)',
      'localStorage.setItem("a", "b")',
      'window.open(url)',
      'location.href = url',
      'location.assign(url)',
      'const base = "http://localhost:8000"',
      'fetch(u, { credentials: "include" })',
      'import.meta.env.VITE_TOKEN',
      '<a href="/x" target="_blank">x</a>',
      '<a href="/x" target="_blank" rel="noopener">x</a>',
    ])('catches %s', (code) => {
      expect(violations(code)).not.toEqual([])
    })

    it.each([
      '<a href="/books" target="_blank" rel="noopener noreferrer">x</a>',
      '<a href="/books" target="_blank" rel="noreferrer noopener">x</a>',
      '<RouterLink :to="{ name: \'book\', params: { id } }">x</RouterLink>',
      '<input :id="id" :type="type ?? \'text\'" :aria-invalid="e">',
      '<label :for="id">x</label>',
      'baseUrl: globalThis.location.origin,',
      'createPinia()',
      'if (location.origin === x) return',
      'import.meta.env.BASE_URL',
      'const text = `Deleted ${title}`',
    ])('ignores %s', (code) => {
      expect(violations(code)).toEqual([])
    })
  })

  it('serves a page with one external module script and nothing inline', () => {
    const html = readFileSync(join(root, 'index.html'), 'utf8')

    const scripts = html.match(/<script\b[^>]*>/g) ?? []
    expect(scripts).toEqual(['<script type="module" src="/src/main.ts">'])
    expect(html).not.toMatch(/<script\b[^>]*>\s*(?!<\/script>)\S/)
    expect(html).not.toMatch(/\son[a-z]+\s*=/i)
    expect(html).not.toMatch(/<base\b|<iframe\b|<object\b|<embed\b|http-equiv/i)
    expect(html).not.toMatch(/https?:\/\/|\s(href|src)\s*=\s*["']\/\//)
  })

  it('has only the favicon in public/ and no .env files', () => {
    expect(readdirSync(join(root, 'public'))).toEqual(['favicon.ico'])
    const envFiles = readdirSync(root).filter((name) => name.startsWith('.env') && name !== '.env.example')
    expect(envFiles).toEqual([])
  })
})

async function resolveOptions(options: unknown): Promise<unknown[]> {
  const value = await options
  if (Array.isArray(value)) return (await Promise.all(value.map(resolveOptions))).flat()
  return value ? [value] : []
}

describe('security: dev server and build configuration', () => {
  const server = viteConfig.server ?? {}

  it('applies Vue devtools to the dev server only', async () => {
    const plugins = await resolveOptions(viteConfig.plugins)
    const named = plugins.filter(
      (p): p is { name: string; apply?: unknown } =>
        typeof p === 'object' && p !== null && 'name' in p && typeof p.name === 'string',
    )
    const devtools = named.filter((p) => /devtools/i.test(p.name))

    expect(devtools.length).toBeGreaterThan(0)
    for (const plugin of devtools) expect(plugin.apply, plugin.name).toBe('serve')
  })

  it('does not ship source maps', () => {
    expect([undefined, false]).toContain(viteConfig.build?.sourcemap)
  })

  it('listens on the loopback interface only', () => {
    expect([undefined, 'localhost', '127.0.0.1', '::1']).toContain(server.host)
    expect(server.allowedHosts).toBeUndefined()
    expect(server.cors).toBeUndefined()
    expect(server.fs?.strict).not.toBe(false)
    expect(server.fs?.allow).toBeUndefined()
  })

  it('proxies /api and nothing else, to a local backend', () => {
    expect(Object.keys(server.proxy ?? {})).toEqual(['/api'])
    const target = (server.proxy ?? {})['/api']
    expect(typeof target).toBe('string')
    const url = new URL(String(target))
    expect(url.protocol).toBe('http:')
    expect(['localhost', '127.0.0.1', '[::1]']).toContain(url.hostname)
  })

  it('exposes no environment variable to the browser and aliases only @', () => {
    expect(viteConfig.envPrefix).toBeUndefined()
    expect(viteConfig.define).toBeUndefined()
    expect(Object.keys((viteConfig.resolve?.alias ?? {}) as Record<string, string>)).toEqual(['@'])
  })
})

interface LockPackage {
  version?: string
  resolved?: string
  integrity?: string
  link?: boolean
  optional?: boolean
  hasInstallScript?: boolean
  dependencies?: Record<string, string>
  devDependencies?: Record<string, string>
}
interface Lock {
  lockfileVersion: number
  packages: Record<string, LockPackage>
}
interface Manifest {
  scripts?: Record<string, string>
  dependencies: Record<string, string>
  devDependencies: Record<string, string>
  overrides?: Record<string, unknown>
}

const manifest = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8')) as Manifest
const lock = JSON.parse(readFileSync(join(root, 'package-lock.json'), 'utf8')) as Lock
const locked = Object.entries(lock.packages).filter(([path]) => path !== '')

describe('security: dependencies (offline)', () => {
  it('uses lockfile version 3', () => {
    expect(lock.lockfileVersion).toBe(3)
  })

  it('takes every package from registry.npmjs.org with an integrity hash', () => {
    expect(locked.length).toBeGreaterThan(100)
    const offenders = locked.flatMap(([path, pkg]) => {
      const problems: string[] = []
      if (!path.startsWith('node_modules/')) problems.push('outside node_modules')
      if (pkg.link === true) problems.push('link')
      if (!pkg.resolved?.startsWith('https://registry.npmjs.org/')) problems.push(`resolved ${pkg.resolved}`)
      if (!pkg.integrity?.startsWith('sha512-')) problems.push(`integrity ${pkg.integrity}`)
      return problems.map((p) => `${path}: ${p}`)
    })
    expect(offenders).toEqual([])
  })

  it('has no install script except the optional macOS file watcher', () => {
    const withScripts = locked.filter(([, pkg]) => pkg.hasInstallScript === true).map(([path]) => path)
    expect(withScripts).toEqual(['node_modules/fsevents'])
    expect(lock.packages['node_modules/fsevents']?.optional).toBe(true)
  })

  it('declares only ranges from the registry, and no lifecycle scripts of its own', () => {
    const all = { ...manifest.dependencies, ...manifest.devDependencies }
    for (const [name, range] of Object.entries(all)) {
      expect(range, name).toMatch(/^[\^~]?\d+\.\d+\.\d+$/)
    }
    const lifecycle = ['preinstall', 'install', 'postinstall', 'prepare', 'prepublish']
    expect(Object.keys(manifest.scripts ?? {}).filter((s) => lifecycle.includes(s))).toEqual([])
  })

  it('has exactly four runtime dependencies, so a new one is reviewed on purpose', () => {
    expect(Object.keys(manifest.dependencies).sort()).toEqual(['openapi-fetch', 'pinia', 'vue', 'vue-router'])
  })

  it('has one override, which only points the type generator at our TypeScript', () => {
    expect(manifest.overrides).toEqual({ 'openapi-typescript': { typescript: '$typescript' } })
  })

  it('locks every declared package to a version its range allows, and mirrors them in the root entry', () => {
    const rootEntry = lock.packages['']
    expect(rootEntry?.dependencies).toEqual(manifest.dependencies)
    expect(rootEntry?.devDependencies).toEqual(manifest.devDependencies)

    const all = { ...manifest.dependencies, ...manifest.devDependencies }
    for (const [name, range] of Object.entries(all)) {
      const version = lock.packages[`node_modules/${name}`]?.version
      expect(version, name).toBeDefined()
      const want = range.replace(/^[\^~]/, '').split('.').map(Number)
      const got = String(version).split('.').map(Number)
      const [wMajor = 0, wMinor = 0, wPatch = 0] = want
      const [gMajor = 0, gMinor = 0, gPatch = 0] = got
      const atLeast = gMajor > wMajor || (gMajor === wMajor && (gMinor > wMinor || (gMinor === wMinor && gPatch >= wPatch)))
      expect(atLeast, `${name} ${version} is older than ${range}`).toBe(true)
      expect(gMajor, `${name} ${version} is outside ${range}`).toBe(wMajor)
      if (range.startsWith('~') || range.match(/^\d/)) expect(gMinor, `${name} ${version} is outside ${range}`).toBe(wMinor)
      if (/^\d/.test(range)) expect(gPatch, `${name} ${version} is not pinned to ${range}`).toBe(wPatch)
    }
  })

  it('has no .npmrc that changes the registry', () => {
    const path = join(root, '.npmrc')
    if (existsSync(path)) expect(readFileSync(path, 'utf8')).not.toMatch(/registry\s*=/)
  })
})
