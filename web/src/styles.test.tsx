/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const stylesheet = readFileSync('src/styles.css', 'utf8')

describe('touch target styles', () => {
  it('keeps the password visibility control at least 44px square', () => {
    const rule = stylesheet.match(/(?:^|\n)\.ant-input-password-icon\s*\{([^}]*)\}/)?.[1]
    expect(rule).toBeDefined()
    expect(rule).toMatch(/(?:^|;)\s*min-width:\s*44px(?:;|$)/)
    expect(rule).toMatch(/(?:^|;)\s*min-height:\s*44px(?:;|$)/)
  })
})
