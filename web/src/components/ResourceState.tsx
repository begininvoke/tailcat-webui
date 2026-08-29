import { Button, Empty, Result, Skeleton } from 'antd'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

export function ResourceState({ loading, error, empty, emptyTitle, emptyDescription, emptyAction, retry, children }: {
  loading: boolean; error: unknown; empty: boolean; emptyTitle: string; emptyDescription: string;
  emptyAction?: ReactNode; retry: () => void; children: ReactNode
}) {
  const { t } = useTranslation()
  if (loading) return <div className="surface-grid"><Skeleton active /><Skeleton active /></div>
  if (error) return <Result status="error" title={t('feedback.loadFailed')} extra={<Button onClick={retry}>{t('common.retry')}</Button>} />
  if (empty) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={<><strong>{emptyTitle}</strong><div className="empty-description">{emptyDescription}</div></>} >{emptyAction}</Empty>
  return children
}
