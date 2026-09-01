import { Alert, Descriptions, Flex, Progress, Space, Tag, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import type { DiagnosticPath, DiagnosticRun, DiagnosticStatus } from '../services/api'

const statusKeys = {
  running: 'diagnostics.running',
  succeeded: 'diagnostics.succeeded',
  failed: 'diagnostics.failed',
  canceled: 'diagnostics.canceled',
  interrupted: 'diagnostics.interrupted',
} satisfies Record<DiagnosticStatus, string>

const pathKeys = {
  direct: 'diagnostics.direct',
  derp: 'diagnostics.derp',
  peer_relay: 'diagnostics.peerRelay',
} satisfies Record<DiagnosticPath, string>

function localeFor(language: string) { return language === 'zh-CN' ? 'zh-CN' : 'en-US' }
function formatBytes(bytes: number, locale: string) {
  const units = ['B', 'KiB', 'MiB', 'GiB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit += 1 }
  return `${new Intl.NumberFormat(locale, { maximumFractionDigits: unit === 0 ? 0 : 1 }).format(value)} ${units[unit]}`
}
function formatRate(bitsPerSecond: number, locale: string) { return `${formatBytes(bitsPerSecond / 8, locale)}/s` }

export function DiagnosticPathTag({ path }: { path?: DiagnosticPath }) {
  const { t } = useTranslation()
  if (!path) return <Tag>{t('common.unavailable')}</Tag>
  return <Tag className={`diagnostic-path-tag diagnostic-path-${path.replace('_', '-')}`}>{t(pathKeys[path])}</Tag>
}

export function DiagnosticStatusTag({ status }: { status: DiagnosticStatus }) {
  const { t } = useTranslation()
  return <Tag className={`diagnostic-status-tag diagnostic-status-${status}`}>{t(statusKeys[status])}</Tag>
}

export function OperationProgress({ run, progress, compact = false }: { run: DiagnosticRun; progress?: number; compact?: boolean }) {
  const { t, i18n } = useTranslation()
  const locale = localeFor(i18n.resolvedLanguage ?? i18n.language)
  const percent = run.status === 'running' ? Math.min(100, Math.max(0, progress ?? 0)) : 100
  const isRunning = run.status === 'running'
  const phase = isRunning ? t('diagnostics.progressComplete', { percent }) : null
  const metrics = run.kind === 'ping'
    ? run.latency_ms == null ? [] : [{ key: 'latency', label: t('diagnostics.latency'), children: <span className="tabular-figure">{new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(run.latency_ms)} ms</span> }]
    : [
      { key: 'upload', label: t('diagnostics.upload'), children: <span className="tabular-figure">{formatBytes(run.upload_bytes, locale)} · {formatRate(run.upload_bps, locale)}</span> },
      { key: 'download', label: t('diagnostics.download'), children: <span className="tabular-figure">{formatBytes(run.download_bytes, locale)} · {formatRate(run.download_bps, locale)}</span> },
    ]

  return <div className={compact ? 'operation-progress operation-progress-compact' : 'operation-progress'} aria-busy={isRunning}>
    {compact ? phase && <Typography.Text className="operation-progress-summary" aria-live="polite">{phase}</Typography.Text> : <Flex gap={8} wrap="wrap" align="center" className="operation-progress-summary">
      <DiagnosticStatusTag status={run.status} />
      <DiagnosticPathTag path={run.path} />
      {phase && <Typography.Text aria-live="polite">{phase}</Typography.Text>}
    </Flex>}
    {isRunning && <Progress className="operation-progress-bar" percent={percent} status="normal" aria-label={t('diagnostics.progressLabel', { percent })} format={() => `${percent}%`} />}
    {metrics.length > 0 && <Descriptions size="small" column={compact ? 1 : { xs: 1, sm: 2 }} items={metrics} />}
    {run.error_code && <Alert className="operation-progress-error" type="error" showIcon message={t(`diagnostics.${run.error_code}`)} />}
    {!compact && run.kind === 'throughput' && <Space className="operation-progress-limits" size={8} wrap><Typography.Text type="secondary">{t('diagnostics.perDirection')}</Typography.Text><Typography.Text className="tabular-figure" type="secondary">{formatBytes(run.upload_bytes + run.download_bytes, locale)}</Typography.Text></Space>}
  </div>
}
