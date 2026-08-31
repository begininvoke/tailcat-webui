import { describe, expect, it } from 'vitest'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import i18n from './i18n'
import { RuntimeState } from './components/RuntimeState'

describe('localization resources', () => {
  it('ships matching navigation labels in English and Chinese', () => {
    expect(i18n.getResource('en', 'translation', 'nav.servers')).toBe('Servers')
    expect(i18n.getResource('zh-CN', 'translation', 'nav.servers')).toBe('服务端')
  })

  it('localizes interactive tunnel controls', () => {
    expect(i18n.getResource('en', 'translation', 'clients.tunnel')).toBe('TCP tunnel')
    expect(i18n.getResource('zh-CN', 'translation', 'clients.tunnel')).toBe('TCP 隧道')
  })

  it('renders an error runtime state as localized Error', () => {
    expect(renderToStaticMarkup(createElement(RuntimeState, { state: 'error' }))).toContain('Error')
  })
})
