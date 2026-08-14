// 方向的中文
export const sideCn = (side: string): string =>
  side === 'LONG' ? '做多' : side === 'SHORT' ? '做空' : '净'

// A trade closes/reduces the position when its Side moves opposite to the
// PositionSide it's tagged with (hedge mode). One-way/spot orders carry no
// PositionSide ("") and are always long-only in this codebase (shorting
// requires hedge mode — see macross's EnableShort), so a bare SELL there is
// also a close.
export const isClosingSide = (side: string, positionSide: string): boolean => {
  if (positionSide === 'LONG') return side === 'SELL'
  if (positionSide === 'SHORT') return side === 'BUY'
  return side === 'SELL'
}

// Human-readable action label matching how exchanges display hedge-mode
// orders: 开多/平多/开空/平空 instead of a bare 买/卖 that looks identical for
// both legs of a same-bar direction flip (2026-08-13 finding: two BUY or two
// SELL rows next to each other, one opening the new leg and one closing the
// old one, looked like a contradiction without this context).
export const actionLabel = (side: string, positionSide: string): string => {
  if (positionSide !== 'LONG' && positionSide !== 'SHORT') {
    return side === 'BUY' ? '买' : side === 'SELL' ? '卖' : side
  }
  const dir = positionSide === 'LONG' ? '多' : '空'
  return (isClosingSide(side, positionSide) ? '平' : '开') + dir
}

// Backs out the (average) entry price a closing fill was measured against,
// from data already on the fill row — no cross-referencing other rows needed.
// realized_pnl already nets out fee (internal/oms/position.go ApplyFill:
// LONG/net realized = (exit-entry)*qty-fee, SHORT realized = (entry-exit)*qty-fee),
// so inverting gives entry = exit ∓ (realized+fee)/qty. This is always the
// weighted AVERAGE entry (even a single opening fill's avg is just itself),
// not necessarily one specific earlier order — a position can be built from
// several opening fills before one closing fill nets it out, so there isn't
// always a single "matching" order to point to anyway (2026-08-13 finding:
// users couldn't tell which order/fill rows paired with which).
export const derivedEntryPrice = (
  positionSide: string, exitPrice: number, qty: number, fee: number, realizedPnl: number,
): number | null => {
  if (!(qty > 0) || !(exitPrice > 0)) return null
  const sign = positionSide === 'SHORT' ? 1 : -1
  return exitPrice + (sign * (realizedPnl + fee)) / qty
}

// 价格格式化:>=1 用千分位 + 2 位小数;不足 1 的小币保留更多小数位
export const fmtPrice = (v: number): string => {
  if (v == null || !isFinite(v)) return '—'
  if (Math.abs(v) >= 1) {
    return v.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  }
  return v.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 8 })
}

// 数量:最多 6 位小数,去掉多余的末尾 0(避免 0.063000 这种)
export const fmtQty = (v: number): string => {
  if (v == null || !isFinite(v)) return '—'
  const s = v.toFixed(6)
  return s.includes('.') ? s.replace(/\.?0+$/, '') : s
}

// 带符号的盈亏金额:+$12.3400 / -$12.3400
export const fmtSignedUsd = (v: number): string => {
  const sign = v >= 0 ? '+' : '-'
  return `${sign}$${Math.abs(v).toFixed(4)}`
}
