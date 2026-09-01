import { CloudDownloadOutlined, DeleteOutlined, FileOutlined, GlobalOutlined, InboxOutlined, LockOutlined, PlusOutlined, SafetyCertificateOutlined, SendOutlined, SyncOutlined } from '@ant-design/icons'
import { Alert, App, Button, Card, Checkbox, Col, Descriptions, Divider, Drawer, Empty, Flex, Form, Grid, Input, InputNumber, List, Popconfirm, Radio, Result, Row, Select, Space, Table, Tabs, Tag, Typography, Upload, type TableProps, type UploadFile, type UploadProps } from 'antd'
import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useAuth } from '../app/auth'
import { CopyButton } from '../components/CopyButton'
import { PageHeader } from '../components/PageHeader'
import { ResourceState } from '../components/ResourceState'
import { formatTransferBytes, TransferProgress } from '../components/TransferProgress'
import { useAsyncResource } from '../hooks/useAsyncResource'
import { useTransferEvents, type TransferRuntimeEvent } from '../hooks/useRuntimeEvents'
import { APIError, api, type Client, type PublicTransferConfig, type TransferEventPayload, type TransferItem, type TransferJob, type TransferShare, type TransferShareFile, type TransferStatus } from '../services/api'

interface RouteFormValues { client_id: string; name: string; slug: string; remote_port: number; base_path: string; access: 'private' | 'public'; allowed_methods: string[] }
interface ReceiveFormValues { client_id: string; capability: string }
export type QueueStatus = 'queued' | 'uploading' | 'succeeded' | 'failed'
export interface QueuedFile { uid: string; file: File; virtualPath: string; status: QueueStatus; error?: string }
export interface TransferQueueState { items: QueuedFile[]; operation?: { id: number; uids: readonly string[] } }
type QueueAction = { type: 'add'; files: QueuedFile[] } | { type: 'remove'; uid: string } | { type: 'status'; operationID: number; uid: string; status: QueueStatus; error?: string } | { type: 'reset' } | { type: 'begin'; operationID: number; uids: readonly string[] } | { type: 'finish'; operationID: number; clearSucceeded: boolean }
type Route = Awaited<ReturnType<typeof api.routes>>[number]
export interface OneTimeCode { shareID: string; value: string; generation: number }
export interface TerminalFileSummary { completed: number; total: number }

const transferRefreshDelayMS = 100
const terminalTransferStatuses = new Set<TransferStatus>(['completed', 'failed', 'canceled', 'interrupted', 'expired', 'deleting'])
const terminalEventStatuses = new Set<TransferEventPayload['status']>(['completed', 'failed', 'canceled', 'interrupted', 'expired', 'deleting', 'deleted'])
const restartableTransferStatuses = new Set<TransferEventPayload['status']>(['failed', 'canceled', 'interrupted'])
const maxTerminalSummaries = 100

export const initialTransferQueueState: TransferQueueState = { items: [] }

export function transferQueueReducer(state: TransferQueueState, action: QueueAction): TransferQueueState {
  if (action.type === 'begin') return state.operation ? state : { ...state, operation: { id: action.operationID, uids: [...action.uids] } }
  if (action.type === 'finish') {
    if (state.operation?.id !== action.operationID) return state
    const snapshotUIDs = new Set(state.operation.uids)
    return { items: action.clearSucceeded ? state.items.filter((item) => !snapshotUIDs.has(item.uid) || item.status !== 'succeeded') : state.items }
  }
  if (action.type === 'status') {
    if (state.operation?.id !== action.operationID || !state.operation.uids.includes(action.uid)) return state
    return { ...state, items: state.items.map((item) => item.uid === action.uid ? { ...item, status: action.status, ...(action.error ? { error: action.error } : { error: undefined }) } : item) }
  }
  if (state.operation) return state
  if (action.type === 'reset') return initialTransferQueueState
  if (action.type === 'remove') return { ...state, items: state.items.filter((item) => item.uid !== action.uid) }
  const known = new Set(state.items.map((item) => item.uid))
  const additions: QueuedFile[] = []
  for (const item of action.files) if (!known.has(item.uid)) { known.add(item.uid); additions.push(item) }
  return { ...state, items: [...state.items, ...additions] }
}

export function compareAndClearOneTimeCode(current: OneTimeCode | null, expected: OneTimeCode) {
  return current?.generation === expected.generation && current.shareID === expected.shareID && current.value === expected.value ? null : current
}

export function pruneTerminalSummaries(summaries: Record<string, TerminalFileSummary>, currentJobs: readonly TransferJob[], maximum = maxTerminalSummaries) {
  const retained = new Set(currentJobs.slice(0, maximum).map((job) => job.id))
  return Object.fromEntries(Object.entries(summaries).filter(([id]) => retained.has(id)))
}

function formatDate(value: string | undefined, locale: string) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

function patchShare(share: TransferShare, payload: TransferEventPayload): TransferShare {
  if (payload.status === 'deleted') return share
  return { ...share, status: payload.status, total_bytes: payload.total_bytes ?? share.total_bytes, file_count: payload.total_files ?? share.file_count }
}

function patchJob(job: TransferJob, payload: TransferEventPayload): TransferJob {
  if (payload.status === 'deleted') return job
  return { ...job, status: payload.status, received_bytes: payload.received_bytes ?? job.received_bytes, total_bytes: payload.total_bytes ?? job.total_bytes, error_code: payload.error_code ?? job.error_code }
}

function TransferLimits({ limits }: { limits: PublicTransferConfig }) {
  const { t, i18n } = useTranslation()
  const locale = i18n.resolvedLanguage === 'zh-CN' ? 'zh-CN' : 'en-US'
  return <Card size="small" className="transfer-limits" title={t('transfers.limits')}>
    <Descriptions size="small" column={{ xs: 1, sm: 2, lg: 5 }} items={[
      { key: 'file', label: t('transfers.maxFile'), children: <span className="tabular-figure">{formatTransferBytes(limits.max_file_bytes, locale)}</span> },
      { key: 'share', label: t('transfers.maxShare'), children: <span className="tabular-figure">{formatTransferBytes(Math.min(limits.max_share_bytes, limits.max_job_bytes), locale)}</span> },
      { key: 'owner', label: t('transfers.maxOwner'), children: <span className="tabular-figure">{formatTransferBytes(limits.max_owner_bytes, locale)}</span> },
      { key: 'files', label: t('transfers.maxFiles'), children: <span className="tabular-figure">{new Intl.NumberFormat(locale).format(limits.max_files_per_share)}</span> },
      { key: 'expiry', label: t('transfers.expiry'), children: <span className="tabular-figure">{t('transfers.hours', { count: Math.round(limits.expiry_seconds / 3600) })}</span> },
    ]} />
  </Card>
}

