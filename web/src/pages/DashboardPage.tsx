import { ApiOutlined, ArrowRightOutlined, GlobalOutlined, PlusOutlined, SwapOutlined } from '@ant-design/icons'
import { Button, Card, Col, List, Row, Space, Statistic, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router-dom'
import { PageHeader } from '../components/PageHeader'
import { ResourceState } from '../components/ResourceState'
import { RuntimeState } from '../components/RuntimeState'
import { useAsyncResource } from '../hooks/useAsyncResource'
import { api } from '../services/api'

export default function DashboardPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const resource = useAsyncResource(api.dashboard)
  const data = resource.data
  return (
    <div className="page dashboard-page">
      <PageHeader title={t('dashboard.title')} subtitle={t('dashboard.subtitle')} />
      <ResourceState loading={resource.loading} error={resource.error} empty={false} emptyTitle="" emptyDescription="" retry={() => void resource.refresh()}>
        {data && <>
          <Row gutter={[16, 16]}>
            <Col xs={24} md={8}><Card className="metric-card"><Statistic title={t('dashboard.servers')} value={data.servers.total} prefix={<ApiOutlined />} /><Typography.Text type="secondary">{t('dashboard.running', { count: data.servers.running })}</Typography.Text></Card></Col>
            <Col xs={24} md={8}><Card className="metric-card"><Statistic title={t('dashboard.clients')} value={data.clients.total} prefix={<SwapOutlined />} /><Typography.Text type="secondary">{t('dashboard.reachable', { count: data.clients.reachable })}</Typography.Text></Card></Col>
            <Col xs={24} md={8}><Card className="metric-card"><Statistic title={t('dashboard.routes')} value={data.routes.total} prefix={<GlobalOutlined />} /><Typography.Text type="secondary">{t('dashboard.public', { count: data.routes.public })}</Typography.Text></Card></Col>
          </Row>
          <Row gutter={[16, 16]} className="dashboard-lower">
            <Col xs={24} lg={16}>
              <Card title={t('dashboard.recentServers')} extra={<Button type="link" onClick={() => navigate('/servers')}>{t('common.open')} <ArrowRightOutlined /></Button>}>
                {data.recent_servers.length === 0 ? <Typography.Text type="secondary">{t('dashboard.empty')}</Typography.Text> : (
                  <List dataSource={data.recent_servers} renderItem={(server) => <List.Item extra={<RuntimeState state={server.runtime_state} compact />}><List.Item.Meta title={server.name} description={`${server.region} · ${server.key_mode === 'saved' ? t('servers.saved') : t('servers.ephemeral')}`} /></List.Item>} />
                )}
              </Card>
            </Col>
            <Col xs={24} lg={8}>
              <Card title={t('dashboard.quickStart')}>
                <Space direction="vertical" className="quick-actions" size={10}>
                  <Button block icon={<PlusOutlined />} onClick={() => navigate('/servers?new=1')}>{t('dashboard.addServer')}</Button>
                  <Button block icon={<SwapOutlined />} onClick={() => navigate('/clients?new=1')}>{t('dashboard.addClient')}</Button>
                  <Button block icon={<GlobalOutlined />} onClick={() => navigate('/routes?new=1')}>{t('dashboard.publishRoute')}</Button>
                </Space>
              </Card>
            </Col>
          </Row>
        </>}
      </ResourceState>
    </div>
  )
}
