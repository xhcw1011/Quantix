import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import LeverageSlider from './LeverageSlider'

describe('LeverageSlider', () => {
  it('shows the current value and calls onChange when moved', () => {
    const onChange = vi.fn()
    render(<LeverageSlider value={5} onChange={onChange} />)
    expect(screen.getByText('5x')).toBeInTheDocument()
    const slider = screen.getByRole('slider') as HTMLInputElement
    expect(slider.value).toBe('5')
  })

  it('renders an optional hint next to the label', () => {
    render(<LeverageSlider value={5} onChange={() => {}} hint="(仅新开仓)" />)
    expect(screen.getByText('(仅新开仓)')).toBeInTheDocument()
  })
})