function RoutePanel({ routes, clients, routesLoading, routesError, clientsLoading, clientsError, refreshRoutes, refreshClients, openCreate, remove, busyID }: { routes: Route[]; clients: Client[]; routesLoading: boolean; routesError: unknown; clientsLoading: boolean; clientsError: unknown; refreshRoutes: () => void; refreshClients: () => void; openCreate: () => void; remove: (id: string) => void; busyID: string }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  return <>
    {clientsError && <Alert className="inline-callout" type="warning" showIcon message={t('routes.clientsLoadFailed')} action={<Button onClick={refreshClients}>{t('common.retry')}</Button>} />}
    {!clientsError && clients.length === 0 && !clientsLoading && <Card className="inline-callout"><Flex align="center" justify="space-between" gap={12} wrap="wrap"><Space><SafetyCertificateOutlined /><Typography.Text>{t('routes.noClients')}</Typography.Text></Space><Button onClick={() => navigate('/clients?new=1')}>{t('clients.new')}</Button></Flex></Card>}
    <ResourceState loading={routesLoading} error={routesError} empty={routes.length === 0} emptyTitle={t('routes.empty')} emptyDescription={t('routes.emptyDescription')} emptyAction={clients.length > 0 ? <Button type="primary" onClick={openCreate}>{t('routes.new')}</Button> : undefined} retry={refreshRoutes}>
      <Row gutter={[16, 16]}>{routes.map((route) => <Col xs={24} xl={12} key={route.id}>
        <Card className="resource-card" title={<Flex align="center" gap={10}><GlobalOutlined /><span>{route.name}</span></Flex>} extra={<Tag icon={route.access === 'private' ? <LockOutlined /> : <GlobalOutlined />} color={route.access === 'public' ? 'warning' : undefined}>{route.access === 'public' ? t('routes.public') : t('routes.private')}</Tag>}>
          <Descriptions size="small" column={1} items={[
            { key: 'url', label: t('routes.preview'), children: <Space className="token-row"><Typography.Link href={route.url} target="_blank" rel="noreferrer" ellipsis>{route.url}</Typography.Link><CopyButton value={route.url} /></Space> },
            { key: 'port', label: t('routes.remotePort'), children: <code>{route.remote_port}</code> },
            { key: 'base', label: t('routes.basePath'), children: <code>{route.base_path}</code> },
            { key: 'methods', label: t('routes.methods'), children: <Space size={[4, 4]} wrap>{route.allowed_methods.map((method) => <Tag key={method}>{method}</Tag>)}</Space> },
          ]} />
          <Divider />
          <Flex gap={8} wrap="wrap"><Button type="primary" href={route.url} target="_blank" rel="noreferrer">{t('common.open')}</Button><Popconfirm title={t('routes.deleteTitle')} description={t('routes.deleteDescription')} okText={t('common.delete')} cancelText={t('common.cancel')} okButtonProps={{ danger: true }} onConfirm={() => remove(route.id)}><Button danger loading={busyID === route.id} icon={<DeleteOutlined />}>{t('common.delete')}</Button></Popconfirm></Flex>
        </Card>
      </Col>)}</Row>
    </ResourceState>
  </>
}

