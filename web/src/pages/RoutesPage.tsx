import { DeleteOutlined, GlobalOutlined, LockOutlined, PlusOutlined, SafetyCertificateOutlined } from '@ant-design/icons'
import { App, Button, Card, Checkbox, Col, Descriptions, Divider, Drawer, Flex, Form, Grid, Input, InputNumber, Popconfirm, Radio, Row, Select, Space, Tag, Typography } from 'antd'
import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { CopyButton } from '../components/CopyButton'
import { PageHeader } from '../components/PageHeader'
import { ResourceState } from '../components/ResourceState'
import { useAsyncResource } from '../hooks/useAsyncResource'
import { api } from '../services/api'

interface RouteFormValues { client_id: string; name: string; slug: string; remote_port: number; base_path: string; access: 'private' | 'public'; allowed_methods: string[] }

export default function RoutesPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const screens = Grid.useBreakpoint()
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()
  const [createOpen, setCreateOpen] = useState(params.get('new') === '1')
  const [submitting, setSubmitting] = useState(false)
  const [form] = Form.useForm<RouteFormValues>()
  const load = useCallback(async () => {
    const [routes, clients] = await Promise.all([api.routes(), api.clients()])
    return { routes, clients }
  }, [])
  const resource = useAsyncResource(load)
  const closeCreate = () => {
    setCreateOpen(false)
    if (params.has('new')) { params.delete('new'); setParams(params, { replace: true }) }
  }
  const create = async (values: RouteFormValues) => {
    setSubmitting(true)
    try { await api.createRoute(values); closeCreate(); form.resetFields(); await resource.refresh() }
    catch { void message.error(t('feedback.createFailed')) } finally { setSubmitting(false) }
  }
  const remove = async (id: string) => {
    try { await api.deleteRoute(id); await resource.refresh(); void message.success(t('feedback.deleted')) }
    catch { void message.error(t('feedback.deleteFailed')) }
  }
  const clients = resource.data?.clients ?? []
  return (
    <div className="page routes-page">
      <PageHeader title={t('routes.title')} subtitle={t('routes.subtitle')} actions={<Button type="primary" icon={<PlusOutlined />} disabled={clients.length === 0} onClick={() => setCreateOpen(true)}>{t('routes.new')}</Button>} />
      {resource.data && clients.length === 0 && <Card className="inline-callout"><Flex align="center" justify="space-between" gap={12} wrap="wrap"><Space><SafetyCertificateOutlined /><Typography.Text>{t('routes.noClients')}</Typography.Text></Space><Button onClick={() => navigate('/clients?new=1')}>{t('clients.new')}</Button></Flex></Card>}
      <ResourceState loading={resource.loading} error={resource.error} empty={(resource.data?.routes.length ?? 0) === 0} emptyTitle={t('routes.empty')} emptyDescription={t('routes.emptyDescription')} emptyAction={clients.length > 0 ? <Button type="primary" onClick={() => setCreateOpen(true)}>{t('routes.new')}</Button> : undefined} retry={() => void resource.refresh()}>
        <Row gutter={[16, 16]}>{resource.data?.routes.map((route) => (
          <Col xs={24} xl={12} key={route.id}>
            <Card className="resource-card" title={<Flex align="center" gap={10}><GlobalOutlined /><span>{route.name}</span></Flex>} extra={<Tag icon={route.access === 'private' ? <LockOutlined /> : <GlobalOutlined />} color={route.access === 'public' ? 'warning' : undefined}>{route.access === 'public' ? t('routes.public') : t('routes.private')}</Tag>}>
              <Descriptions size="small" column={1} items={[
                { key: 'url', label: t('routes.preview'), children: <Space className="token-row"><Typography.Link href={route.url} target="_blank" rel="noreferrer" ellipsis>{route.url}</Typography.Link><CopyButton value={route.url} /></Space> },
                { key: 'port', label: t('routes.remotePort'), children: <code>{route.remote_port}</code> },
                { key: 'base', label: t('routes.basePath'), children: <code>{route.base_path}</code> },
                { key: 'methods', label: t('routes.methods'), children: <Space size={[4, 4]} wrap>{route.allowed_methods.map((method) => <Tag key={method}>{method}</Tag>)}</Space> },
              ]} />
              <Divider />
              <Flex gap={8}><Button type="primary" href={route.url} target="_blank" rel="noreferrer">{t('common.open')}</Button><Popconfirm title={t('routes.deleteTitle')} description={t('routes.deleteDescription')} okText={t('common.delete')} cancelText={t('common.cancel')} okButtonProps={{ danger: true }} onConfirm={() => void remove(route.id)}><Button danger icon={<DeleteOutlined />}>{t('common.delete')}</Button></Popconfirm></Flex>
            </Card>
          </Col>
        ))}</Row>
      </ResourceState>
      <Drawer title={t('routes.new')} width={500} placement={screens.md ? 'right' : 'bottom'} height={screens.md ? undefined : '88dvh'} open={createOpen} onClose={closeCreate} destroyOnHidden extra={<Button type="primary" loading={submitting} onClick={() => form.submit()}>{t('common.create')}</Button>}>
        <Form form={form} layout="vertical" initialValues={{ access: 'private', base_path: '/', allowed_methods: ['GET', 'HEAD'] }} onFinish={(values) => void create(values)}>
          <Form.Item name="name" label={t('common.name')} rules={[{ required: true, message: t('validation.required') }]}><Input autoFocus maxLength={80} /></Form.Item>
          <Form.Item name="client_id" label={t('routes.client')} rules={[{ required: true, message: t('validation.required') }]}><Select options={clients.map((client) => ({ value: client.id, label: client.name }))} /></Form.Item>
          <Form.Item name="slug" label={t('routes.slug')} rules={[{ required: true, pattern: /^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$/, message: t('validation.slug') }]}><Input addonBefore="/r/" maxLength={63} /></Form.Item>
          <Form.Item name="remote_port" label={t('routes.remotePort')} rules={[{ required: true, message: t('validation.port') }]}><InputNumber min={1} max={65535} className="full-width" /></Form.Item>
          <Form.Item name="base_path" label={t('routes.basePath')} rules={[{ required: true, message: t('validation.required') }]}><Input placeholder="/" /></Form.Item>
          <Form.Item name="access" label={t('routes.access')}><Radio.Group optionType="button" buttonStyle="solid" options={[{ value: 'private', label: t('routes.private') }, { value: 'public', label: t('routes.public') }]} /></Form.Item>
          <Form.Item name="allowed_methods" label={t('routes.methods')} tooltip={t('routes.methodsHint')}><Checkbox.Group options={[{ label: 'GET', value: 'GET', disabled: true }, 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS']} /></Form.Item>
        </Form>
      </Drawer>
    </div>
  )
}
