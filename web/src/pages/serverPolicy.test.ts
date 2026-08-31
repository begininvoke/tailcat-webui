import { describe, expect, it } from 'vitest'
import type { ExitRule } from '../services/api'
import { beginExitNodeUpdate, beginServerSettingsLoad, completeExitNodeUpdate, completeServerSettingsLoad, emptyExitNodeUpdate, invalidateExitNodeUpdate, isValidCIDR } from './serverPolicy'

const exitRule = (id: string, enabled: boolean): ExitRule => ({
  id, server_id: 'server-a', prefix: '10.0.0.0/8', start_port: 443, end_port: 443, enabled,
  created_at: '2026-08-31T00:00:00Z', updated_at: '2026-08-31T00:00:00Z',
})

describe('server policy helpers', () => {
  it('accepts valid IPv4 and IPv6 CIDR values only', () => {
    expect(isValidCIDR('10.0.0.0/8')).toBe(true)
    expect(isValidCIDR('2001:db8:abcd::/48')).toBe(true)
    expect(isValidCIDR('300.0.0.1/24')).toBe(false)
    expect(isValidCIDR('10.0.0.1/33')).toBe(false)
    expect(isValidCIDR('2001:db8:::1/64')).toBe(false)
    expect(isValidCIDR('2001:db8::1/129')).toBe(false)
    expect(isValidCIDR(' 10.0.0.0/8')).toBe(false)
  })

  it('keeps server B settings empty while a late server A response resolves', () => {
    const serverA = beginServerSettingsLoad('server-a', 1)
    const serverB = beginServerSettingsLoad('server-b', 2)
    const lateA = completeServerSettingsLoad(serverB, serverA.requestID, {
      mappings: [], allowedClients: [], exitRules: [exitRule('rule-a', true)],
    })

    expect(lateA).toEqual(serverB)

    const completeB = completeServerSettingsLoad(serverB, serverB.requestID, {
      mappings: [], allowedClients: [], exitRules: [exitRule('rule-b', false)],
    })
    expect(completeB.exitRules.map((rule) => rule.id)).toEqual(['rule-b'])
    expect(completeB.loading).toBe(false)
  })

  it('clears an exit-node update when its settings generation is invalidated', () => {
    const updateA = beginExitNodeUpdate(1)
    const invalidated = invalidateExitNodeUpdate(updateA)

    expect(invalidated.updating).toBe(false)
    expect(completeExitNodeUpdate(invalidated, updateA.updateID)).toEqual(invalidated)

    const updateB = beginExitNodeUpdate(2)
    expect(completeExitNodeUpdate(updateB, updateA.updateID)).toEqual(updateB)
    expect(emptyExitNodeUpdate()).toEqual({ updateID: 0, updating: false })
  })
})
