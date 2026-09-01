import { CheckCircleOutlined, ClockCircleOutlined, CloseCircleOutlined, CloudDownloadOutlined, DeleteOutlined, ExclamationCircleOutlined, PauseCircleOutlined } from '@ant-design/icons'
import { Alert, Flex, Progress, Tag, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import type { TransferErrorCode, TransferStatus } from '../services/api'

export interface TransferProgressProps {
  status: TransferStatus
  receivedBytes: number
  totalBytes: number
  completedFiles: number
  totalFiles: number
  errorCode?: TransferErrorCode
  compact?: boolean
}

const statusKeys = {
  staging: 'transfers.staging', ready: 'transfers.ready', running: 'transfers.running', completed: 'transfers.completed',
  failed: 'transfers.failed', canceled: 'transfers.canceled', interrupted: 'transfers.interrupted', expired: 'transfers.expired', deleting: 'transfers.deleting',
} satisfies Record<TransferStatus, string>

const statusIcons = {
  staging: <ClockCircleOutlined aria-hidden />, ready: <CheckCircleOutlined aria-hidden />, running: <CloudDownloadOutlined aria-hidden />, completed: <CheckCircleOutlined aria-hidden />,
  failed: <CloseCircleOutlined aria-hidden />, canceled: <PauseCircleOutlined aria-hidden />, interrupted: <ExclamationCircleOutlined aria-hidden />, expired: <ClockCircleOutlined aria-hidden />, deleting: <DeleteOutlined aria-hidden />,
} satisfies Record<TransferStatus, React.ReactNode>

export function formatTransferBytes(bytes: number, locale: string) {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let value = Math.max(0, Number.isFinite(bytes) ? bytes : 0)
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit += 1 }
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: unit === 0 ? 0 : 1 }).format(value)} ${units[unit]}`
}

export function TransferProgress({ status, receivedBytes, totalBytes, completedFiles, totalFiles, errorCode, compact = false }: TransferProgressProps) {
  const { t, i18n } = useTranslation()
  const locale = i18n.resolvedLanguage === 'zh-CN' ? 'zh-CN' : 'en-US'
  const safeTotal = Math.max(0, totalBytes)
  const safeReceived = Math.max(0, receivedBytes)
  const percent = safeTotal === 0 ? (status === 'completed' ? 100 : 0) : Math.min(100, Math.round((safeReceived / safeTotal) * 100))
  return <div className={compact ? 'transfer-progress transfer-progress-compact' : 'transfer-progress'} aria-busy={status === 'running'}>
    <Flex className="transfer-progress-summary" align="center" gap={8} wrap="wrap">
      <Tag className={`transfer-status-tag transfer-status-${status}`} icon={statusIcons[status]}>{t(statusKeys[status])}</Tag>
      <Typography.Text className="tabular-figure transfer-progress-amounts" aria-live="polite">
        {formatTransferBytes(safeReceived, locale)} / {formatTransferBytes(safeTotal, locale)} · {Math.max(0, completedFiles)} / {Math.max(0, totalFiles)} {t('transfers.files')}
      </Typography.Text>
    </Flex>
    <Progress className="transfer-progress-bar" percent={percent} status="normal" aria-label={t('transfers.progressLabel', { percent })} format={() => `${percent}%`} strokeColor={status === 'failed' ? 'var(--ant-color-error)' : undefined} />
    {errorCode && <Alert className="transfer-progress-error" type="error" showIcon message={t(`transfers.${errorCode}`)} />}
  </div>
}
