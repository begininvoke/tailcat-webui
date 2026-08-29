import { Badge, Tag } from 'antd'
import { useTranslation } from 'react-i18next'

export function RuntimeState({ state, compact = false }: { state: string; compact?: boolean }) {
  const { t } = useTranslation()
  const status = state === 'running' || state === 'ready' ? 'success' : state === 'error' ? 'error' : 'default'
  const label = state === 'running' ? t('common.running') : state === 'ready' ? t('common.ready') : state === 'stopped' ? t('common.stopped') : t('common.idle')
  return compact ? <Badge status={status} text={label} /> : <Tag color={status === 'success' ? 'success' : status === 'error' ? 'error' : 'default'}>{label}</Tag>
}
