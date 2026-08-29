import { GlobalOutlined, MoonOutlined, SunOutlined } from '@ant-design/icons'
import { Button, Dropdown, Segmented, Space, Tooltip, type MenuProps } from 'antd'
import { useTranslation } from 'react-i18next'
import { usePreferences, type ThemeChoice } from '../app/preferences'

export function LocaleThemeControls({ compact = false }: { compact?: boolean }) {
  const { t } = useTranslation()
  const { language, setLanguage, themeChoice, setThemeChoice, resolvedTheme } = usePreferences()
  const languageItems: MenuProps['items'] = [
    { key: 'en', label: 'English' },
    { key: 'zh-CN', label: '简体中文' },
  ]
  if (compact) {
    return (
      <Space size={4}>
        <Dropdown menu={{ items: languageItems, selectedKeys: [language], onClick: ({ key }) => setLanguage(key as 'en' | 'zh-CN') }} trigger={['click']}>
          <Tooltip title={t('settings.language')}><Button type="text" aria-label={t('settings.language')} icon={<GlobalOutlined />} /></Tooltip>
        </Dropdown>
        <Tooltip title={t('settings.appearance')}>
          <Button type="text" aria-label={t('settings.appearance')} icon={resolvedTheme === 'dark' ? <MoonOutlined /> : <SunOutlined />} onClick={() => setThemeChoice(resolvedTheme === 'dark' ? 'light' : 'dark')} />
        </Tooltip>
      </Space>
    )
  }
  return (
    <Space direction="vertical" size={12} className="preference-controls">
      <Segmented block value={themeChoice} onChange={(value) => setThemeChoice(value as ThemeChoice)} options={[
        { value: 'light', label: t('settings.light'), icon: <SunOutlined /> },
        { value: 'dark', label: t('settings.dark'), icon: <MoonOutlined /> },
        { value: 'system', label: t('settings.system') },
      ]} />
      <Segmented block value={language} onChange={(value) => setLanguage(value as 'en' | 'zh-CN')} options={[
        { value: 'en', label: t('settings.english') },
        { value: 'zh-CN', label: t('settings.chinese') },
      ]} />
    </Space>
  )
}
