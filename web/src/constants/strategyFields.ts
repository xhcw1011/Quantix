// Per-strategy config field schema. Drives the dynamic form in Engine.tsx so
// non-technical users set parameters with labelled inputs instead of raw JSON.
//
// `default` values are in DISPLAY units (what the user sees). For fields with
// pctOf1=true the UI shows a percent (e.g. 2) but the value sent to the backend is
// the fraction (0.02) — the form divides by 100 on submit. Number params without
// pctOf1 are sent as-is.

export interface FieldDef {
  key: string // backend param key
  label: string // Chinese label
  type: 'number' | 'select'
  default: number | string
  unit?: string // e.g. 'USDT', '%', 'bar', '×'
  step?: number
  min?: number
  max?: number
  options?: { value: string; label: string }[] // for select
  help?: string
  pctOf1?: boolean // UI shows %, backend gets fraction (÷100)
}

const PERIOD_OPTIONS = [
  { value: 'daily', label: '每天' },
  { value: 'weekly', label: '每周' },
  { value: 'monthly', label: '每月' },
]

export const STRATEGY_FIELDS: Record<string, FieldDef[]> = {
  dca: [
    { key: 'BuyQuoteAmount', label: '每次金额', type: 'number', default: 50, unit: 'USDT', min: 1, help: '每个周期买入多少 U' },
    { key: 'Interval', label: '定投周期', type: 'select', default: 'weekly', options: PERIOD_OPTIONS },
    { key: 'MaxTotalQuote', label: '累计上限', type: 'number', default: 0, unit: 'USDT', min: 0, help: '0 = 不限' },
  ],
  dipdca: [
    { key: 'BaseQuoteAmount', label: '基础金额', type: 'number', default: 50, unit: 'USDT', min: 1 },
    { key: 'Interval', label: '定投周期', type: 'select', default: 'weekly', options: PERIOD_OPTIONS },
    { key: 'RefPeriod', label: '均线周期', type: 'number', default: 20, unit: '根', min: 2, help: '用多少根 K 线算参考均价' },
    { key: 'DipRefPct', label: '加满跌幅', type: 'number', default: 10, unit: '%', pctOf1: true, min: 0.5, help: '价格低于均线这么多时,买入倍数拉满' },
    { key: 'DipMultiplier', label: '最大倍数', type: 'number', default: 2, unit: '×', step: 0.5, min: 1 },
    { key: 'MaxTotalQuote', label: '累计上限', type: 'number', default: 0, unit: 'USDT', min: 0, help: '0 = 不限' },
  ],
  spotgrid: [
    { key: 'StepPct', label: '网格间距', type: 'number', default: 2, unit: '%', step: 0.1, pctOf1: true, min: 0.1, help: '跌这么多买一格、涨这么多卖一格' },
    { key: 'QuotePerBuy', label: '每格金额', type: 'number', default: 50, unit: 'USDT', min: 1 },
    { key: 'MaxHoldQuote', label: '持仓上限', type: 'number', default: 500, unit: 'USDT', min: 0, help: '0 = 不限;下跌市里防越买越多' },
  ],
  rebalance: [
    { key: 'TargetBasePct', label: '目标持币比例', type: 'number', default: 50, unit: '%', pctOf1: true, min: 1, max: 99, help: '币 : 现金 的目标比例(如 50%)' },
    { key: 'BandPct', label: '触发带宽', type: 'number', default: 5, unit: '%', pctOf1: true, min: 0.5, help: '偏离目标超过这么多就调仓' },
    { key: 'MinTradeQuote', label: '最小交易额', type: 'number', default: 10, unit: 'USDT', min: 0 },
  ],
  spottrend: [
    { key: 'FastPeriod', label: '快线', type: 'number', default: 10, unit: '根', min: 2 },
    { key: 'SlowPeriod', label: '慢线', type: 'number', default: 30, unit: '根', min: 3 },
    { key: 'StopLossPct', label: '止损', type: 'number', default: 0, unit: '%', pctOf1: true, min: 0, help: '0 = 不设' },
    { key: 'TakeProfitPct', label: '止盈', type: 'number', default: 0, unit: '%', pctOf1: true, min: 0, help: '0 = 不设' },
  ],
  guardian: [
    { key: 'StopValue', label: '亏多少就自动平仓', type: 'number', default: 3, unit: '%', step: 0.5, pctOf1: true, min: 0.5, help: '亏到这个幅度就帮你平掉,保住本金。赚了之后它会自动把这条线往上抬,锁住利润。' },
    { key: 'BreakEvenPct', label: '赚多少就保本', type: 'number', default: 0, unit: '%', step: 0.5, pctOf1: true, min: 0, help: '0 = 不设。赚到这个幅度后,止损自动移到成本价,这单就不会亏了。' },
    { key: 'TPValue', label: '赚多少就落袋', type: 'number', default: 0, unit: '%', step: 0.5, pctOf1: true, min: 0, help: '0 = 不设,让利润继续跑(靠自动上移的止损收尾)' },
  ],
}

export function fieldsForStrategy(id: string): FieldDef[] {
  return STRATEGY_FIELDS[id] ?? []
}
