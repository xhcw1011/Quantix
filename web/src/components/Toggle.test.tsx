import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import Toggle from './Toggle'

describe('Toggle', () => {
  it('calls onChange with the flipped value when clicked', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<Toggle checked={false} onChange={onChange} />)
    await user.click(screen.getByRole('button'))
    expect(onChange).toHaveBeenCalledWith(true)
  })

  it('applies the active color only when checked', () => {
    const { rerender } = render(<Toggle checked={false} onChange={() => {}} activeColor="bg-purple-600" />)
    expect(screen.getByRole('button').className).toContain('bg-slate-600')
    expect(screen.getByRole('button').className).not.toContain('bg-purple-600')

    rerender(<Toggle checked={true} onChange={() => {}} activeColor="bg-purple-600" />)
    expect(screen.getByRole('button').className).toContain('bg-purple-600')
  })
})
