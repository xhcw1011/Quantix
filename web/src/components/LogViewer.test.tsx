import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import LogViewer from './LogViewer'
import { getRecentLogs } from '../api/trading'

vi.mock('../api/trading', () => ({
  getRecentLogs: vi.fn(),
}))

const mockedGetRecentLogs = vi.mocked(getRecentLogs)

describe('LogViewer', () => {
  beforeEach(() => {
    mockedGetRecentLogs.mockReset()
  })

  it('renders fetched log lines', async () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    mockedGetRecentLogs.mockResolvedValue({ data: { lines: ['hello world'] } } as any)
    render(<LogViewer engineId="engine-1" />)
    expect(await screen.findByText('hello world')).toBeInTheDocument()
  })

  it('queries with the typed filter when 查询 is clicked', async () => {
    const user = userEvent.setup()
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    mockedGetRecentLogs.mockResolvedValue({ data: { lines: [] } } as any)
    render(<LogViewer engineId="engine-1" />)
    await waitFor(() => expect(mockedGetRecentLogs).toHaveBeenCalledTimes(1))

    await user.type(screen.getByPlaceholderText(/golden cross/), 'STOP')
    await user.click(screen.getByRole('button', { name: '查询' }))

    await waitFor(() => expect(mockedGetRecentLogs).toHaveBeenLastCalledWith('engine-1', 300, 'STOP'))
  })

  it('shows an error message when the fetch fails', async () => {
    mockedGetRecentLogs.mockRejectedValue({ response: { data: { error: '拉取失败' } } })
    render(<LogViewer engineId="engine-1" />)
    expect(await screen.findByText('拉取失败')).toBeInTheDocument()
  })
})
