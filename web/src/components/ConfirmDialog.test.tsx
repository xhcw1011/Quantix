import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { ConfirmProvider } from './ConfirmDialog'
import { useConfirm } from '../hooks/useConfirm'

function Caller({ onResult }: { onResult: (ok: boolean) => void }) {
  const confirm = useConfirm()
  return (
    <button
      onClick={async () => {
        const ok = await confirm({ title: '停止「BTCUSDT-5m-macross」？', message: '会真实平仓，不可撤销。', confirmLabel: '停止' })
        onResult(ok)
      }}
    >
      trigger
    </button>
  )
}

describe('ConfirmDialog / useConfirm', () => {
  it('resolves true when the confirm button is clicked', async () => {
    const user = userEvent.setup()
    const results: boolean[] = []
    render(
      <ConfirmProvider>
        <Caller onResult={(ok) => results.push(ok)} />
      </ConfirmProvider>
    )
    await user.click(screen.getByText('trigger'))
    expect(screen.getByText('停止「BTCUSDT-5m-macross」？')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '停止' }))
    expect(results).toEqual([true])
    expect(screen.queryByText('停止「BTCUSDT-5m-macross」？')).not.toBeInTheDocument()
  })

  it('resolves false when the cancel button is clicked', async () => {
    const user = userEvent.setup()
    const results: boolean[] = []
    render(
      <ConfirmProvider>
        <Caller onResult={(ok) => results.push(ok)} />
      </ConfirmProvider>
    )
    await user.click(screen.getByText('trigger'))
    await user.click(screen.getByRole('button', { name: '取消' }))
    expect(results).toEqual([false])
  })

  it('resolves false on Escape', async () => {
    const user = userEvent.setup()
    const results: boolean[] = []
    render(
      <ConfirmProvider>
        <Caller onResult={(ok) => results.push(ok)} />
      </ConfirmProvider>
    )
    await user.click(screen.getByText('trigger'))
    await user.keyboard('{Escape}')
    expect(results).toEqual([false])
  })
})
