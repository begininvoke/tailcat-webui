// @vitest-environment jsdom
import { ConfigProvider } from 'antd'
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import '../i18n'
import { TransferProgress } from './TransferProgress'

describe('TransferProgress', () => {
  it('renders zero totals safely with text and a non-active progress bar', () => {
    const { container } = render(<ConfigProvider><TransferProgress status="running" receivedBytes={0} totalBytes={0} completedFiles={0} totalFiles={0} /></ConfigProvider>)
    expect(screen.getByText('Running')).not.toBeNull()
    expect(container.querySelector('.transfer-progress-amounts')?.textContent).toBe('0 B / 0 B · 0 / 0 files')
    expect(screen.getByText('0%')).not.toBeNull()
    expect(container.querySelector('.ant-progress-status-normal')).not.toBeNull()
    expect(container.querySelector('.ant-progress-status-active')).toBeNull()
  })

  it('localizes integrity failures and clamps invalid ratios', () => {
    render(<ConfigProvider><TransferProgress status="failed" receivedBytes={200} totalBytes={100} completedFiles={2} totalFiles={1} errorCode="transfer_integrity_mismatch" /></ConfigProvider>)
    expect(screen.getByText('Integrity verification failed. Retry to receive verified blocks again.')).not.toBeNull()
    expect(screen.getByText('100%')).not.toBeNull()
  })
})
