import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import NumberInput from './NumberInput'

// A thin controlled wrapper mirroring how every real call site uses
// NumberInput: value/onChange wired to real React state, not a mock — so
// these tests exercise the actual re-render loop, not just prop callbacks.
function Controlled({
  initial = '' as number | '',
  min,
  max,
}: {
  initial?: number | ''
  min?: number
  max?: number
}) {
  const [v, setV] = useState<number | ''>(initial)
  return <NumberInput value={v} onChange={setV} min={min} max={max} placeholder="qty" />
}

describe('NumberInput', () => {
  it('does not truncate a partial decimal while typing character by character', async () => {
    const user = userEvent.setup()
    render(<Controlled />)
    const input = screen.getByPlaceholderText('qty') as HTMLInputElement

    // Reproduces the exact 2026-08 "持仓" bug: typing "0.005" one keystroke
    // at a time must never snap back to "0" mid-type.
    await user.type(input, '0')
    expect(input.value).toBe('0')
    await user.type(input, '.')
    expect(input.value).toBe('0.')
    await user.type(input, '0')
    expect(input.value).toBe('0.0')
    await user.type(input, '0')
    expect(input.value).toBe('0.00')
    await user.type(input, '5')
    expect(input.value).toBe('0.005')
  })

  it('does not blank the field when the leading digit is zero', async () => {
    // Reproduces the `qty > 0 ? display : ''` zero-falsy bug: an initial
    // value of 0 must render as "0", not an empty box.
    const user = userEvent.setup()
    render(<Controlled initial={0} />)
    const input = screen.getByPlaceholderText('qty') as HTMLInputElement
    expect(input.value).toBe('0')
    await user.type(input, '.5')
    expect(input.value).toBe('0.5')
  })

  it('allows clearing the field to empty without forcing it back to 0', async () => {
    const user = userEvent.setup()
    render(<Controlled initial={5} />)
    const input = screen.getByPlaceholderText('qty') as HTMLInputElement
    await user.clear(input)
    expect(input.value).toBe('')
  })

  it('does not clamp to min/max while typing, only on blur', async () => {
    const user = userEvent.setup()
    render(<Controlled initial="" min={50} max={200} />)
    const input = screen.getByPlaceholderText('qty') as HTMLInputElement

    // Typing "123" digit by digit must be able to pass through "1" and "12"
    // (both below min=50) without getting force-clamped mid-type -- this is
    // the EngineDetail.tsx:319 bug (Math.min/Math.max inside onChange) that
    // makes it impossible to type any multi-digit number below the max.
    await user.type(input, '1')
    expect(input.value).toBe('1')
    await user.type(input, '2')
    expect(input.value).toBe('12')
    await user.type(input, '3')
    expect(input.value).toBe('123')

    await user.tab() // blur
    expect(input.value).toBe('123') // within range, unchanged
  })

  it('clamps an out-of-range value to min/max on blur', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    function Wrapped() {
      const [v, setV] = useState<number | ''>('')
      return (
        <NumberInput
          value={v}
          onChange={(x) => {
            setV(x)
            onChange(x)
          }}
          min={50}
          max={200}
          placeholder="qty"
        />
      )
    }
    render(<Wrapped />)
    const input = screen.getByPlaceholderText('qty') as HTMLInputElement
    await user.type(input, '5')
    await user.tab()
    expect(input.value).toBe('50')
    expect(onChange).toHaveBeenLastCalledWith(50)
  })

  it('commits an empty field as "" on blur, not 0', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    function Wrapped() {
      const [v, setV] = useState<number | ''>(5)
      return (
        <NumberInput
          value={v}
          onChange={(x) => {
            setV(x)
            onChange(x)
          }}
          placeholder="qty"
        />
      )
    }
    render(<Wrapped />)
    const input = screen.getByPlaceholderText('qty') as HTMLInputElement
    await user.clear(input)
    await user.tab()
    expect(onChange).toHaveBeenLastCalledWith('')
  })
})
