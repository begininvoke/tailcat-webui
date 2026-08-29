import { CodeOutlined, DeleteOutlined, ExperimentOutlined, PlusOutlined, RadarChartOutlined } from '@ant-design/icons'
import { App, Button, Card, Col, Descriptions, Divider, Drawer, Flex, Form, Grid, Input, Popconfirm, Row, Space, Switch, Tag, Typography } from 'antd'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router-dom'
import { PageHeader } from '../components/PageHeader'
import { ResourceState } from '../components/ResourceState'
import { RuntimeState } from '../components/RuntimeState'
import { useAsyncResource } from '../hooks/useAsyncResource'
import { api, type Client } from '../services/api'

interface ClientFormValues { name: string; server: string; derp_map_url?: string; save_identity: boolean }

export default function ClientsPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const screens = Grid.useBreakpoint()
  const [params, setParams] = useSearchParams()
  const [createOpen, setCreateOpen] = useState(params.get('new') === '1')
  const [toolsOpen, setToolsOpen] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [busyID, setBusyID] = useState('')
  const [toolToken, setToolToken] = useState('')
  const [toolResult, setToolResult] = useState('')
  const [tunnelClient, setTunnelClient] = useState<Client | null>(null)
  const [tunnelAddress, setTunnelAddress] = useState('server.tailcat:80')
  const [tunnelInput, setTunnelInput] = useState('')
  const [tunnelOutput, setTunnelOutput] = useState('')
  const [tunnelConnected, setTunnelConnected] = useState(false)
  const tunnelRef = useRef<WebSocket | null>(null)
  const [form] = Form.useForm<ClientFormValues>()
  const resource = useAsyncResource(api.clients)
  const closeCreate = () => {
    setCreateOpen(false)
    if (params.has('new')) { params.delete('new'); setParams(params, { replace: true }) }
  }
  const create = async (values: ClientFormValues) => {
    setSubmitting(true)
    try { await api.createClient(values); closeCreate(); form.resetFields(); await resource.refresh() }
    catch { void message.error(t('feedback.createFailed')) } finally { setSubmitting(false) }
  }
  const ping = async (client: Client) => {
    setBusyID(client.id)
    try { await api.pingClient(client.id); await resource.refresh() }
    catch { void message.error(t('clients.pingFailed')) } finally { setBusyID('') }
  }
  const remove = async (id: string) => {
    setBusyID(id)
    try { await api.deleteClient(id); await resource.refresh(); void message.success(t('feedback.deleted')) }
    catch { void message.error(t('feedback.deleteFailed')) } finally { setBusyID('') }
  }
  const runTool = async (mode: 'parse' | 'resolve') => {
    setSubmitting(true); setToolResult('')
    try {
      const result = mode === 'parse' ? await api.parseToken(toolToken) : await api.resolveToken(toolToken)
      setToolResult(JSON.stringify(result, null, 2))
    } catch { void message.error(t('clients.pingFailed')) } finally { setSubmitting(false) }
  }

  const disconnectTunnel = () => {
    tunnelRef.current?.close(1000)
    tunnelRef.current = null
    setTunnelConnected(false)
  }

  const connectTunnel = () => {
    if (!tunnelClient) return
    disconnectTunnel(); setTunnelOutput('')
    const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const socket = new WebSocket(`${scheme}//${location.host}/api/v1/clients/${tunnelClient.id}/tunnel?address=${encodeURIComponent(tunnelAddress)}`)
    socket.binaryType = 'arraybuffer'
    socket.onopen = () => { setTunnelConnected(true); void message.success(t('clients.connected')) }
    socket.onmessage = (event) => {
      const text = typeof event.data === 'string' ? event.data : new TextDecoder().decode(event.data as ArrayBuffer)
      setTunnelOutput((value) => value + text)
    }
    socket.onerror = () => void message.error(t('clients.pingFailed'))
    socket.onclose = () => { setTunnelConnected(false); tunnelRef.current = null }
    tunnelRef.current = socket
  }

  const sendTunnel = () => {
    if (!tunnelConnected || !tunnelInput) return
    tunnelRef.current?.send(new TextEncoder().encode(tunnelInput))
    setTunnelInput('')
  }

  return (
    <div className="page clients-page">
      <PageHeader title={t('clients.title')} subtitle={t('clients.subtitle')} actions={<Space><Button icon={<ExperimentOutlined />} onClick={() => setToolsOpen(true)}>{t('clients.tools')}</Button><Button type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>{t('clients.new')}</Button></Space>} />
      <ResourceState loading={resource.loading} error={resource.error} empty={(resource.data?.length ?? 0) === 0} emptyTitle={t('clients.empty')} emptyDescription={t('clients.emptyDescription')} emptyAction={<Button type="primary" onClick={() => setCreateOpen(true)}>{t('clients.new')}</Button>} retry={() => void resource.refresh()}>
        <Row gutter={[16, 16]}>{resource.data?.map((client) => (
          <Col xs={24} xl={12} key={client.id}>
            <Card className="resource-card" title={<Flex align="center" gap={10}><span>{client.name}</span><RuntimeState state={client.runtime_state} /></Flex>} extra={<Tag>{client.saved_key ? t('servers.saved') : t('servers.ephemeral')}</Tag>}>
              <Descriptions size="small" column={1} items={[
                { key: 'token', label: t('clients.tokenHint'), children: <code>{client.token_hint}</code> },
                { key: 'ping', label: t('clients.lastPing'), children: client.last_ping_ms != null ? `${client.last_ping_ms} ms` : t('common.never') },
                { key: 'path', label: t('clients.path'), children: client.last_path ? <Tag color={client.last_path === 'direct' ? 'success' : 'default'}>{client.last_path === 'direct' ? t('clients.direct') : client.last_path === 'peer-relay' ? t('clients.peerRelay') : t('clients.derp')}</Tag> : '—' },
                ...(client.public_key ? [{ key: 'public', label: t('servers.publicKey'), children: <Typography.Text className="mono-value" ellipsis>{client.public_key}</Typography.Text> }] : []),
              ]} />
              <Divider />
              <Flex gap={8} wrap="wrap">
                <Button type="primary" icon={<RadarChartOutlined />} loading={busyID === client.id} onClick={() => void ping(client)}>{t('common.ping')}</Button>
                <Button icon={<CodeOutlined />} onClick={() => setTunnelClient(client)}>{t('clients.tunnel')}</Button>
                <Popconfirm title={t('clients.deleteTitle')} description={t('clients.deleteDescription')} okText={t('common.delete')} cancelText={t('common.cancel')} okButtonProps={{ danger: true }} onConfirm={() => void remove(client.id)}><Button danger icon={<DeleteOutlined />}>{t('common.delete')}</Button></Popconfirm>
              </Flex>
            </Card>
          </Col>
        ))}</Row>
      </ResourceState>

      <Drawer title={t('clients.new')} width={500} placement={screens.md ? 'right' : 'bottom'} height={screens.md ? undefined : '88dvh'} open={createOpen} onClose={closeCreate} destroyOnHidden extra={<Button type="primary" loading={submitting} onClick={() => form.submit()}>{t('common.create')}</Button>}>
        <Form form={form} layout="vertical" initialValues={{ save_identity: false }} onFinish={(values) => void create(values)}>
          <Form.Item name="name" label={t('common.name')} rules={[{ required: true, message: t('validation.required') }]}><Input autoFocus maxLength={80} /></Form.Item>
          <Form.Item name="server" label={t('clients.server')} rules={[{ required: true, message: t('validation.required') }]}><Input.TextArea autoSize={{ minRows: 4, maxRows: 8 }} className="mono-input" /></Form.Item>
          <Form.Item name="derp_map_url" label={t('servers.customMap')}><Input type="url" placeholder="https://example.com/derpmap.json" /></Form.Item>
          <Form.Item name="save_identity" label={t('clients.saveIdentity')} tooltip={t('clients.saveIdentityHint')} valuePropName="checked"><Switch /></Form.Item>
        </Form>
      </Drawer>

      <Drawer title={t('clients.tools')} width={600} placement={screens.md ? 'right' : 'bottom'} height={screens.md ? undefined : '90dvh'} open={toolsOpen} onClose={() => setToolsOpen(false)} destroyOnHidden>
        <Form layout="vertical"><Form.Item label={t('clients.tokenInput')}><Input.TextArea value={toolToken} onChange={(event) => setToolToken(event.target.value)} autoSize={{ minRows: 5, maxRows: 10 }} className="mono-input" /></Form.Item></Form>
        <Space><Button type="primary" loading={submitting} onClick={() => void runTool('parse')}>{t('clients.parse')}</Button><Button loading={submitting} onClick={() => void runTool('resolve')}>{t('clients.resolve')}</Button></Space>
        {toolResult && <><Divider>{t('clients.result')}</Divider><pre className="code-result">{toolResult}</pre></>}
      </Drawer>

      <Drawer title={t('clients.tunnel')} width={640} placement={screens.md ? 'right' : 'bottom'} height={screens.md ? undefined : '92dvh'} open={Boolean(tunnelClient)} onClose={() => { disconnectTunnel(); setTunnelClient(null) }} destroyOnHidden>
        <Space.Compact block>
          <Input value={tunnelAddress} onChange={(event) => setTunnelAddress(event.target.value)} aria-label={t('clients.tunnelAddress')} className="mono-input" />
          {tunnelConnected ? <Button danger onClick={disconnectTunnel}>{t('clients.disconnect')}</Button> : <Button type="primary" onClick={connectTunnel}>{t('clients.connect')}</Button>}
        </Space.Compact>
        <Divider>{t('clients.output')}</Divider>
        <pre className="code-result tunnel-output" aria-live="polite">{tunnelOutput || '—'}</pre>
        <Divider>{t('clients.input')}</Divider>
        <Input.TextArea value={tunnelInput} onChange={(event) => setTunnelInput(event.target.value)} autoSize={{ minRows: 3, maxRows: 8 }} className="mono-input" />
        <Button className="tunnel-send" type="primary" icon={<CodeOutlined />} disabled={!tunnelConnected || !tunnelInput} onClick={sendTunnel}>{t('clients.send')}</Button>
      </Drawer>
    </div>
  )
}
