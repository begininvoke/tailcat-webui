// @vitest-environment jsdom
import { render, screen } from '@testing-library/react'
import { ConfigProvider } from 'antd'
import { describe, expect, it } from 'vitest'
import '../i18n'
import { DiagnosticPathTag, DiagnosticStatusTag, OperationProgress } from './OperationProgress'

describe('OperationProgress', () => {
  it('makes running progress and path legible without relying on color', () => {
    render(<ConfigProvider><OperationProgress run={{ id: 'run-1', client_id: 'client-1', kind: 'throughput', status: 'running', path: 'direct', upload_bytes: 1024, download_bytes: 2048, upload_bps: 8000, download_bps: 16000, started_at: '2026-08-31T12:00:00Z' }} progress={42} /></ConfigProvider>)

    expect(screen.getByRole('progressbar', { name: 'Diagnostic progress: 42%' })).not.toBeNull()
    expect(screen.getByText('42% complete')).not.toBeNull()
    expect(screen.getByText('Direct')).not.toBeNull()

    const progress = screen.getByRole('progressbar', { name: 'Diagnostic progress: 42%' })
    expect(progress.className).toContain('ant-progress-status-normal')
    expect(progress.className).not.toMatch(/active|processing/)
  })

  it('keeps successful status, direct path, and compact labels on contrast-safe styling hooks', () => {
    const { container } = render(<ConfigProvider><DiagnosticStatusTag status="succeeded" /><DiagnosticPathTag path="direct" /><OperationProgress compact run={{ id: 'run-2', client_id: 'client-1', kind: 'ping', status: 'succeeded', path: 'direct', latency_ms: 1, upload_bytes: 0, download_bytes: 0, upload_bps: 0, download_bps: 0, started_at: '2026-09-01T12:00:00Z', finished_at: '2026-09-01T12:00:01Z' }} /></ConfigProvider>)

    expect(screen.getByText('Succeeded').className).toContain('diagnostic-status-succeeded')
    expect(screen.getByText('Direct').className).toContain('diagnostic-path-direct')
    expect(container.querySelector('.ant-descriptions-item-label')?.closest('.operation-progress')).not.toBeNull()
  })
})
