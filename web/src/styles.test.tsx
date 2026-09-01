/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const stylesheet = readFileSync('src/styles.css', 'utf8')

describe('touch target styles', () => {
  it('keeps the password visibility control at least 44px square', () => {
    expect(stylesheet).toContain('.ant-input-password-icon')
    expect(stylesheet).toContain('min-width: 44px')
    expect(stylesheet).toContain('min-height: 44px')
  })
})
