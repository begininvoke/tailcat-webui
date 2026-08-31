// @vitest-environment jsdom
import { render, screen } from '@testing-library/react'
import { ConfigProvider } from 'antd'
import { describe, expect, it } from 'vitest'
import '../i18n'
import { OperationProgress } from './OperationProgress'

describe('OperationProgress', () => {
  it('makes running progress and path legible without relying on color', () => {
    render(<ConfigProvider><OperationProgress run={{ id: 'run-1', client_id: 'client-1', kind: 'throughput', status: 'running', path: 'direct', upload_bytes: 1024, download_bytes: 2048, upload_bps: 8000, download_bps: 16000, started_at: '2026-08-31T12:00:00Z' }} progress={42} /></ConfigProvider>)

    expect(screen.getByRole('progressbar', { name: 'Diagnostic progress: 42%' })).not.toBeNull()
    expect(screen.getByText('42% complete')).not.toBeNull()
    expect(screen.getByText('Direct')).not.toBeNull()
  })
})
