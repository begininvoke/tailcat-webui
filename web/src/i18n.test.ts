import { describe, expect, it } from 'vitest'
import i18n from './i18n'

describe('localization resources', () => {
  it('ships matching navigation labels in English and Chinese', () => {
    expect(i18n.getResource('en', 'translation', 'nav.servers')).toBe('Servers')
    expect(i18n.getResource('zh-CN', 'translation', 'nav.servers')).toBe('服务端')
  })

  it('localizes interactive tunnel controls', () => {
    expect(i18n.getResource('en', 'translation', 'clients.tunnel')).toBe('TCP tunnel')
    expect(i18n.getResource('zh-CN', 'translation', 'clients.tunnel')).toBe('TCP 隧道')
  })
})
