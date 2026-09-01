import { CodeOutlined, DeleteOutlined, ExperimentOutlined, PlusOutlined, RadarChartOutlined } from '@ant-design/icons'
import { Alert, App, Button, Card, Col, Descriptions, Divider, Drawer, Empty, Flex, Form, Grid, Input, InputNumber, List, Popconfirm, Radio, Row, Space, Switch, Table, Tabs, Tag, Tooltip, Typography, type TableProps } from 'antd'
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router-dom'
import { PageHeader } from '../components/PageHeader'
import { DiagnosticPathTag, DiagnosticStatusTag, OperationProgress } from '../components/OperationProgress'
import { ResourceState } from '../components/ResourceState'
import { RuntimeState } from '../components/RuntimeState'
import { useAsyncResource } from '../hooks/useAsyncResource'
import { useDiagnosticEvents, type DiagnosticEventPayload, type DiagnosticRuntimeEvent } from '../hooks/useRuntimeEvents'
import { APIError, api, type Client, type DiagnosticRun, type StartDiagnosticInput } from '../services/api'

interface ClientFormValues { name: string; server: string; derp_map_url?: string; save_identity: boolean }
interface DiagnosticFormValues { client_id: string; kind: 'ping' | 'throughput'; duration_ms: number; bytes: number }

const diagnosticRefreshDelayMS = 100
const diagnosticLiveUpdateLimit = 100

function integerInRange(minimum: number, maximum: number, message: string) {
  return (_: unknown, value: unknown) => typeof value === 'number' && Number.isInteger(value) && value >= minimum && value <= maximum ? Promise.resolve() : Promise.reject(new Error(message))
}

function formatDate(value: string | undefined, locale: string) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'medium' }).format(date)
}

function patchRun(run: DiagnosticRun, payload: DiagnosticEventPayload): DiagnosticRun {
  return {
    ...run,
    status: payload.status,
    latency_ms: payload.latency_ms ?? run.latency_ms,
    upload_bytes: payload.upload_bytes ?? run.upload_bytes,
    download_bytes: payload.download_bytes ?? run.download_bytes,
    upload_bps: payload.upload_bps ?? run.upload_bps,
    download_bps: payload.download_bps ?? run.download_bps,
    error_code: payload.error_code ?? run.error_code,
  }
}

export function reduceDiagnosticLiveUpdates(current: Record<string, DiagnosticEventPayload>, event: DiagnosticRuntimeEvent) {
  const next = { ...current }
  delete next[event.resource_id]
  next[event.resource_id] = event.payload
  const keys = Object.keys(next)
  for (let index = 0; index < keys.length - diagnosticLiveUpdateLimit; index += 1) delete next[keys[index]!]
  return next
}

export function pruneAuthoritativeDiagnosticUpdates(current: Record<string, DiagnosticEventPayload>, runs: DiagnosticRun[] | null) {
  const terminalIDs = new Set(runs?.filter((run) => run.status !== 'running').map((run) => run.id) ?? [])
  if (terminalIDs.size === 0) return current
  let next: Record<string, DiagnosticEventPayload> | undefined
  for (const id of terminalIDs) {
    if (current[id]?.status === 'running' || current[id] === undefined) continue
    next ??= { ...current }
    delete next[id]
  }
  return next ?? current
}

function rememberBounded<T>(items: Map<string, T>, id: string, value: T) {
  items.delete(id)
  items.set(id, value)
  while (items.size > diagnosticLiveUpdateLimit) items.delete(items.keys().next().value!)
}

