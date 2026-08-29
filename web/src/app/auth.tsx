import { LockOutlined } from '@ant-design/icons'
import { App, Button, Card, Flex, Result, Skeleton, Space, Typography } from 'antd'
import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { api, APIError, type PublicConfig, type User } from '../services/api'
import { LocaleThemeControls } from '../components/LocaleThemeControls'

interface AuthValue {
  user: User
  config: PublicConfig
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthValue | null>(null)

export function AuthGate({ children }: { children: ReactNode }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [config, setConfig] = useState<PublicConfig | null>(null)
  const [user, setUser] = useState<User | null>(null)
  const [loading, setLoading] = useState(true)
  const [fatal, setFatal] = useState(false)

  const load = async () => {
    setLoading(true)
    setFatal(false)
    try {
      const publicConfig = await api.config()
      setConfig(publicConfig)
      try {
        setUser(await api.me())
      } catch (error) {
        if (!(error instanceof APIError) || error.status !== 401) throw error
        setUser(null)
      }
    } catch {
      setFatal(true)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    let active = true
    const initialize = async () => {
      try {
        const publicConfig = await api.config()
        let currentUser: User | null = null
        try {
          currentUser = await api.me()
        } catch (error) {
          if (!(error instanceof APIError) || error.status !== 401) throw error
        }
        if (active) {
          setConfig(publicConfig)
          setUser(currentUser)
        }
      } catch {
        if (active) setFatal(true)
      } finally {
        if (active) setLoading(false)
      }
    }
    void initialize()
    return () => { active = false }
  }, [])

  if (loading) {
    return <div className="center-stage"><Card className="loading-card"><Skeleton active paragraph={{ rows: 4 }} /></Card></div>
  }
  if (fatal || !config) {
    return <Result status="500" title={t('feedback.loadFailed')} extra={<Button type="primary" onClick={() => void load()}>{t('common.retry')}</Button>} />
  }
  if (!user) {
    const demoLogin = async () => {
      try { setUser(await api.demoLogin()) } catch { void message.error(t('auth.failed')) }
    }
    return <LoginPage config={config} onDemoLogin={demoLogin} />
  }

  const value: AuthValue = {
    user,
    config,
    logout: async () => { await api.logout(); setUser(null) },
  }
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

function LoginPage({ config, onDemoLogin }: { config: PublicConfig; onDemoLogin: () => Promise<void> }) {
  const { t } = useTranslation()
  return (
    <main className="login-shell">
      <div className="login-toolbar"><LocaleThemeControls compact /></div>
      <section className="login-copy">
        <div className="login-eyebrow"><span className="signal-line" />{t('auth.eyebrow')}</div>
        <Typography.Title className="login-title">{t('auth.title')}</Typography.Title>
        <Typography.Paragraph className="login-description">{t('auth.description')}</Typography.Paragraph>
        <Space direction="vertical" size={12} className="login-actions">
          {config.auth_mode === 'demo' ? (
            <Button type="primary" size="large" block onClick={() => void onDemoLogin()}>{t('auth.demo')}</Button>
          ) : (
            <Button type="primary" size="large" block href="/api/v1/auth/login?return_to=/">{t('auth.oidc')}</Button>
          )}
          <Flex align="center" gap={8} className="secure-note"><LockOutlined /><span>{t('auth.secure')}</span></Flex>
        </Space>
      </section>
      <aside className="login-mark" aria-hidden="true">
        <div className="logo-orbit"><img src="/tailcat.png" alt="" /></div>
        <div className="network-lines"><i /><i /><i /></div>
      </aside>
    </main>
  )
}

export function useAuth() {
  const value = useContext(AuthContext)
  if (!value) throw new Error('useAuth must be used inside AuthGate')
  return value
}
