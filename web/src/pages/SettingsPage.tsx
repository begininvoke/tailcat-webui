import { InfoCircleOutlined, LockOutlined, UserOutlined } from '@ant-design/icons'
import { Avatar, Card, Col, Descriptions, Row, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { useAuth } from '../app/auth'
import { LocaleThemeControls } from '../components/LocaleThemeControls'
import { PageHeader } from '../components/PageHeader'

export default function SettingsPage() {
  const { t } = useTranslation()
  const { user, config } = useAuth()
  return (
    <div className="page settings-page">
      <PageHeader title={t('settings.title')} subtitle={t('settings.subtitle')} />
      <Row gutter={[16, 16]}>
        <Col xs={24} lg={14}>
          <Card title={<Space><InfoCircleOutlined />{t('settings.appearance')}</Space>}>
            <LocaleThemeControls />
            <Typography.Paragraph type="secondary" className="settings-hint">{t('settings.themeHint')} {t('settings.languageHint')}</Typography.Paragraph>
          </Card>
        </Col>
        <Col xs={24} lg={10}>
          <Card title={<Space><UserOutlined />{t('settings.account')}</Space>}>
            <Space size={12}><Avatar className="user-avatar" size={48} src={user.avatar_url}>{(user.display_name || user.email || 'T')[0]}</Avatar><div><Typography.Text strong>{user.display_name || user.email}</Typography.Text><br /><Typography.Text type="secondary">{user.email}</Typography.Text></div></Space>
          </Card>
        </Col>
        <Col xs={24}>
          <Card title={<Space><LockOutlined />{t('settings.deployment')}</Space>}>
            <Descriptions column={{ xs: 1, sm: 2 }} items={[
              { key: 'version', label: t('settings.version'), children: <code>{config.version}</code> },
              { key: 'auth', label: t('settings.authMode'), children: config.auth_mode === 'oidc' ? t('settings.oidc') : t('settings.demo') },
            ]} />
          </Card>
        </Col>
      </Row>
    </div>
  )
}
