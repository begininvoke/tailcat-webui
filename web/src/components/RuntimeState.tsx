import { Badge, Tag } from 'antd'
import { useTranslation } from 'react-i18next'
import type { RuntimePhase } from '../services/api'

const runtimeStateKeys = {
  idle: 'common.idle',
  starting: 'common.starting',
  connecting: 'common.connecting',
  ready: 'common.ready',
  running: 'common.running',
  stopping: 'common.stopping',
  stopped: 'common.stopped',
  error: 'common.error',
  interrupted: 'common.interrupted',
} satisfies Record<RuntimePhase, string>

export function RuntimeState({ state, compact = false }: { state: RuntimePhase; compact?: boolean }) {
  const { t } = useTranslation()
  const status = state === 'running' || state === 'ready' ? 'success' : state === 'error' ? 'error' : 'default'
  const label = t(runtimeStateKeys[state])
  return compact ? <Badge status={status} text={label} /> : <Tag color={status === 'success' ? 'success' : status === 'error' ? 'error' : 'default'}>{label}</Tag>
}