export default function RoutesPage() {
  const { t, i18n } = useTranslation()
  const { message } = App.useApp()
  const { config } = useAuth()
  const screens = Grid.useBreakpoint()
  const [params, setParams] = useSearchParams()
  const [activeTab, setActiveTab] = useState(params.get('tab') === 'transfers' ? 'transfers' : 'routes')
  const [createOpen, setCreateOpen] = useState(params.get('new') === '1')
  const [submitting, setSubmitting] = useState(false)
  const [busyID, setBusyID] = useState('')
  const [senderOpen, setSenderOpen] = useState(false)
  const [senderServerID, setSenderServerID] = useState('')
  const [senderBusy, setSenderBusy] = useState(false)
  const [senderError, setSenderError] = useState('')
  const [selectionError, setSelectionError] = useState('')
  const [activeShareID, setActiveShareID] = useState('')
  const [existingFiles, setExistingFiles] = useState<TransferShareFile[]>([])
  const [resumeLoadingID, setResumeLoadingID] = useState('')
  const [resumeHistoryError, setResumeHistoryError] = useState<{ share: TransferShare; message: string } | null>(null)
  const resumeRequestGeneration = useRef(0)
  const capabilityGeneration = useRef(0)
  const [oneTimeCode, setOneTimeCode] = useState<OneTimeCode | null>(null)
  const [queueState, dispatchQueue] = useReducer(transferQueueReducer, initialTransferQueueState)
  const queue = queueState.items
  const queueRef = useRef(queueState)
  const senderOperationRef = useRef<{ id: number; snapshot: readonly QueuedFile[] } | null>(null)
  const senderOperationCounter = useRef(0)
  useEffect(() => { queueRef.current = queueState }, [queueState])
  const [receiveError, setReceiveError] = useState('')
  const [receiveBusy, setReceiveBusy] = useState(false)
  const [jobBusyID, setJobBusyID] = useState('')
  const [selectedJobID, setSelectedJobID] = useState('')
  const selectedJobIDRef = useRef('')
  const [detailsItems, setDetailsItems] = useState<TransferItem[]>([])
  const [detailsItemsLoaded, setDetailsItemsLoaded] = useState(false)
  const [detailsLoading, setDetailsLoading] = useState(false)
  const [detailsError, setDetailsError] = useState('')
  const detailsRequestID = useRef(0)
  const [liveProgress, setLiveProgress] = useState<Record<string, TransferEventPayload>>({})
  const liveProgressRef = useRef<Record<string, TransferEventPayload>>({})
  const [liveAnnouncement, setLiveAnnouncement] = useState('')
  const [terminalFileSummaries, setTerminalFileSummaries] = useState<Record<string, TerminalFileSummary>>({})
  const terminalFileSummariesRef = useRef<Record<string, TerminalFileSummary>>({})
  const terminalSummaryAttempts = useRef(new Set<string>())
  const terminalSummaryRequests = useRef(new Map<string, number>())
  const terminalSummaryGeneration = useRef(0)
  const jobLifecycleGenerations = useRef(new Map<string, number>())
  const sharesDataRef = useRef<TransferShare[] | null>(null)
  const jobsDataRef = useRef<TransferJob[] | null>(null)
  const transferSequences = useRef(new Map<string, number>())
  const transferRefreshTimer = useRef<number | null>(null)
  const transferRefreshKinds = useRef({ shares: false, jobs: false, items: false, showItemsLoading: false })
  const durableCleanup = useRef({ shares: false, jobs: false })
  const [form] = Form.useForm<RouteFormValues>()
  const [receiveForm] = Form.useForm<ReceiveFormValues>()

  const routes = useAsyncResource(api.routes)
  const clientResource = useAsyncResource(api.clients)
  const serverResource = useAsyncResource(api.servers)
  const shares = useAsyncResource(api.transferShares, { refreshOnRuntime: false })
  const jobs = useAsyncResource(api.transferJobs, { refreshOnRuntime: false })
  const sharesRefresh = useRef(shares.refresh)
  const jobsRefresh = useRef(jobs.refresh)
  useEffect(() => { sharesDataRef.current = shares.data }, [shares.data])
  useEffect(() => { jobsDataRef.current = jobs.data }, [jobs.data])
  useEffect(() => { sharesRefresh.current = shares.refresh }, [shares.refresh])
  useEffect(() => { jobsRefresh.current = jobs.refresh }, [jobs.refresh])
  const clients = useMemo(() => clientResource.data ?? [], [clientResource.data])
  const servers = useMemo(() => serverResource.data ?? [], [serverResource.data])
  const selectedSenderServerID = senderServerID || servers[0]?.id || ''
  const commitTerminalSummary = useCallback((jobID: string, summary: TerminalFileSummary) => {
    const expanded = { ...terminalFileSummariesRef.current, [jobID]: summary }
    const next = jobsDataRef.current ? pruneTerminalSummaries(expanded, jobsDataRef.current) : expanded
    terminalFileSummariesRef.current = next
    terminalSummaryAttempts.current.add(jobID)
    setTerminalFileSummaries(next)
  }, [])
  const clearTerminalLifecycle = useCallback((jobID: string, clearLive: boolean, forceLive = false) => {
    jobLifecycleGenerations.current.set(jobID, (jobLifecycleGenerations.current.get(jobID) ?? 0) + 1)
    terminalSummaryGeneration.current += 1
    terminalSummaryRequests.current.delete(jobID)
    terminalSummaryAttempts.current.delete(jobID)
    if (terminalFileSummariesRef.current[jobID]) {
      const next = { ...terminalFileSummariesRef.current }
      delete next[jobID]
      terminalFileSummariesRef.current = next
      setTerminalFileSummaries(next)
    }
    if (selectedJobIDRef.current === jobID) {
      detailsRequestID.current += 1
      setDetailsItems([])
      setDetailsItemsLoaded(false)
      setDetailsError('')
      setDetailsLoading(false)
    }
    if (clearLive && liveProgressRef.current[jobID] && (forceLive || terminalEventStatuses.has(liveProgressRef.current[jobID].status))) {
      const next = { ...liveProgressRef.current }
      delete next[jobID]
      liveProgressRef.current = next
      setLiveProgress(next)
    }
  }, [])
  useEffect(() => () => {
    resumeRequestGeneration.current += 1
    if (transferRefreshTimer.current !== null) window.clearTimeout(transferRefreshTimer.current)
    detailsRequestID.current += 1
    terminalSummaryGeneration.current += 1
    terminalSummaryRequests.current.clear()
  }, [])

  useEffect(() => {
    const currentJobs = jobs.data
    if (!currentJobs) return
    const retainedIDs = new Set(currentJobs.slice(0, maxTerminalSummaries).map((job) => job.id))
    for (const id of terminalSummaryAttempts.current) if (!retainedIDs.has(id)) terminalSummaryAttempts.current.delete(id)
    for (const id of terminalSummaryRequests.current.keys()) if (!retainedIDs.has(id)) terminalSummaryRequests.current.delete(id)
    for (const id of jobLifecycleGenerations.current.keys()) if (!retainedIDs.has(id)) jobLifecycleGenerations.current.delete(id)
    const pruned = pruneTerminalSummaries(terminalFileSummariesRef.current, currentJobs)
    if (Object.keys(pruned).length !== Object.keys(terminalFileSummariesRef.current).length) {
      terminalFileSummariesRef.current = pruned
      Promise.resolve().then(() => { if (jobsDataRef.current === currentJobs) setTerminalFileSummaries(pruned) })
    }
    for (const job of currentJobs.slice(0, maxTerminalSummaries)) {
      if (job.status !== 'completed' || terminalFileSummariesRef.current[job.id] || terminalSummaryAttempts.current.has(job.id)) continue
      terminalSummaryAttempts.current.add(job.id)
      const generation = ++terminalSummaryGeneration.current
      terminalSummaryRequests.current.set(job.id, generation)
      void api.transferJobItems(job.id).then((items) => {
        const latest = jobsDataRef.current?.find((candidate) => candidate.id === job.id)
        if (terminalSummaryRequests.current.get(job.id) !== generation || latest?.status !== 'completed' || terminalFileSummariesRef.current[job.id]) return
        terminalSummaryRequests.current.delete(job.id)
        commitTerminalSummary(job.id, { completed: items.filter((item) => item.status === 'completed').length, total: items.length })
      }).catch(() => { if (terminalSummaryRequests.current.get(job.id) === generation) terminalSummaryRequests.current.delete(job.id) })
    }
  }, [commitTerminalSummary, jobs.data])

  useEffect(() => {
    if (!durableCleanup.current.shares || !shares.data) return
    durableCleanup.current.shares = false
    const retained = new Set(shares.data.filter((share) => !terminalTransferStatuses.has(share.status)).map((share) => share.id))
    for (const id of transferSequences.current.keys()) if (!retained.has(id) && !jobs.data?.some((job) => job.id === id)) transferSequences.current.delete(id)
    setLiveProgress((current) => {
      const next = Object.fromEntries(Object.entries(current).filter(([id]) => retained.has(id) || jobs.data?.some((job) => job.id === id)))
      liveProgressRef.current = next
      return next
    })
  }, [jobs.data, shares.data])
  useEffect(() => {
    if (!durableCleanup.current.jobs || !jobs.data) return
    durableCleanup.current.jobs = false
    const retained = new Set(jobs.data.filter((job) => !terminalTransferStatuses.has(job.status)).map((job) => job.id))
    for (const id of transferSequences.current.keys()) if (!retained.has(id) && !shares.data?.some((share) => share.id === id)) transferSequences.current.delete(id)
    setLiveProgress((current) => {
      const next = Object.fromEntries(Object.entries(current).filter(([id]) => retained.has(id) || shares.data?.some((share) => share.id === id)))
      liveProgressRef.current = next
      return next
    })
  }, [jobs.data, shares.data])

  const loadJobItems = useCallback(async (jobID: string, silent = false) => {
    if (selectedJobIDRef.current !== jobID) return
    const requestID = ++detailsRequestID.current
    const lifecycleGeneration = jobLifecycleGenerations.current.get(jobID) ?? 0
    const isCurrent = () => requestID === detailsRequestID.current
      && selectedJobIDRef.current === jobID
      && (jobLifecycleGenerations.current.get(jobID) ?? 0) === lifecycleGeneration
    if (!silent) setDetailsLoading(true)
    setDetailsError('')
    try {
      const items = await api.transferJobItems(jobID)
      if (isCurrent()) {
        setDetailsItems(items)
        setDetailsItemsLoaded(true)
        const status = liveProgressRef.current[jobID]?.status ?? jobsDataRef.current?.find((job) => job.id === jobID)?.status
        if (status && terminalEventStatuses.has(status)) commitTerminalSummary(jobID, { completed: items.filter((item) => item.status === 'completed').length, total: items.length })
      }
    } catch {
      if (isCurrent()) { setDetailsItemsLoaded(false); setDetailsError(t('transfers.loadItemsFailed')) }
    } finally {
      if (isCurrent()) setDetailsLoading(false)
    }
  }, [commitTerminalSummary, t])

  const queueTransferRefresh = useCallback((kind: 'shares' | 'jobs', refreshItems: boolean, showItemsLoading = false) => {
    transferRefreshKinds.current[kind] = true
    if (refreshItems) {
      transferRefreshKinds.current.items = true
      if (showItemsLoading) transferRefreshKinds.current.showItemsLoading = true
    }
    if (transferRefreshTimer.current !== null) return
    transferRefreshTimer.current = window.setTimeout(() => {
      transferRefreshTimer.current = null
      const pending = transferRefreshKinds.current
      transferRefreshKinds.current = { shares: false, jobs: false, items: false, showItemsLoading: false }
      if (pending.shares) { durableCleanup.current.shares = true; sharesRefresh.current({ silent: true }) }
      if (pending.jobs) { durableCleanup.current.jobs = true; jobsRefresh.current({ silent: true }) }
      if (pending.items && selectedJobIDRef.current) void loadJobItems(selectedJobIDRef.current, !pending.showItemsLoading)
    }, transferRefreshDelayMS)
  }, [loadJobItems])

  const onTransfer = useCallback((event: TransferRuntimeEvent) => {
    const previous = transferSequences.current.get(event.resource_id)
    if (previous !== undefined && event.sequence <= previous) return
    transferSequences.current.set(event.resource_id, event.sequence)
    const previousStatus = liveProgressRef.current[event.resource_id]?.status ?? (event.payload.share_id ? sharesDataRef.current?.find((share) => share.id === event.resource_id)?.status : jobsDataRef.current?.find((job) => job.id === event.resource_id)?.status)
    const lifecycleReset = Boolean(event.payload.job_id && event.payload.status === 'running' && previousStatus !== undefined && restartableTransferStatuses.has(previousStatus))
    const loadResetItems = lifecycleReset && selectedJobIDRef.current === event.resource_id
    if (lifecycleReset) clearTerminalLifecycle(event.resource_id, true)
    if (event.payload.job_id && event.payload.status === 'deleted') {
      clearTerminalLifecycle(event.resource_id, false)
      if (selectedJobIDRef.current === event.resource_id) { detailsRequestID.current += 1; selectedJobIDRef.current = ''; setSelectedJobID(''); setDetailsItems([]); setDetailsError('') }
    }
    const nextLive = { ...liveProgressRef.current, [event.resource_id]: event.payload }
    liveProgressRef.current = nextLive
    setLiveProgress(nextLive)
    if (previousStatus !== undefined && previousStatus !== event.payload.status) {
      const status = t(`transfers.${event.payload.status}`)
      setLiveAnnouncement(t(event.payload.share_id ? 'transfers.shareStatusAnnouncement' : 'transfers.jobStatusAnnouncement', { status }))
    }
    if (event.payload.job_id && terminalEventStatuses.has(event.payload.status) && event.payload.completed_files !== undefined && event.payload.total_files !== undefined) {
      commitTerminalSummary(event.resource_id, { completed: event.payload.completed_files, total: event.payload.total_files })
    }
    if (event.payload.share_id) {
      if (event.payload.status === 'deleted') { resumeRequestGeneration.current += 1; setResumeLoadingID('') }
      queueTransferRefresh('shares', false)
    } else if (event.payload.job_id) {
      queueTransferRefresh('jobs', selectedJobIDRef.current === event.resource_id, loadResetItems)
    }
  }, [clearTerminalLifecycle, commitTerminalSummary, queueTransferRefresh, t])
  useTransferEvents(onTransfer)

  const localizedError = (error: unknown, fallback = t('transfers.genericFailure')) => {
    const code = error instanceof APIError ? error.code : 'REQUEST_FAILED'
    return t(`transfers.errors.${code}`, { defaultValue: fallback })
  }

  const showOneTimeCode = (shareID: string, value: string) => {
    const code = { shareID, value, generation: ++capabilityGeneration.current }
    setOneTimeCode(code)
    return code
  }

  const invalidateResumeRequest = () => {
    resumeRequestGeneration.current += 1
    setResumeLoadingID('')
  }

  const openSender = () => {
    invalidateResumeRequest()
    setResumeHistoryError(null)
    setSenderOpen(true)
  }

  const closeCreate = () => {
    setCreateOpen(false)
    if (params.has('new')) { params.delete('new'); setParams(params, { replace: true }) }
  }
  const createRoute = async (values: RouteFormValues) => {
    setSubmitting(true)
    try { await api.createRoute(values); closeCreate(); form.resetFields(); routes.refresh({ silent: true }) }
    catch { void message.error(t('feedback.createFailed')) } finally { setSubmitting(false) }
  }
  const removeRoute = async (id: string) => {
    setBusyID(id)
    try { await api.deleteRoute(id); routes.refresh({ silent: true }); void message.success(t('feedback.deleted')) }
    catch { void message.error(t('feedback.deleteFailed')) } finally { setBusyID('') }
  }

  const addFiles: UploadProps['onChange'] = ({ fileList }) => {
    if (senderOperationRef.current || queueRef.current.operation) { setSelectionError(t('transfers.queueLocked')); return }
    const currentQueue = queueRef.current.items
    const known = new Set(currentQueue.map((item) => item.uid))
    const knownPaths = new Set([...existingFiles.map((file) => file.virtual_path), ...currentQueue.map((item) => item.virtualPath)])
    const additions: QueuedFile[] = []
    let nextBytes = existingFiles.reduce((sum, item) => sum + item.size, 0) + currentQueue.reduce((sum, item) => sum + item.file.size, 0)
    let nextCount = existingFiles.length + currentQueue.length
    let error = ''
    for (const upload of fileList) {
      if (known.has(upload.uid)) continue
      const file = upload.originFileObj
      if (!file) continue
      const virtualPath = file.webkitRelativePath || file.name
      if (knownPaths.has(virtualPath)) { error = t('transfers.duplicatePath'); continue }
      if (file.size > config.transfers.max_file_bytes) { error = t('transfers.overFile', { name: file.name }); continue }
      if (nextCount + 1 > config.transfers.max_files_per_share) { error = t('transfers.overCount'); continue }
      if (nextBytes + file.size > config.transfers.max_share_bytes) { error = t('transfers.overShare'); continue }
      additions.push({ uid: upload.uid, file, virtualPath, status: 'queued' })
      known.add(upload.uid); knownPaths.add(virtualPath); nextBytes += file.size; nextCount += 1
    }
    if (additions.length > 0) dispatchQueue({ type: 'add', files: additions })
    setSelectionError(error)
  }

  const startSending = async () => {
    if (!selectedSenderServerID || senderOperationRef.current || (queue.length === 0 && existingFiles.length === 0)) return
    const operationID = ++senderOperationCounter.current
    const snapshot = Object.freeze(queueRef.current.items.map((item) => Object.freeze({ ...item })))
    if (new Set(snapshot.map((item) => item.virtualPath)).size !== snapshot.length) { setSenderError(t('transfers.duplicatePath')); return }
    senderOperationRef.current = { id: operationID, snapshot }
    dispatchQueue({ type: 'begin', operationID, uids: snapshot.map((item) => item.uid) })
    setSenderBusy(true); setSenderError('')
    let shareID = activeShareID
    let finalized = false
    try {
      if (!shareID) {
        const created = await api.createTransferShare({ server_id: selectedSenderServerID })
        shareID = created.share.id
        setActiveShareID(shareID)
        showOneTimeCode(shareID, created.capability)
        shares.setData((current) => [created.share, ...(current ?? []).filter((share) => share.id !== created.share.id)])
      }
      for (const item of snapshot) {
        if (item.status === 'succeeded') continue
        const current = queueRef.current.items.find((queued) => queued.uid === item.uid)
        if (!current || current.file !== item.file || current.virtualPath !== item.virtualPath || current.status !== item.status || senderOperationRef.current?.id !== operationID) throw new Error('sender operation queue changed')
        dispatchQueue({ type: 'status', operationID, uid: item.uid, status: 'uploading' })
        try {
          await api.uploadTransferShareFile(shareID, item.file, item.virtualPath)
          dispatchQueue({ type: 'status', operationID, uid: item.uid, status: 'succeeded' })
        } catch (error) {
          dispatchQueue({ type: 'status', operationID, uid: item.uid, status: 'failed', error: localizedError(error) })
          throw error
        }
      }
      await api.finalizeTransferShare(shareID)
      finalized = true
      void message.success(t('transfers.finalized'))
      shares.refresh({ silent: true })
      invalidateResumeRequest(); setActiveShareID(''); setExistingFiles([]); setSenderOpen(false)
    } catch (error) { setSenderError(localizedError(error)) } finally {
      dispatchQueue({ type: 'finish', operationID, clearSucceeded: finalized })
      senderOperationRef.current = null
      setSenderBusy(false)
    }
  }

  const finalizeShare = async (share: TransferShare) => {
    setBusyID(share.id)
    try { await api.finalizeTransferShare(share.id); shares.refresh({ silent: true }); void message.success(t('transfers.finalized')) }
    catch (error) { void message.error(localizedError(error)) } finally { setBusyID('') }
  }
  const rotateShare = async (share: TransferShare) => {
    setBusyID(share.id)
    try { const rotated = await api.rotateTransferShare(share.id); showOneTimeCode(share.id, rotated.capability); void message.success(t('transfers.rotated')) }
    catch (error) { void message.error(localizedError(error)) } finally { setBusyID('') }
  }
  const deleteShare = async (share: TransferShare) => {
    invalidateResumeRequest()
    setResumeHistoryError((current) => current?.share.id === share.id ? null : current)
    setBusyID(share.id)
    try { await api.deleteTransferShare(share.id); shares.setData((current) => current?.filter((item) => item.id !== share.id) ?? current); void message.success(t('feedback.deleted')) }
    catch (error) { void message.error(localizedError(error)) } finally { setBusyID('') }
  }
  const resumeShare = async (share: TransferShare) => {
    const generation = ++resumeRequestGeneration.current
    setResumeLoadingID(share.id); setResumeHistoryError(null)
    try {
      const files = await api.transferShareFiles(share.id)
      const latest = sharesDataRef.current?.find((candidate) => candidate.id === share.id)
      const liveStatus = liveProgressRef.current[share.id]?.status
      if (resumeRequestGeneration.current !== generation) return
      if (!latest || latest.status !== 'staging' || (liveStatus !== undefined && liveStatus !== 'staging')) { setResumeHistoryError({ share, message: t('transfers.resumeUnavailable') }); return }
      setActiveShareID(share.id); setSenderServerID(share.server_id); setExistingFiles(files)
      dispatchQueue({ type: 'reset' }); setSenderError(''); setSelectionError(''); setSenderOpen(true)
    } catch { if (resumeRequestGeneration.current === generation) setResumeHistoryError({ share, message: t('transfers.resumeLoadFailed') }) }
    finally { if (resumeRequestGeneration.current === generation) setResumeLoadingID('') }
  }

  const createJob = async (values: ReceiveFormValues) => {
    setReceiveBusy(true); setReceiveError('')
    try {
      const job = await api.createTransferJob(values)
      receiveForm.setFieldValue('capability', '')
      jobs.setData((current) => [job, ...(current ?? []).filter((item) => item.id !== job.id)])
      void message.success(t('transfers.created'))
    } catch (error) { setReceiveError(localizedError(error)) } finally { setReceiveBusy(false) }
  }
  const jobAction = async (job: TransferJob, action: 'start' | 'cancel' | 'retry' | 'delete') => {
    setJobBusyID(job.id)
    try {
      if (action === 'start') { const updated = await api.startTransferJob(job.id); if (updated.status === 'running') clearTerminalLifecycle(job.id, true); jobs.setData((current) => current?.map((item) => item.id === job.id ? updated : item) ?? current); void message.success(t('transfers.started')) }
      if (action === 'cancel') { await api.cancelTransferJob(job.id); void message.success(t('transfers.canceledSuccess')) }
      if (action === 'retry') { const updated = await api.retryTransferJob(job.id); if (updated.status === 'running') clearTerminalLifecycle(job.id, true); jobs.setData((current) => current?.map((item) => item.id === job.id ? updated : item) ?? current); void message.success(t('transfers.retryStarted')) }
      if (action === 'delete') {
        await api.deleteTransferJob(job.id)
        clearTerminalLifecycle(job.id, true, true)
        jobs.setData((current) => current?.filter((item) => item.id !== job.id) ?? current)
        if (selectedJobIDRef.current === job.id) { detailsRequestID.current += 1; selectedJobIDRef.current = ''; setSelectedJobID('') }
        void message.success(t('feedback.deleted'))
      }
      jobs.refresh({ silent: true })
    } catch (error) { void message.error(localizedError(error)) } finally { setJobBusyID('') }
  }
  const openDetails = (job: TransferJob) => { selectedJobIDRef.current = job.id; setSelectedJobID(job.id); setDetailsItems([]); setDetailsItemsLoaded(false); void loadJobItems(job.id) }

  const locale = i18n.resolvedLanguage === 'zh-CN' ? 'zh-CN' : 'en-US'
  const serverNames = useMemo(() => new Map(servers.map((server) => [server.id, server.name])), [servers])
  const clientNames = useMemo(() => new Map(clients.map((client) => [client.id, client.name])), [clients])
  const displayShares = (shares.data ?? []).filter((share) => liveProgress[share.id]?.status !== 'deleted').map((share) => { const live = liveProgress[share.id]; return live ? patchShare(share, live) : share })
  const displayJobs = (jobs.data ?? []).filter((job) => liveProgress[job.id]?.status !== 'deleted').map((job) => { const live = liveProgress[job.id]; return live ? patchJob(job, live) : job })
  const selectedJob = displayJobs.find((job) => job.id === selectedJobID) ?? null
  const fileProgress = (jobID: string) => {
    const live = liveProgress[jobID]
    if (live?.completed_files !== undefined && live.total_files !== undefined) return { completed: live.completed_files, total: live.total_files }
    if (terminalFileSummaries[jobID]) return terminalFileSummaries[jobID]
    if (selectedJobID === jobID && detailsItemsLoaded && !detailsError && !detailsLoading) return { completed: detailsItems.filter((item) => item.status === 'completed').length, total: detailsItems.length }
    return null
  }
  const succeededFiles = queue.filter((item) => item.status === 'succeeded')
  const existingBytes = existingFiles.reduce((sum, item) => sum + item.size, 0)
  const uploadTotals = { received: existingBytes + succeededFiles.reduce((sum, item) => sum + item.file.size, 0), total: existingBytes + queue.reduce((sum, item) => sum + item.file.size, 0), files: existingFiles.length + succeededFiles.length, totalFiles: existingFiles.length + queue.length }

  const shareActions = (share: TransferShare) => <Flex className="transfer-actions" gap={8} wrap="wrap">
    {share.status === 'staging' && <Button loading={resumeLoadingID === share.id} onClick={() => void resumeShare(share)}>{t('common.retry')}</Button>}
    {share.status === 'staging' && share.file_count > 0 && <Popconfirm title={t('transfers.finalizeTitle')} description={t('transfers.finalizeDescription')} okText={t('transfers.finalize')} cancelText={t('common.cancel')} onConfirm={() => void finalizeShare(share)}><Button type="primary" loading={busyID === share.id}>{t('transfers.finalize')}</Button></Popconfirm>}
    {(share.status === 'staging' || share.status === 'ready') && <Popconfirm title={t('transfers.rotateTitle')} description={t('transfers.rotateDescription')} okText={t('transfers.rotate')} cancelText={t('common.cancel')} onConfirm={() => void rotateShare(share)}><Button icon={<SyncOutlined aria-hidden />} loading={busyID === share.id}>{t('transfers.rotate')}</Button></Popconfirm>}
    <Popconfirm title={t('transfers.deleteShareTitle')} description={t('transfers.deleteShareDescription')} okText={t('common.delete')} cancelText={t('common.cancel')} okButtonProps={{ danger: true }} onConfirm={() => void deleteShare(share)}><Button danger icon={<DeleteOutlined aria-hidden />} disabled={share.status === 'deleting'} loading={busyID === share.id}>{t('common.delete')}</Button></Popconfirm>
  </Flex>
  const jobActions = (job: TransferJob, showDetails = true) => <Flex className="transfer-actions" gap={8} wrap="wrap">
    {job.status === 'ready' && <Button type="primary" loading={jobBusyID === job.id} onClick={() => void jobAction(job, 'start')}>{t('transfers.startJob')}</Button>}
    {job.status === 'running' && <Button danger loading={jobBusyID === job.id} onClick={() => void jobAction(job, 'cancel')}>{t('transfers.cancelJob')}</Button>}
    {(job.status === 'failed' || job.status === 'interrupted' || job.status === 'canceled') && <Button type="primary" loading={jobBusyID === job.id} onClick={() => void jobAction(job, 'retry')}>{t('transfers.retryJob')}</Button>}
    {showDetails && <Button icon={<FileOutlined aria-hidden />} onClick={() => openDetails(job)}>{t('transfers.details')}</Button>}
    <Popconfirm title={t('transfers.deleteJobTitle')} description={t('transfers.deleteJobDescription')} okText={t('common.delete')} cancelText={t('common.cancel')} okButtonProps={{ danger: true }} onConfirm={() => void jobAction(job, 'delete')}><Button danger icon={<DeleteOutlined aria-hidden />} disabled={job.status === 'deleting'} loading={jobBusyID === job.id}>{t('common.delete')}</Button></Popconfirm>
  </Flex>

  const shareColumns: TableProps<TransferShare>['columns'] = [
    { title: t('transfers.server'), dataIndex: 'server_id', render: (id: string) => serverNames.get(id) ?? <code>{id}</code> },
    { title: t('common.status'), dataIndex: 'status', render: (_: TransferStatus, share) => <TransferProgress compact status={share.status} receivedBytes={share.total_bytes} totalBytes={share.total_bytes} completedFiles={share.file_count} totalFiles={share.file_count} /> },
    { title: t('transfers.expires'), dataIndex: 'expires_at', render: (value: string) => <span className="tabular-figure">{formatDate(value, locale)}</span> },
    { title: t('common.actions'), key: 'actions', align: 'right', render: (_: unknown, share) => shareActions(share) },
  ]
  const jobColumns: TableProps<TransferJob>['columns'] = [
    { title: t('transfers.client'), dataIndex: 'client_id', render: (id: string) => clientNames.get(id) ?? <code>{id}</code> },
    { title: t('common.status'), dataIndex: 'status', render: (_: TransferStatus, job) => { const files = fileProgress(job.id); return <TransferProgress compact status={job.status} receivedBytes={job.received_bytes} totalBytes={job.total_bytes} completedFiles={files?.completed} totalFiles={files?.total} errorCode={job.error_code} /> } },
    { title: t('transfers.expires'), dataIndex: 'expires_at', render: (value: string) => <span className="tabular-figure">{formatDate(value, locale)}</span> },
    { title: t('common.actions'), key: 'actions', align: 'right', render: (_: unknown, job) => jobActions(job) },
  ]

  const transferPanel = <div className="transfers-console">
    <TransferLimits limits={config.transfers} />
    <Row className="transfer-workflows" gutter={[16, 16]}>
      <Col xs={24} lg={10}><Card className="transfer-workflow-card" title={<Space><SendOutlined aria-hidden />{t('transfers.sender')}</Space>}><Typography.Paragraph type="secondary">{t('transfers.senderHint')}</Typography.Paragraph>{serverResource.error ? <Alert type="warning" showIcon message={t('transfers.loadServersFailed')} action={<Button onClick={() => serverResource.refresh()}>{t('common.retry')}</Button>} /> : serverResource.loading ? <Typography.Text type="secondary">{t('common.loading')}</Typography.Text> : servers.length === 0 ? <Alert type="warning" showIcon message={t('transfers.noServers')} /> : <Button type="primary" icon={<SendOutlined aria-hidden />} onClick={openSender}>{t('transfers.sendFiles')}</Button>}</Card></Col>
      <Col xs={24} lg={14}><Card className="transfer-workflow-card" title={<Space><CloudDownloadOutlined aria-hidden />{t('transfers.receiver')}</Space>}><Typography.Paragraph type="secondary">{t('transfers.receiverHint')}</Typography.Paragraph>{clientResource.error ? <Alert type="warning" showIcon message={t('transfers.loadClientsFailed')} action={<Button onClick={() => clientResource.refresh()}>{t('common.retry')}</Button>} /> : clientResource.loading ? <Typography.Text type="secondary">{t('common.loading')}</Typography.Text> : clients.length === 0 ? <Alert type="warning" showIcon message={t('transfers.noClients')} /> : <Form form={receiveForm} layout="vertical" onFinish={(values) => void createJob(values)}>
        {receiveError && <Alert className="drawer-alert" type="error" showIcon message={receiveError} />}
        <Row gutter={[12, 0]}><Col xs={24} md={8}><Form.Item name="client_id" initialValue={clients[0]?.id} label={t('transfers.client')} rules={[{ required: true, message: t('validation.required') }]}><Select options={clients.map((client) => ({ value: client.id, label: client.name }))} /></Form.Item></Col><Col xs={24} md={10}><Form.Item name="capability" label={t('transfers.capability')} rules={[{ required: true, message: t('validation.required') }]}><Input.Password autoComplete="off" className="mono-input" /></Form.Item></Col><Col xs={24} md={6} className="receive-submit"><Button block type="primary" htmlType="submit" loading={receiveBusy}>{t('transfers.createJob')}</Button></Col></Row>
      </Form>}</Card></Col>
    </Row>
    {resumeHistoryError && <Alert className="inline-callout" type="error" showIcon message={resumeHistoryError.message} action={<Button onClick={() => void resumeShare(resumeHistoryError.share)}>{t('transfers.retryStagedHistory')}</Button>} />}
    <Card className="transfer-history" title={t('transfers.outgoing')}>
      <ResourceState loading={shares.loading} error={shares.error} empty={displayShares.length === 0} emptyTitle={t('transfers.emptyShares')} emptyDescription={t('transfers.emptySharesDescription')} emptyAction={servers.length > 0 ? <Button type="primary" onClick={openSender}>{t('transfers.sendFiles')}</Button> : undefined} retry={() => shares.refresh()}>
        {screens.md ? <Table<TransferShare> className="transfer-table" rowKey="id" size="small" pagination={false} dataSource={displayShares} columns={shareColumns} /> : <List className="transfer-list" dataSource={displayShares} renderItem={(share) => <List.Item><Card size="small" className="transfer-mobile-card" title={serverNames.get(share.server_id) ?? share.server_id}><TransferProgress status={share.status} receivedBytes={share.total_bytes} totalBytes={share.total_bytes} completedFiles={share.file_count} totalFiles={share.file_count} /><Typography.Text type="secondary" className="tabular-figure">{t('transfers.expires')}: {formatDate(share.expires_at, locale)}</Typography.Text><Divider />{shareActions(share)}</Card></List.Item>} />}
      </ResourceState>
    </Card>
    <Card className="transfer-history" title={t('transfers.incoming')}>
      <ResourceState loading={jobs.loading} error={jobs.error} empty={displayJobs.length === 0} emptyTitle={t('transfers.emptyJobs')} emptyDescription={t('transfers.emptyJobsDescription')} retry={() => jobs.refresh()}>
        {screens.md ? <Table<TransferJob> className="transfer-table" rowKey="id" size="small" pagination={false} dataSource={displayJobs} columns={jobColumns} /> : <List className="transfer-list" dataSource={displayJobs} renderItem={(job) => { const files = fileProgress(job.id); return <List.Item><Card size="small" className="transfer-mobile-card" title={clientNames.get(job.client_id) ?? job.client_id}><TransferProgress status={job.status} receivedBytes={job.received_bytes} totalBytes={job.total_bytes} completedFiles={files?.completed} totalFiles={files?.total} errorCode={job.error_code} /><Typography.Text type="secondary" className="tabular-figure">{t('transfers.expires')}: {formatDate(job.expires_at, locale)}</Typography.Text><Divider />{jobActions(job)}</Card></List.Item> }} />}
      </ResourceState>
    </Card>
  </div>

  const changeTab = (key: string) => {
    invalidateResumeRequest()
    setActiveTab(key)
    if (key === 'transfers') params.set('tab', key); else params.delete('tab')
    setParams(params, { replace: true })
  }

  return <div className="page routes-page">
    <span className="sr-only transfer-live-announcement" role="status" aria-live="polite" aria-atomic="true">{liveAnnouncement}</span>
    <PageHeader title={activeTab === 'routes' ? t('routes.title') : t('transfers.title')} subtitle={activeTab === 'routes' ? t('routes.subtitle') : t('transfers.subtitle')} actions={activeTab === 'routes' ? <Button type="primary" icon={<PlusOutlined />} disabled={clients.length === 0 || clientResource.loading || Boolean(clientResource.error)} onClick={() => setCreateOpen(true)}>{t('routes.new')}</Button> : servers.length > 0 && !serverResource.error ? <Button type="primary" icon={<SendOutlined aria-hidden />} onClick={openSender}>{t('transfers.sendFiles')}</Button> : undefined} />
    {oneTimeCode && <Alert className="capability-alert" type="success" showIcon message={t('transfers.capabilityTitle')} description={<Flex vertical gap={8}><Typography.Text>{t('transfers.capabilityDescription')}</Typography.Text><code className="capability-code" tabIndex={0}>{oneTimeCode.value}</code></Flex>} action={<Flex gap={8} wrap="wrap"><CopyButton value={oneTimeCode.value} /><Button type="primary" onClick={() => setOneTimeCode((current) => compareAndClearOneTimeCode(current, oneTimeCode))}>{t('transfers.confirmSaved')}</Button></Flex>} />}
    {senderBusy && <Card className="sender-operation-banner" size="small" title={t('transfers.uploadInProgress')} extra={<Button onClick={() => setSenderOpen(true)}>{t('transfers.viewUpload')}</Button>}><TransferProgress status="running" receivedBytes={uploadTotals.received} totalBytes={uploadTotals.total} completedFiles={uploadTotals.files} totalFiles={uploadTotals.totalFiles} /></Card>}
    <Tabs className="routes-tabs" activeKey={activeTab} onChange={changeTab} items={[
      { key: 'routes', label: t('nav.routes'), children: <RoutePanel routes={routes.data ?? []} clients={clients} routesLoading={routes.loading} routesError={routes.error} clientsLoading={clientResource.loading} clientsError={clientResource.error} refreshRoutes={() => routes.refresh()} refreshClients={() => clientResource.refresh()} openCreate={() => setCreateOpen(true)} remove={(id) => void removeRoute(id)} busyID={busyID} /> },
      { key: 'transfers', label: t('transfers.tab'), children: transferPanel },
    ]} />

    <Drawer title={t('routes.new')} width={500} placement={screens.md ? 'right' : 'bottom'} height={screens.md ? undefined : '88dvh'} open={createOpen} onClose={closeCreate} destroyOnHidden extra={<Button type="primary" loading={submitting} onClick={() => form.submit()}>{t('common.create')}</Button>}>
      <Form form={form} layout="vertical" initialValues={{ access: 'private', base_path: '/', allowed_methods: ['GET', 'HEAD'] }} onFinish={(values) => void createRoute(values)}>
        <Form.Item name="name" label={t('common.name')} rules={[{ required: true, message: t('validation.required') }]}><Input autoFocus maxLength={80} /></Form.Item>
        <Form.Item name="client_id" label={t('routes.client')} rules={[{ required: true, message: t('validation.required') }]}><Select options={clients.map((client) => ({ value: client.id, label: client.name }))} /></Form.Item>
        <Form.Item name="slug" label={t('routes.slug')} rules={[{ required: true, pattern: /^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$/, message: t('validation.slug') }]}><Input addonBefore="/r/" maxLength={63} /></Form.Item>
        <Form.Item name="remote_port" label={t('routes.remotePort')} rules={[{ required: true, message: t('validation.port') }]}><InputNumber min={1} max={65535} className="full-width" /></Form.Item>
        <Form.Item name="base_path" label={t('routes.basePath')} rules={[{ required: true, message: t('validation.required') }]}><Input placeholder="/" /></Form.Item>
        <Form.Item name="access" label={t('routes.access')}><Radio.Group optionType="button" buttonStyle="solid" options={[{ value: 'private', label: t('routes.private') }, { value: 'public', label: t('routes.public') }]} /></Form.Item>
        <Form.Item name="allowed_methods" label={t('routes.methods')} tooltip={t('routes.methodsHint')}><Checkbox.Group options={[{ label: 'GET', value: 'GET', disabled: true }, 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS']} /></Form.Item>
      </Form>
    </Drawer>

    <Drawer title={activeShareID ? t('transfers.retryUpload') : t('transfers.sendFiles')} width={600} placement={screens.md ? 'right' : 'bottom'} height={screens.md ? undefined : '92dvh'} open={senderOpen} maskClosable keyboard onClose={() => { invalidateResumeRequest(); setSenderOpen(false) }} extra={<Button type="primary" disabled={(queue.length === 0 && existingFiles.length === 0) || !selectedSenderServerID || senderBusy} loading={senderBusy} onClick={() => void startSending()}>{queue.some((item) => item.status === 'failed') ? t('transfers.retryUpload') : queue.length === 0 ? t('transfers.finalize') : t('transfers.uploadAndFinalize')}</Button>}>
      <TransferLimits limits={config.transfers} />
      {senderError && <Alert className="drawer-alert" type="error" showIcon message={senderError} action={<Button onClick={() => void startSending()}>{t('common.retry')}</Button>} />}
      {selectionError && <Alert className="drawer-alert" type="warning" showIcon message={selectionError} />}
      <Form layout="vertical" className="sender-form"><Form.Item label={t('transfers.server')} required><Select aria-label={t('transfers.server')} disabled={Boolean(activeShareID) || senderBusy} value={selectedSenderServerID || undefined} onChange={setSenderServerID} options={servers.map((server) => ({ value: server.id, label: server.name }))} /></Form.Item></Form>
      <Upload.Dragger multiple disabled={senderBusy} beforeUpload={() => false} fileList={[] as UploadFile[]} showUploadList={false} onChange={addFiles} aria-label={t('transfers.selectFiles')}>
        <p className="ant-upload-drag-icon"><InboxOutlined aria-hidden /></p><p className="ant-upload-text">{t('transfers.selectFiles')}</p><p className="ant-upload-hint">{t('transfers.selectFilesHint')}</p>
      </Upload.Dragger>
      {(queue.length > 0 || existingFiles.length > 0) && <><Divider>{t('transfers.queue')}</Divider><TransferProgress status={senderBusy ? 'running' : 'staging'} receivedBytes={uploadTotals.received} totalBytes={uploadTotals.total} completedFiles={uploadTotals.files} totalFiles={uploadTotals.totalFiles} /><List className="upload-queue" dataSource={queue} renderItem={(item) => <List.Item actions={item.status === 'succeeded' ? [] : [<Button key="remove" className="queue-delete-button" type="text" danger disabled={senderBusy} aria-label={`${t('common.delete')} ${item.file.name}`} icon={<DeleteOutlined aria-hidden />} onClick={() => dispatchQueue({ type: 'remove', uid: item.uid })} />]}><List.Item.Meta title={<span className="transfer-path">{item.virtualPath}</span>} description={<Flex vertical gap={4}><Typography.Text className="tabular-figure" type="secondary">{formatTransferBytes(item.file.size, locale)}</Typography.Text><Tag color={item.status === 'succeeded' ? 'success' : item.status === 'failed' ? 'error' : item.status === 'uploading' ? 'processing' : 'default'}>{t(`transfers.${item.status}`)}</Tag>{item.error && <Typography.Text type="danger">{item.error}</Typography.Text>}</Flex>} /></List.Item>} /></>}
    </Drawer>

    <Drawer title={t('transfers.details')} width={680} placement={screens.md ? 'right' : 'bottom'} height={screens.md ? undefined : '92dvh'} open={Boolean(selectedJob)} onClose={() => { detailsRequestID.current += 1; selectedJobIDRef.current = ''; setSelectedJobID(''); setDetailsItems([]); setDetailsItemsLoaded(false); setDetailsLoading(false); setDetailsError('') }}>
      {selectedJob && <><TransferProgress status={selectedJob.status} receivedBytes={selectedJob.received_bytes} totalBytes={selectedJob.total_bytes} completedFiles={fileProgress(selectedJob.id)?.completed} totalFiles={fileProgress(selectedJob.id)?.total} errorCode={selectedJob.error_code} /><div className="drawer-job-actions">{jobActions(selectedJob, false)}</div></>}
      {detailsError && <Alert className="drawer-alert" type="error" showIcon message={detailsError} action={selectedJobID ? <Button onClick={() => void loadJobItems(selectedJobID)}>{t('common.retry')}</Button> : undefined} />}
      {detailsLoading ? <Result status="info" title={t('common.loading')} /> : !detailsError && <List className="transfer-item-list" locale={{ emptyText: <Empty description={t('transfers.noItems')} /> }} dataSource={detailsItems} renderItem={(item: TransferItem) => <List.Item actions={selectedJob?.status === 'completed' && item.status === 'completed' ? [<Button key="download" type="link" icon={<CloudDownloadOutlined aria-hidden />} href={api.transferItemDownloadHref(item.job_id, item.id)} download>{t('transfers.download')}</Button>] : []}><List.Item.Meta title={<span className="transfer-path" tabIndex={0}>{item.virtual_path}</span>} description={<TransferProgress compact status={item.status} receivedBytes={item.received_bytes} totalBytes={item.size} completedFiles={item.status === 'completed' ? 1 : 0} totalFiles={1} errorCode={selectedJob?.error_code} />} /></List.Item>} />}
    </Drawer>
  </div>
}
