import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import StatusBadge from './StatusBadge'

describe('StatusBadge', () => {
  it('renders the known FILLED status with its green color classes', () => {
    render(<StatusBadge status="FILLED" />)
    const el = screen.getByText('FILLED')
    expect(el.className).toContain('bg-green-900/50')
    expect(el.className).toContain('text-green-300')
  })

  it('falls back to a neutral slate color for an unrecognized status', () => {
    render(<StatusBadge status="WEIRD_NEW_STATUS" />)
    const el = screen.getByText('WEIRD_NEW_STATUS')
    expect(el.className).toContain('bg-slate-600')
    expect(el.className).toContain('text-slate-300')
  })

  it('renders a custom label while still coloring by the raw status', () => {
    render(<StatusBadge status="FILLED" label="已成交" />)
    const el = screen.getByText('已成交')
    expect(el.className).toContain('bg-green-900/50')
  })
})
