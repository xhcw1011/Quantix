import { describe, it, expect, vi, beforeEach } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { useEngineLogs } from './useEngineLogs'
import { getRecentLogs } from '../api/trading'

vi.mock('../api/trading', () => ({
  getRecentLogs: vi.fn(),
}))

const mockedGetRecentLogs = vi.mocked(getRecentLogs)

describe('useEngineLogs', () => {
  beforeEach(() => {
    mockedGetRecentLogs.mockReset()
  })

  it('fetches logs for the given engine on mount', async () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    mockedGetRecentLogs.mockResolvedValue({ data: { lines: ['line1', 'line2'] } } as any)
    const { result } = renderHook(() => useEngineLogs('engine-1'))
    await waitFor(() => expect(result.current.lines).toEqual(['line1', 'line2']))
    expect(mockedGetRecentLogs).toHaveBeenCalledWith('engine-1', 300, '')
  })

  it('does not fetch when engineId is undefined', () => {
    renderHook(() => useEngineLogs(undefined))
    expect(mockedGetRecentLogs).not.toHaveBeenCalled()
  })

  it('surfaces fetch errors', async () => {
    mockedGetRecentLogs.mockRejectedValue({ response: { data: { error: '拉取失败' } } })
    const { result } = renderHook(() => useEngineLogs('engine-1'))
    await waitFor(() => expect(result.current.err).toBe('拉取失败'))
  })

  it('polls every 5s while autoRefresh is enabled', async () => {
    vi.useFakeTimers()
    try {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      mockedGetRecentLogs.mockResolvedValue({ data: { lines: [] } } as any)
      const { result } = renderHook(() => useEngineLogs('engine-1'))
      await act(async () => { await Promise.resolve() })
      expect(mockedGetRecentLogs).toHaveBeenCalledTimes(1)

      act(() => { result.current.setAutoRefresh(true) })
      await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
      expect(mockedGetRecentLogs).toHaveBeenCalledTimes(2)
    } finally {
      vi.useRealTimers()
    }
  })
})
