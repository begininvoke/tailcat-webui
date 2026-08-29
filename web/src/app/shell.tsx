import { ApiOutlined, DashboardOutlined, GlobalOutlined, LogoutOutlined, MenuFoldOutlined, MenuUnfoldOutlined, SettingOutlined, SwapOutlined } from '@ant-design/icons'
import { App, Avatar, Button, Dropdown, Grid, Layout, Menu, Space, Typography, type MenuProps } from 'antd'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from './auth'
import { LocaleThemeControls } from '../components/LocaleThemeControls'
import { useRuntimeEvents } from '../hooks/useRuntimeEvents'

const { Header, Sider, Content } = Layout

export function AppShell() {
  useRuntimeEvents()
  const { t } = useTranslation()
  const { user, logout } = useAuth()
  const { message } = App.useApp()
  const screens = Grid.useBreakpoint()
  const desktop = Boolean(screens.lg)
  const [collapsed, setCollapsed] = useState(false)
  const navigate = useNavigate()
  const location = useLocation()
  const navItems: MenuProps['items'] = [
    { key: '/', icon: <DashboardOutlined />, label: t('nav.overview') },
    { key: '/servers', icon: <ApiOutlined />, label: t('nav.servers') },
    { key: '/clients', icon: <SwapOutlined />, label: t('nav.clients') },
    { key: '/routes', icon: <GlobalOutlined />, label: t('nav.routes') },
    { key: '/settings', icon: <SettingOutlined />, label: t('nav.settings') },
  ]
  const selected = location.pathname === '/' ? '/' : `/${location.pathname.split('/')[1]}`
  const accountItems: MenuProps['items'] = [{ key: 'logout', icon: <LogoutOutlined />, label: t('auth.logout'), danger: true }]
  const onAccountClick: MenuProps['onClick'] = async ({ key }) => {
    if (key === 'logout') {
      try { await logout() } catch { void message.error(t('feedback.loadFailed')) }
    }
  }

  return (
    <Layout className="app-layout">
      <a className="skip-link" href="#main-content">{t('common.skip')}</a>
      {desktop && (
        <Sider width={224} collapsedWidth={72} collapsible collapsed={collapsed} trigger={null} className="app-sider">
          <button className="brand-button" onClick={() => navigate('/')} aria-label={t('brand.name')}>
            <img src="/tailcat.png" alt="" /><span className="brand-copy"><strong>{t('brand.name')}</strong><small>{t('brand.tagline')}</small></span>
          </button>
          <Menu mode="inline" selectedKeys={[selected]} items={navItems} onClick={({ key }) => navigate(key)} />
          <Button className="collapse-button" type="text" icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />} aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'} onClick={() => setCollapsed((value) => !value)} />
        </Sider>
      )}
      <Layout>
        <Header className="app-header">
          {!desktop && <Space><img className="mobile-logo" src="/tailcat.png" alt="" /><Typography.Text strong>{t('brand.name')}</Typography.Text></Space>}
          <Space className="header-actions">
            <LocaleThemeControls compact />
            <Dropdown menu={{ items: accountItems, onClick: onAccountClick }} trigger={['click']}>
              <Button type="text" className="account-button"><Avatar className="user-avatar" src={user.avatar_url}>{(user.display_name || user.email || 'T').slice(0, 1).toUpperCase()}</Avatar>{desktop && <span>{user.display_name || user.email}</span>}</Button>
            </Dropdown>
          </Space>
        </Header>
        <Content id="main-content" tabIndex={-1} className="app-content"><Outlet /></Content>
        {!desktop && <nav className="mobile-nav" aria-label="Primary"><Menu mode="horizontal" selectedKeys={[selected]} items={navItems} onClick={({ key }) => navigate(key)} /></nav>}
      </Layout>
    </Layout>
  )
}
