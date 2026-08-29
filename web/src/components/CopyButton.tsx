import { CopyOutlined } from '@ant-design/icons'
import { App, Button, Tooltip } from 'antd'
import { useTranslation } from 'react-i18next'

export function CopyButton({ value }: { value: string }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value)
      void message.success(t('common.copied'))
    } catch {
      void message.error(t('common.copyFailed'))
    }
  }
  return <Tooltip title={t('common.copy')}><Button type="text" size="small" aria-label={t('common.copy')} icon={<CopyOutlined />} onClick={() => void copy()} /></Tooltip>
}
