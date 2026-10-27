import { readdirSync, readFileSync } from 'node:fs'
import { join, relative, sep } from 'node:path'
import { describe, expect, it } from 'vitest'

// Vitest runs with the frontend folder as its root.
const srcDir = join(process.cwd(), 'src')
const selfFile = join(srcDir, '__tests__', 'quality.spec.ts')

const ANY_TYPE = /:\s*any\b|\bas\s+any\b|<\s*any\s*[>,]|,\s*any\s*>|\bany\s*\[\]|\|\s*any\b/

function violations(source: string): string[] {
  const found: string[] = []
  if (source.includes('v-html')) found.push('v-html')
  if (ANY_TYPE.test(source)) found.push('any type')
  return found
}

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) return sourceFiles(path)
    return /\.(ts|vue)$/.test(entry.name) ? [path] : []
  })
}

describe('source quality', () => {
  it('has no `any` type and no v-html in src/', () => {
    const offenders = sourceFiles(srcDir)
      .filter((file) => file !== selfFile && relative(srcDir, file) !== `api${sep}schema.d.ts`)
      .flatMap((file) =>
        violations(readFileSync(file, 'utf8')).map((v) => `${relative(srcDir, file)}: ${v}`),
      )
    expect(offenders).toEqual([])
  })

  it('finds source files to check', () => {
    expect(sourceFiles(srcDir).length).toBeGreaterThan(5)
  })

  describe('matcher self-check', () => {
    it.each([
      'let a: any',
      'x as any',
      'f<any>(x)',
      'Map<any, string>',
      'Map<string, any>',
      'let a: any[]',
      'type T = string | any',
      '<div v-html="x"></div>',
    ])('catches %s', (code) => {
      expect(violations(code)).not.toEqual([])
    })

    it.each([
      '// Any other message',
      '// returns the first match, if any',
      'const text = "Any other message"',
      'const company = 1',
      '<p>Is there any book?</p>',
    ])('ignores %s', (text) => {
      expect(violations(text)).toEqual([])
    })
  })
})
