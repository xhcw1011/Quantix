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
  type: 'number' | 'select' | 'boolean'
  default: number | string | boolean
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
  macross: [
    { key: 'FastPeriod', label: '快线', type: 'number', default: 10, unit: '根', min: 2 },
    { key: 'SlowPeriod', label: '慢线', type: 'number', default: 30, unit: '根', min: 3 },
    { key: 'EnableShort', label: '双向交易(做多做空)', type: 'boolean', default: true, help: '关闭 = 只做多(单向持仓模式);开启后死叉会开空、金叉会开多(需要交易所账户是对冲模式,否则下单会被拒)。' },
    { key: 'StopLossPct', label: '止损', type: 'number', default: 3, unit: '%', pctOf1: true, min: 0, help: '0 = 不设' },
    { key: 'TakeProfitPct', label: '止盈', type: 'number', default: 0, unit: '%', pctOf1: true, min: 0, help: '0 = 不设' },
    { key: 'EntryOrderType', label: '开仓方式', type: 'select', default: 'limit',
      options: [{ value: 'market', label: '市价(立即成交)' }, { value: 'limit', label: '限价(省手续费,可能等不到)' }] },
    { key: 'EntryLimitOffsetPct', label: '限价偏移', type: 'number', default: 0.05, unit: '%', step: 0.01, pctOf1: true, min: 0,
      help: '挂单价比最新价更有利的偏移幅度(多单挂低、空单挂高),偏移越大越容易吃到 maker 费率,但也越难成交;只在开仓方式选限价时生效' },
    { key: 'EntryTimeoutBars', label: '限价超时', type: 'number', default: 3, unit: '根K线', min: 1,
      help: '挂这么多根K线还没成交,自动撤单改市价,不会一直空等错过信号;只在开仓方式选限价时生效' },
    { key: 'AsymmetricExit', label: '亏损时扛单不反手', type: 'boolean', default: true,
      help: '反向信号来了:如果这笔当前是盈利的才平仓反手;如果是亏损的就扛着不翻仓,改用下面的分批减仓+移动止盈来管理,避免震荡行情里每次交叉都吃一次反手亏损。3%止损仍然独立生效,不受这个开关影响。' },
    { key: 'MinProfitToClosePct', label: '反手最低盈利要求', type: 'number', default: 0.1, unit: '%', step: 0.01, pctOf1: true, min: 0,
      help: '反向信号来了,浮盈要超过这个幅度才平仓反手(不是只要大于0)——留出双边手续费的空间,避免账面刚好打平的单子等真正成交完反而倒亏手续费。默认0.1%,略高于实际约0.08%的双边手续费成本。' },
    { key: 'ReduceTriggerPct', label: '扛单确认亏损阈值', type: 'number', default: 1, unit: '%', step: 0.1, pctOf1: true, min: 0,
      help: '浮亏达到这个幅度且连续确认后,一次性减掉部分仓位止损,不是全平' },
    { key: 'ReduceConfirmBars', label: '确认根数', type: 'number', default: 2, unit: '根K线', min: 1,
      help: '浮亏要连续这么多根K线都达到阈值才触发减仓,避免单根K线抖动误触发' },
    { key: 'ReduceFrac', label: '减仓比例', type: 'number', default: 50, unit: '%', pctOf1: true, min: 1, max: 99,
      help: '触发确认亏损后,一次性减掉这么多比例的仓位' },
    { key: 'TrailActivatePct', label: '扛单浮盈启动移动止盈', type: 'number', default: 1, unit: '%', step: 0.5, pctOf1: true, min: 0,
      help: '浮盈达到这个幅度后开始跟踪峰值,回撤到下面的比例就全平锁利' },
    { key: 'TrailGivebackFrac', label: '回吐比例', type: 'number', default: 3, unit: '%', pctOf1: true, min: 1, max: 99,
      help: '从浮盈峰值回撤这么多比例就平仓离场' },
  ],
  guardian: [
    { key: 'StopValue', label: '亏多少就自动平仓', type: 'number', default: 3, unit: '%', step: 0.5, pctOf1: true, min: 0.5, help: '亏到这个幅度就帮你平掉,保住本金。赚了之后它会自动把这条线往上抬,锁住利润。' },
    { key: 'BreakEvenPct', label: '赚多少就保本', type: 'number', default: 0, unit: '%', step: 0.5, pctOf1: true, min: 0, help: '0 = 不设。赚到这个幅度后,止损自动移到成本价,这单就不会亏了。' },
    { key: 'TrailActivatePct', label: '赚多少就开始自动上移止损', type: 'number', default: 1, unit: '%', step: 0.5, pctOf1: true, min: 0, help: '赚到这个幅度,止损就开始跟着价格自动往上抬、锁住利润(只进不退)。0 = 不自动上移,止损固定。' },
    { key: 'PartialTPPct', label: '赚多少就先落袋一半', type: 'number', default: 0, unit: '%', step: 0.5, pctOf1: true, min: 0, help: '0 = 不设。赚到这个幅度先平掉一半锁利,剩下一半继续让止损守着跑。' },
    { key: 'TPValue', label: '赚多少就全平落袋', type: 'number', default: 0, unit: '%', step: 0.5, pctOf1: true, min: 0, help: '0 = 不设,让利润继续跑(靠自动上移的止损收尾)' },
  ],
}

export function fieldsForStrategy(id: string): FieldDef[] {
  return STRATEGY_FIELDS[id] ?? []
}
