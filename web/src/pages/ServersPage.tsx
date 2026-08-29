import { DeleteOutlined, LinkOutlined, PlayCircleOutlined, PlusOutlined, PoweroffOutlined, SettingOutlined } from '@ant-design/icons'
import { Alert, App, Button, Card, Col, Descriptions, Divider, Drawer, Empty, Flex, Form, Grid, Input, InputNumber, List, Popconfirm, Radio, Row, Space, Switch, Tag, Typography } from 'antd'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router-dom'
import { useAuth } from '../app/auth'
import { CopyButton } from '../components/CopyButton'
import { PageHeader } from '../components/PageHeader'
import { ResourceState } from '../components/ResourceState'
import { RuntimeState } from '../components/RuntimeState'
import { useAsyncResource } from '../hooks/useAsyncResource'
import { api, type AllowedClient, type PortMapping, type Server } from '../services/api'

interface ServerFormValues {
  name: string; key_mode: 'ephemeral' | 'saved'; region: string; derp_map_url?: string; exit_node_enabled: boolean; start: boolean
}

interface MappingFormValues {
  name: string; kind: 'tcp' | 'no_auth_ssh'; listen_port: number; target_host?: string; target_port?: number
}

interface AllowedClientFormValues { name: string; public_key: string }

export default function ServersPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { config } = useAuth()
  const screens = Grid.useBreakpoint()
  const [params, setParams] = useSearchParams()
  const [createOpen, setCreateOpen] = useState(params.get('new') === '1')
  const [submitting, setSubmitting] = useState(false)
  const [busyID, setBusyID] = useState('')
  const [mappingServer, setMappingServer] = useState<Server | null>(null)
  const [mappings, setMappings] = useState<PortMapping[]>([])
  const [allowedClients, setAllowedClients] = useState<AllowedClient[]>([])
  const [mappingsLoading, setMappingsLoading] = useState(false)
  const [mappingForm] = Form.useForm<MappingFormValues>()
  const [allowedForm] = Form.useForm<AllowedClientFormValues>()
  const [serverForm] = Form.useForm<ServerFormValues>()
  const resource = useAsyncResource(api.servers)
  const mappingKind = Form.useWatch('kind', mappingForm)

  const closeCreate = () => {
    setCreateOpen(false)
    if (params.has('new')) { params.delete('new'); setParams(params, { replace: true }) }
  }

  const createServer = async (values: ServerFormValues) => {
    setSubmitting(true)
    try {
      await api.createServer(values)
      closeCreate(); serverForm.resetFields(); await resource.refresh()
    } catch { void message.error(t('servers.createFailed')) } finally { setSubmitting(false) }
  }

  const changeState = async (server: Server) => {
    setBusyID(server.id)
    try {
      if (server.runtime_state === 'running') await api.stopServer(server.id)
      else await api.startServer(server.id)
      await resource.refresh()
    } catch { void message.error(t('servers.startFailed')) } finally { setBusyID('') }
  }

  const remove = async (id: string) => {
    setBusyID(id)
    try { await api.deleteServer(id); await resource.refresh(); void message.success(t('feedback.deleted')) }
    catch { void message.error(t('feedback.deleteFailed')) } finally { setBusyID('') }
  }

  const openMappings = async (server: Server) => {
    setMappingServer(server); setMappingsLoading(true)
    try {
      const [nextMappings, nextAllowed] = await Promise.all([api.mappings(server.id), api.allowedClients(server.id)])
      setMappings(nextMappings); setAllowedClients(nextAllowed)
    } catch { void message.error(t('feedback.loadFailed')) }
    finally { setMappingsLoading(false) }
  }

  const createMapping = async (values: MappingFormValues) => {
    if (!mappingServer) return
    setSubmitting(true)
    try {
      await api.createMapping(mappingServer.id, { ...values, target_host: values.target_host || '', target_port: values.target_port || 0 })
      mappingForm.resetFields(); setMappings(await api.mappings(mappingServer.id)); await resource.refresh()
    } catch { void message.error(t('feedback.createFailed')) } finally { setSubmitting(false) }
  }

  const deleteMapping = async (id: string) => {
    if (!mappingServer) return
    try { await api.deleteMapping(id); setMappings(await api.mappings(mappingServer.id)); await resource.refresh() }
    catch { void message.error(t('feedback.deleteFailed')) }
  }

  const createAllowedClient = async (values: AllowedClientFormValues) => {
    if (!mappingServer) return
    setSubmitting(true)
    try {
      await api.createAllowedClient(mappingServer.id, values)
      allowedForm.resetFields(); setAllowedClients(await api.allowedClients(mappingServer.id)); await resource.refresh()
    } catch { void message.error(t('feedback.createFailed')) } finally { setSubmitting(false) }
  }

  const deleteAllowedClient = async (id: string) => {
    if (!mappingServer) return
    try { await api.deleteAllowedClient(id); setAllowedClients(await api.allowedClients(mappingServer.id)); await resource.refresh() }
    catch { void message.error(t('feedback.deleteFailed')) }
  }

  return (
    <div className="page servers-page">
      <PageHeader title={t('servers.title')} subtitle={t('servers.subtitle')} actions={<Button type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>{t('servers.new')}</Button>} />
      <ResourceState loading={resource.loading} error={resource.error} empty={(resource.data?.length ?? 0) === 0} emptyTitle={t('servers.empty')} emptyDescription={t('servers.emptyDescription')} emptyAction={<Button type="primary" onClick={() => setCreateOpen(true)}>{t('servers.new')}</Button>} retry={() => void resource.refresh()}>
        <Row gutter={[16, 16]}>{resource.data?.map((server) => (
          <Col xs={24} xl={12} key={server.id}>
            <Card className="resource-card" title={<Flex align="center" gap={10}><span>{server.name}</span><RuntimeState state={server.runtime_state} /></Flex>} extra={<Tag>{server.key_mode === 'saved' ? t('servers.saved') : t('servers.ephemeral')}</Tag>}>
              <Descriptions size="small" column={1} items={[
                { key: 'region', label: t('servers.region'), children: <code>{server.region}</code> },
                { key: 'mappings', label: t('servers.mappings'), children: server.mapping_count },
                { key: 'allowlist', label: t('servers.allowlist'), children: server.allowlist_enabled ? t('servers.denyUnknown') : t('servers.allowAll') },
                ...(server.public_key ? [{ key: 'key', label: t('servers.publicKey'), children: <Space className="token-row"><Typography.Text ellipsis copyable={false} className="mono-value">{server.public_key}</Typography.Text><CopyButton value={server.public_key} /></Space> }] : []),
                ...(server.connection_token ? [{ key: 'token', label: t('servers.token'), children: <Space className="token-row"><Typography.Text ellipsis className="mono-value">{server.connection_token}</Typography.Text><CopyButton value={server.connection_token} /></Space> }] : []),
              ]} />
              <Divider />
              <Flex gap={8} wrap="wrap">
                <Button icon={<SettingOutlined />} onClick={() => void openMappings(server)}>{t('servers.mappings')}</Button>
                <Button type={server.runtime_state === 'running' ? 'default' : 'primary'} danger={server.runtime_state === 'running'} loading={busyID === server.id} icon={server.runtime_state === 'running' ? <PoweroffOutlined /> : <PlayCircleOutlined />} onClick={() => void changeState(server)}>{server.runtime_state === 'running' ? t('common.stop') : t('common.start')}</Button>
                <Popconfirm title={t('servers.deleteTitle')} description={t('servers.deleteDescription')} okText={t('common.delete')} cancelText={t('common.cancel')} okButtonProps={{ danger: true }} onConfirm={() => void remove(server.id)}>
                  <Button danger icon={<DeleteOutlined />}>{t('common.delete')}</Button>
                </Popconfirm>
              </Flex>
            </Card>
          </Col>
        ))}</Row>
      </ResourceState>

      <Drawer title={t('servers.new')} width={480} placement={screens.md ? 'right' : 'bottom'} height={screens.md ? undefined : '86dvh'} open={createOpen} onClose={closeCreate} destroyOnHidden extra={<Button type="primary" loading={submitting} onClick={() => serverForm.submit()}>{t('common.create')}</Button>}>
        <Form form={serverForm} layout="vertical" initialValues={{ key_mode: 'ephemeral', region: 'auto', exit_node_enabled: false, start: true }} onFinish={(values) => void createServer(values)}>
          <Form.Item name="name" label={t('common.name')} rules={[{ required: true, message: t('validation.required') }]}><Input autoFocus maxLength={80} /></Form.Item>
          <Form.Item name="key_mode" label={t('servers.keyMode')}><Radio.Group optionType="button" buttonStyle="solid" options={[{ value: 'ephemeral', label: t('servers.ephemeral') }, { value: 'saved', label: t('servers.saved') }]} /></Form.Item>
          <Form.Item name="region" label={t('servers.region')} tooltip="auto, region ID, region code, or custom DERP hostname"><Input placeholder="auto" /></Form.Item>
          <Form.Item name="derp_map_url" label={t('servers.customMap')}><Input type="url" placeholder="https://example.com/derpmap.json" /></Form.Item>
          <Form.Item name="exit_node_enabled" label={t('servers.exitNode')} valuePropName="checked"><Switch /></Form.Item>
          <Form.Item name="start" label={t('servers.startNow')} valuePropName="checked"><Switch /></Form.Item>
        </Form>
      </Drawer>

      <Drawer title={mappingServer ? `${mappingServer.name} · ${t('servers.mappings')}` : t('servers.mappings')} width={560} placement={screens.md ? 'right' : 'bottom'} height={screens.md ? undefined : '92dvh'} open={Boolean(mappingServer)} onClose={() => setMappingServer(null)} destroyOnHidden>
        <Typography.Paragraph type="secondary">{t('servers.mappingsHint')}</Typography.Paragraph>
        {mappingServer?.runtime_state === 'running' && <Alert type="warning" showIcon message={t('servers.mappingsHint')} className="drawer-alert" />}
        <List loading={mappingsLoading} dataSource={mappings} locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} /> }} renderItem={(mapping) => (
          <List.Item actions={[<Popconfirm key="delete" title={t('common.delete')} onConfirm={() => void deleteMapping(mapping.id)}><Button type="text" danger icon={<DeleteOutlined />} aria-label={t('common.delete')} /></Popconfirm>]}>
            <List.Item.Meta avatar={<LinkOutlined />} title={`${mapping.listen_port} · ${mapping.name}`} description={mapping.kind === 'no_auth_ssh' ? t('servers.ssh') : `${mapping.target_host}:${mapping.target_port}`} />
          </List.Item>
        )} />
        <Divider>{t('servers.addMapping')}</Divider>
        <Form form={mappingForm} disabled={mappingServer?.runtime_state === 'running'} layout="vertical" initialValues={{ kind: 'tcp', target_host: '127.0.0.1' }} onFinish={(values) => void createMapping(values)}>
          <Form.Item name="name" label={t('common.name')} rules={[{ required: true, message: t('validation.required') }]}><Input maxLength={80} /></Form.Item>
          <Form.Item name="kind" label={t('servers.kind')}><Radio.Group options={[{ value: 'tcp', label: t('servers.tcp') }, ...(config.unsafe_ssh ? [{ value: 'no_auth_ssh', label: t('servers.ssh') }] : [])]} /></Form.Item>
          <Form.Item name="listen_port" label={t('servers.listenPort')} rules={[{ required: true, message: t('validation.port') }]}><InputNumber min={1} max={65535} className="full-width" /></Form.Item>
          {mappingKind !== 'no_auth_ssh' && <>
            <Form.Item name="target_host" label={t('servers.targetHost')} rules={[{ required: true, message: t('validation.required') }]}><Input /></Form.Item>
            <Form.Item name="target_port" label={t('servers.targetPort')} rules={[{ required: true, message: t('validation.port') }]}><InputNumber min={1} max={65535} className="full-width" /></Form.Item>
          </>}
          <Button type="primary" htmlType="submit" loading={submitting} block>{t('servers.addMapping')}</Button>
        </Form>
        <Divider>{t('servers.allowlist')}</Divider>
        <Typography.Paragraph type="secondary">{t('servers.allowlistHint')}</Typography.Paragraph>
        <List dataSource={allowedClients} locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} /> }} renderItem={(client) => (
          <List.Item actions={[<Popconfirm key="delete" title={t('common.delete')} onConfirm={() => void deleteAllowedClient(client.id)}><Button type="text" danger icon={<DeleteOutlined />} aria-label={t('common.delete')} /></Popconfirm>]}>
            <List.Item.Meta title={client.name} description={<Typography.Text className="mono-value" ellipsis>{client.public_key}</Typography.Text>} />
          </List.Item>
        )} />
        <Form form={allowedForm} layout="vertical" onFinish={(values) => void createAllowedClient(values)}>
          <Form.Item name="name" label={t('common.name')} rules={[{ required: true, message: t('validation.required') }]}><Input maxLength={80} /></Form.Item>
          <Form.Item name="public_key" label={t('servers.publicKey')} rules={[{ required: true, message: t('validation.required') }]}><Input className="mono-input" placeholder="nodekey:…" /></Form.Item>
          <Button htmlType="submit" loading={submitting} block>{t('servers.addAllowed')}</Button>
        </Form>
      </Drawer>
    </div>
  )
}
