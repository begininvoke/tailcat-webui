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

  it('ships every exit-rule policy label in English and Chinese', () => {
    const labels = {
      exitRules: ['Exit rules', '出口规则'],
      settings: ['Server settings', '服务端设置'],
      exitRulesHint: ['Create an enabled rule before enabling exit-node forwarding.', '请先创建一条已启用的规则，再启用出口节点转发。'],
      addExitRule: ['Add exit rule', '添加出口规则'],
      exitRulePrefix: ['Allowed CIDR prefix', '允许的 CIDR 前缀'],
      startPort: ['Start port', '起始端口'],
      endPort: ['End port', '结束端口'],
      exitNodeEnabled: ['Exit-node forwarding', '出口节点转发'],
      ruleEnabled: ['Enabled', '已启用'],
      ruleDisabled: ['Disabled', '已禁用'],
      exitNodeDisabledHint: ['Create an enabled rule before enabling forwarding.', '请先创建一条已启用的规则，再启用转发。'],
      exitRulesEmpty: ['No exit rules', '还没有出口规则'],
      exitRulesEmptyDescription: ['Add an enabled CIDR and port range to define where this server may forward traffic.', '添加已启用的 CIDR 和端口范围，定义此服务端允许转发到的位置。'],
      deleteExitRuleTitle: ['Delete this exit rule?', '删除这条出口规则？'],
      deleteExitRuleDescription: ['Deleting an active rule safely stops this server before the change is applied.', '删除生效规则会先安全停止此服务端，再应用变更。'],
      exitRuleCreateFailed: ['Could not create the exit rule.', '无法创建出口规则。'],
      exitRuleDeleteFailed: ['Could not delete the exit rule.', '无法删除出口规则。'],
      exitNodeUpdateFailed: ['Could not update exit-node forwarding.', '无法更新出口节点转发。'],
      exitRulePrefixInvalid: ['Enter a valid IPv4 or IPv6 CIDR prefix.', '请输入有效的 IPv4 或 IPv6 CIDR 前缀。'],
      exitRulePortRange: ['The end port must be greater than or equal to the start port.', '结束端口必须大于或等于起始端口。'],
    } as const

    for (const [key, [english, chinese]] of Object.entries(labels)) {
      expect(i18n.getResource('en', 'translation', `servers.${key}`)).toBe(english)
      expect(i18n.getResource('zh-CN', 'translation', `servers.${key}`)).toBe(chinese)
    }
  })

  it('renders an error runtime state as localized Error', () => {
    expect(renderToStaticMarkup(createElement(RuntimeState, { state: 'error' }))).toContain('Error')
  })
})
