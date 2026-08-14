import { describe, it, expect } from 'vitest'
import { isClosingSide, actionLabel, derivedEntryPrice } from './positionFormat'

describe('isClosingSide', () => {
  it('LONG + SELL closes the long', () => {
    expect(isClosingSide('SELL', 'LONG')).toBe(true)
  })
  it('LONG + BUY opens/adds to the long', () => {
    expect(isClosingSide('BUY', 'LONG')).toBe(false)
  })
  it('SHORT + BUY closes the short', () => {
    expect(isClosingSide('BUY', 'SHORT')).toBe(true)
  })
  it('SHORT + SELL opens/adds to the short', () => {
    expect(isClosingSide('SELL', 'SHORT')).toBe(false)
  })
  it('one-way mode (no position_side): SELL closes the long', () => {
    expect(isClosingSide('SELL', '')).toBe(true)
  })
  it('one-way mode (no position_side): BUY opens/adds to the long', () => {
    expect(isClosingSide('BUY', '')).toBe(false)
  })
})

describe('actionLabel', () => {
  it('BUY+LONG is 开多', () => {
    expect(actionLabel('BUY', 'LONG')).toBe('开多')
  })
  it('SELL+LONG is 平多', () => {
    expect(actionLabel('SELL', 'LONG')).toBe('平多')
  })
  it('SELL+SHORT is 开空', () => {
    expect(actionLabel('SELL', 'SHORT')).toBe('开空')
  })
  it('BUY+SHORT is 平空', () => {
    expect(actionLabel('BUY', 'SHORT')).toBe('平空')
  })
  it('one-way mode (no position_side): BUY is 买', () => {
    expect(actionLabel('BUY', '')).toBe('买')
  })
  it('one-way mode (no position_side): SELL is 卖', () => {
    expect(actionLabel('SELL', '')).toBe('卖')
  })
})

// realized = (exit - entry) * qty - fee for LONG/net, (entry - exit) * qty - fee for
// SHORT (see internal/oms/position.go ApplyFill) — derivedEntryPrice inverts that so a
// closing fill row can show what it paired against without cross-referencing other rows.
describe('derivedEntryPrice', () => {
  it('LONG close: profit implies a lower entry than exit', () => {
    // entry 100, exit 110, qty 2, fee 1 → realized = (110-100)*2 - 1 = 19
    expect(derivedEntryPrice('LONG', 110, 2, 1, 19)).toBeCloseTo(100, 6)
  })
  it('SHORT close: profit implies a higher entry than exit', () => {
    // entry 110, exit 100, qty 2, fee 1 → realized = (110-100)*2 - 1 = 19
    expect(derivedEntryPrice('SHORT', 100, 2, 1, 19)).toBeCloseTo(110, 6)
  })
  it('one-way mode (no position_side) behaves like a LONG close', () => {
    expect(derivedEntryPrice('', 110, 2, 1, 19)).toBeCloseTo(100, 6)
  })
  it('a losing close still inverts correctly', () => {
    // entry 100, exit 95, qty 2, fee 1 → realized = (95-100)*2 - 1 = -11
    expect(derivedEntryPrice('LONG', 95, 2, 1, -11)).toBeCloseTo(100, 6)
  })
  it('returns null when qty is zero or missing', () => {
    expect(derivedEntryPrice('LONG', 110, 0, 1, 19)).toBeNull()
  })
  it('returns null when exit price is not positive', () => {
    expect(derivedEntryPrice('LONG', 0, 2, 1, 19)).toBeNull()
  })
})
