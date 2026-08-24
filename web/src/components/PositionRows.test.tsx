import type { ReactElement } from 'react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import PositionRows from './PositionRows'
import { closeEnginePosition } from '../api/trading'
import { ConfirmProvider } from './ConfirmDialog'

// PositionRows renders both a table (sm and up) and a card list (below sm) —
// jsdom doesn't evaluate the `hidden sm:block` / `sm:hidden` breakpoint
// classes, so both are simultaneously present in the test DOM. Scope queries
// to the table so each assertion matches exactly one element.
const tableOf = (container: HTMLElement) => within(container.querySelector('table')!)

// PositionRows confirms destructive actions via the shared ConfirmDialog
// (useConfirm), which reads its context from <ConfirmProvider> — same
// wrapper the real app mounts at the root (see App.tsx).
const renderWithConfirm = (ui: ReactElement) => render(<ConfirmProvider>{ui}</ConfirmProvider>)

vi.mock('../api/trading', () => ({
  closeEnginePosition: vi.fn(),
}))

const mockedClose = vi.mocked(closeEnginePosition)

const LONG_POSITION = {
  symbol: 'BTCUSDT',
  position_side: 'LONG',
  qty: 0.05,
  avg_entry_price: 64000,
  unrealized_pnl: 12.5,
  realized_pnl: 0,
}

describe('PositionRows', () => {
  beforeEach(() => {
    mockedClose.mockReset()
  })

  it('renders nothing when there are no positions', () => {
    const { container } = renderWithConfirm(
      <PositionRows engineId="e1" mode="live" lastPrice={64500} positions={[]} onClosed={() => {}} />
    )
    expect(container.firstChild).toBeNull()
  })

  it('renders a row with symbol, side, qty and pnl', () => {
    const { container } = renderWithConfirm(
      <PositionRows engineId="e1" mode="live" lastPrice={64500} positions={[LONG_POSITION]} onClosed={() => {}} />
    )
    const table = tableOf(container)
    expect(table.getByText('BTCUSDT')).toBeInTheDocument()
    expect(table.getByText('做多')).toBeInTheDocument()
    expect(table.getByText('+$12.5000')).toBeInTheDocument()
  })

  it('closes the position when the close button is confirmed', async () => {
    const user = userEvent.setup()
    const onClosed = vi.fn()
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    mockedClose.mockResolvedValue({ data: { qty: 0.05, symbol: 'BTCUSDT', fill_price: 64500 } } as any)
    vi.spyOn(window, 'alert').mockImplementation(() => {})
    const { container } = renderWithConfirm(
      <PositionRows engineId="e1" mode="live" lastPrice={64500} positions={[LONG_POSITION]} onClosed={onClosed} />
    )
    await user.click(tableOf(container).getByRole('button', { name: '平多仓' }))
    // The close is gated by the in-app ConfirmDialog (replaces window.confirm) --
    // confirm it before the actual close call fires.
    await user.click(within(container).getByRole('button', { name: '平仓' }))
    await waitFor(() => expect(mockedClose).toHaveBeenCalledWith('e1', 'LONG'))
    await waitFor(() => expect(onClosed).toHaveBeenCalled())
  })

  it('does not close the position when the confirm dialog is cancelled', async () => {
    const user = userEvent.setup()
    const { container } = renderWithConfirm(
      <PositionRows engineId="e1" mode="live" lastPrice={64500} positions={[LONG_POSITION]} onClosed={() => {}} />
    )
    await user.click(tableOf(container).getByRole('button', { name: '平多仓' }))
    await user.click(within(container).getByRole('button', { name: '取消' }))
    expect(mockedClose).not.toHaveBeenCalled()
  })

  it('disables the close button when mode is not live', () => {
    const { container } = renderWithConfirm(
      <PositionRows engineId="e1" mode="paper" lastPrice={64500} positions={[LONG_POSITION]} onClosed={() => {}} />
    )
    expect(tableOf(container).getByRole('button', { name: '平多仓' })).toBeDisabled()
  })
})