export default function ClientsPage() {
  const { t, i18n } = useTranslation()
  const { message } = App.useApp()
  const screens = Grid.useBreakpoint()
  const [params, setParams] = useSearchParams()
  const [createOpen, setCreateOpen] = useState(params.get('new') === '1')
  const [activeTab, setActiveTab] = useState(params.get('tab') === 'diagnostics' ? 'diagnostics' : 'clients')
  const [diagnosticOpen, setDiagnosticOpen] = useState(false)
  const [diagnosticSubmitting, setDiagnosticSubmitting] = useState(false)
  const [diagnosticBusyID, setDiagnosticBusyID] = useState('')
  const [diagnosticError, setDiagnosticError] = useState('')
  const [liveUpdates, setLiveUpdates] = useState<Record<string, DiagnosticEventPayload>>({})
  const diagnosticSequences = useRef(new Map<string, number>())
  const pendingDiagnosticTerminals = useRef(new Map<string, DiagnosticRuntimeEvent>())
  const diagnosticRefreshTimer = useRef<number | null>(null)
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
  const [diagnosticForm] = Form.useForm<DiagnosticFormValues>()
  const resource = useAsyncResource(api.clients)
  const diagnostics = useAsyncResource(api.diagnostics, { refreshOnRuntime: false })
  const setDiagnosticsData = diagnostics.setData
  const diagnosticsRefresh = useRef(diagnostics.refresh)
  useEffect(() => { diagnosticsRefresh.current = diagnostics.refresh }, [diagnostics.refresh])
  useEffect(() => {
    const authoritativeTerminalIDs = diagnostics.data?.filter((run) => run.status !== 'running').map((run) => run.id) ?? []
    if (authoritativeTerminalIDs.length === 0) return
    for (const id of authoritativeTerminalIDs) pendingDiagnosticTerminals.current.delete(id)
    const timer = window.setTimeout(() => setLiveUpdates((current) => pruneAuthoritativeDiagnosticUpdates(current, diagnostics.data)), 0)
    return () => window.clearTimeout(timer)
  }, [diagnostics.data])
  useEffect(() => () => {
    if (diagnosticRefreshTimer.current !== null) window.clearTimeout(diagnosticRefreshTimer.current)
  }, [])
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

  const queueDiagnosticRefresh = useCallback(() => {
    if (diagnosticRefreshTimer.current !== null) return
    diagnosticRefreshTimer.current = window.setTimeout(() => {
      diagnosticRefreshTimer.current = null
      diagnosticsRefresh.current({ silent: true })
    }, diagnosticRefreshDelayMS)
  }, [])
  const onDiagnostic = useCallback((event: DiagnosticRuntimeEvent) => {
    const previousSequence = diagnosticSequences.current.get(event.resource_id)
    if (previousSequence !== undefined && event.sequence <= previousSequence) return
    rememberBounded(diagnosticSequences.current, event.resource_id, event.sequence)
    if (event.payload.status !== 'running') rememberBounded(pendingDiagnosticTerminals.current, event.resource_id, event)
    setLiveUpdates((current) => reduceDiagnosticLiveUpdates(current, event))
    if (event.payload.status === 'running') setDiagnosticsData((current) => current?.map((run) => run.id === event.resource_id && run.client_id === event.payload.client_id ? patchRun(run, event.payload) : run) ?? current)
    queueDiagnosticRefresh()
  }, [queueDiagnosticRefresh, setDiagnosticsData])
  useDiagnosticEvents(onDiagnostic)

  const startDiagnostic = async (values: DiagnosticFormValues) => {
    const input: StartDiagnosticInput = values.kind === 'ping'
      ? { kind: 'ping', duration_ms: values.duration_ms, bytes: 0 }
      : { kind: 'throughput', duration_ms: values.duration_ms, bytes: values.bytes }
    setDiagnosticSubmitting(true); setDiagnosticError('')
    try {
      const run = await api.startDiagnostic(values.client_id, input)
      diagnostics.setData((current) => {
        if (pendingDiagnosticTerminals.current.has(run.id) || current?.some((item) => item.id === run.id && item.status !== 'running')) return current
        if (current?.some((item) => item.id === run.id)) return current
        return [run, ...(current ?? [])]
      })
      setDiagnosticOpen(false); diagnosticForm.resetFields()
    } catch (error) {
      const code = error instanceof APIError ? error.code : 'REQUEST_FAILED'
      setDiagnosticError(t(`diagnostics.errors.${code}`, { defaultValue: t('diagnostics.startFailed') }))
    } finally { setDiagnosticSubmitting(false) }
  }

  const cancelDiagnostic = async (id: string) => {
    setDiagnosticBusyID(id)
    try {
      await api.cancelDiagnostic(id)
      void message.success(t('diagnostics.canceledSuccess'))
    } catch (error) {
      const code = error instanceof APIError ? error.code : 'REQUEST_FAILED'
      void message.error(t(`diagnostics.errors.${code}`, { defaultValue: t('diagnostics.cancelFailed') }))
    } finally { setDiagnosticBusyID('') }
  }

  const locale = i18n.resolvedLanguage === 'zh-CN' ? 'zh-CN' : 'en-US'
  const clientNames = new Map(resource.data?.map((client) => [client.id, client.name]))
  const diagnosticRuns = diagnostics.data?.map((run) => {
    const update = liveUpdates[run.id]
    return update?.client_id === run.client_id ? patchRun(run, update) : run
  }) ?? []
  const clientLabel = (clientID: string) => {
    const name = clientNames.get(clientID)
    return name ? <Typography.Text>{name}</Typography.Text> : <Tooltip title={clientID}><Typography.Text className="mono-value diagnostic-client-id" ellipsis tabIndex={0} aria-label={`${t('diagnostics.client')}: ${clientID}`}>{clientID}</Typography.Text></Tooltip>
  }
  const diagnosticColumns: TableProps<DiagnosticRun>['columns'] = [
    { title: t('diagnostics.client'), dataIndex: 'client_id', render: clientLabel },
    { title: t('diagnostics.kind'), dataIndex: 'kind', render: (kind: DiagnosticRun['kind']) => t(`diagnostics.${kind}`) },
    { title: t('common.status'), dataIndex: 'status', render: (status: DiagnosticRun['status']) => <DiagnosticStatusTag status={status} /> },
    { title: t('diagnostics.path'), dataIndex: 'path', render: (path: DiagnosticRun['path']) => <DiagnosticPathTag path={path} /> },
    { title: t('diagnostics.started'), dataIndex: 'started_at', render: (value: string) => <span className="tabular-figure">{formatDate(value, locale)}</span> },
    { title: t('diagnostics.finished'), dataIndex: 'finished_at', responsive: ['lg'], render: (value: string | undefined) => <span className="tabular-figure">{formatDate(value, locale)}</span> },
    { title: t('diagnostics.metrics'), key: 'metrics', render: (_: unknown, run) => <OperationProgress run={run} progress={liveUpdates[run.id]?.progress} compact /> },
    { title: t('common.actions'), key: 'actions', align: 'right', render: (_: unknown, run) => run.status === 'running' ? <Button danger loading={diagnosticBusyID === run.id} onClick={() => void cancelDiagnostic(run.id)}>{t('diagnostics.cancel')}</Button> : null },
  ]

  const changeTab = (key: string) => {
    setActiveTab(key)
    if (key === 'diagnostics') params.set('tab', key); else params.delete('tab')
    setParams(params, { replace: true })
  }

  return (
    <div className="page clients-page">
      <PageHeader title={activeTab === 'clients' ? t('clients.title') : t('diagnostics.title')} subtitle={activeTab === 'clients' ? t('clients.subtitle') : t('diagnostics.subtitle')} actions={activeTab === 'clients' ? <Space><Button icon={<ExperimentOutlined />} onClick={() => setToolsOpen(true)}>{t('clients.tools')}</Button><Button type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>{t('clients.new')}</Button></Space> : <Button type="primary" icon={<PlusOutlined aria-hidden />} disabled={(resource.data?.length ?? 0) === 0} onClick={() => setDiagnosticOpen(true)}>{t('diagnostics.start')}</Button>} />
      <Tabs className="clients-tabs" activeKey={activeTab} onChange={changeTab} items={[
        {
          key: 'clients', label: t('nav.clients'), children: <ResourceState loading={resource.loading} error={resource.error} empty={(resource.data?.length ?? 0) === 0} emptyTitle={t('clients.empty')} emptyDescription={t('clients.emptyDescription')} emptyAction={<Button type="primary" onClick={() => setCreateOpen(true)}>{t('clients.new')}</Button>} retry={() => void resource.refresh()}>
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
          </ResourceState>,
        },
        {
          key: 'diagnostics', label: t('diagnostics.tab'), children: <ResourceState loading={diagnostics.loading} error={diagnostics.error} empty={diagnosticRuns.length === 0} emptyTitle={t('diagnostics.empty')} emptyDescription={t('diagnostics.emptyDescription')} emptyAction={(resource.data?.length ?? 0) > 0 ? <Button type="primary" onClick={() => setDiagnosticOpen(true)}>{t('diagnostics.start')}</Button> : undefined} retry={() => void diagnostics.refresh()}>
            {screens.md ? <Table<DiagnosticRun> className="diagnostics-table" rowKey="id" size="small" pagination={false} dataSource={diagnosticRuns} columns={diagnosticColumns} /> : <List className="diagnostics-list" dataSource={diagnosticRuns} renderItem={(run) => (
              <List.Item className="diagnostic-card" actions={run.status === 'running' ? [<Button key="cancel" danger loading={diagnosticBusyID === run.id} onClick={() => void cancelDiagnostic(run.id)}>{t('diagnostics.cancel')}</Button>] : []}>
                <List.Item.Meta description={<Flex vertical gap={8}><Flex gap={8} wrap="wrap" align="center">{clientLabel(run.client_id)}<Typography.Text type="secondary">{t(`diagnostics.${run.kind}`)}</Typography.Text></Flex><OperationProgress run={run} progress={liveUpdates[run.id]?.progress} /><Typography.Text className="tabular-figure" type="secondary">{t('diagnostics.started')}: {formatDate(run.started_at, locale)} · {t('diagnostics.finished')}: {formatDate(run.finished_at, locale)}</Typography.Text></Flex>} />
              </List.Item>
            )} locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} /> }} />}
          </ResourceState>,
        },
      ]} />

      <Drawer title={t('clients.new')} width={500} placement={screens.md ? 'right' : 'bottom'} height={screens.md ? undefined : '88dvh'} open={createOpen} onClose={closeCreate} destroyOnHidden extra={<Button type="primary" loading={submitting} onClick={() => form.submit()}>{t('common.create')}</Button>}>
        <Form form={form} layout="vertical" initialValues={{ save_identity: false }} onFinish={(values) => void create(values)}>
          <Form.Item name="name" label={t('common.name')} rules={[{ required: true, message: t('validation.required') }]}><Input autoFocus maxLength={80} /></Form.Item>
          <Form.Item name="server" label={t('clients.server')} rules={[{ required: true, message: t('validation.required') }]}><Input.TextArea autoSize={{ minRows: 4, maxRows: 8 }} className="mono-input" /></Form.Item>
          <Form.Item name="derp_map_url" label={t('servers.customMap')}><Input type="url" placeholder="https://example.com/derpmap.json" /></Form.Item>
          <Form.Item name="save_identity" label={t('clients.saveIdentity')} tooltip={t('clients.saveIdentityHint')} valuePropName="checked"><Switch /></Form.Item>
        </Form>
      </Drawer>

      <Drawer title={t('diagnostics.start')} width={500} placement={screens.md ? 'right' : 'bottom'} height={screens.md ? undefined : '88dvh'} open={diagnosticOpen} onClose={() => { setDiagnosticOpen(false); setDiagnosticError('') }} destroyOnHidden extra={<Button type="primary" loading={diagnosticSubmitting} disabled={diagnosticSubmitting} onClick={() => diagnosticForm.submit()}>{t('common.start')}</Button>}>
        <Typography.Paragraph type="secondary">{t('diagnostics.startHint')}</Typography.Paragraph>
        {diagnosticError && <Alert className="drawer-alert" type="error" showIcon message={diagnosticError} action={<Button size="small" onClick={() => diagnosticForm.submit()}>{t('common.retry')}</Button>} />}
        <Form form={diagnosticForm} layout="vertical" initialValues={{ client_id: resource.data?.[0]?.id, kind: 'ping', duration_ms: 1000, bytes: 1048576 }} onFinish={(values) => void startDiagnostic(values)} onValuesChange={(changed: Partial<DiagnosticFormValues>) => { if (changed.kind === 'ping') diagnosticForm.setFieldValue('bytes', 0); if (changed.kind === 'throughput' && diagnosticForm.getFieldValue('bytes') === 0) diagnosticForm.setFieldValue('bytes', 1048576) }}>
          <Form.Item name="client_id" label={t('diagnostics.client')} rules={[{ required: true, message: t('validation.required') }]}><Radio.Group className="diagnostic-client-select" options={resource.data?.map((client) => ({ value: client.id, label: client.name })) ?? []} /></Form.Item>
          <Form.Item name="kind" label={t('diagnostics.kind')} rules={[{ required: true, message: t('validation.required') }]}><Radio.Group className="diagnostic-kind-select" optionType="button" buttonStyle="solid" options={[{ value: 'ping', label: t('diagnostics.ping') }, { value: 'throughput', label: t('diagnostics.throughput') }]} /></Form.Item>
          <Form.Item name="duration_ms" label={t('diagnostics.duration')} extra={t('diagnostics.durationHelp')} rules={[{ required: true, message: t('validation.required') }, { validator: integerInRange(1, 5000, t('diagnostics.durationInvalid')) }]}><InputNumber step={1} className="full-width" /></Form.Item>
          <Form.Item noStyle shouldUpdate={(previous, current) => previous.kind !== current.kind}>{({ getFieldValue }) => getFieldValue('kind') === 'throughput' ? <Form.Item name="bytes" label={t('diagnostics.bytes')} extra={t('diagnostics.bytesHelp')} rules={[{ required: true, message: t('validation.required') }, { validator: integerInRange(1, 33554432, t('diagnostics.bytesInvalid')) }]}><InputNumber step={1} className="full-width" /></Form.Item> : null}</Form.Item>
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
