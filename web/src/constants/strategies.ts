// Strategy metadata: the /strategies API returns only raw ids, so friendly names,
// plain-language descriptions, and spot-vs-futures classification live here.
// Only *supported* strategies are listed; anything not here is hidden from the UI.

export type MarketKind = 'spot' | 'futures'

export interface StrategyMeta {
  id: string
  name: string // friendly display name (shown to non-technical users)
  desc: string // one-line plain-language explanation
  market: MarketKind
  advanced?: boolean // futures set is demoted / "advanced"
}

export const STRATEGIES: StrategyMeta[] = [
  // ── 现货(主推:不需要方向 edge,靠纪律 + 波动 + 长期向上)──
  { id: 'dca', name: '定投', desc: '定期定额自动买入,长期囤币', market: 'spot' },
  { id: 'dipdca', name: '逢跌加码定投', desc: '定投升级:价格越低于均线,买得越多', market: 'spot' },
  { id: 'spotgrid', name: '现货网格', desc: '震荡里低买高卖,吃波动(长仓)', market: 'spot' },
  { id: 'rebalance', name: '再平衡', desc: '固定比例持仓,定期机械高抛低吸', market: 'spot' },
  { id: 'spottrend', name: '长仓趋势', desc: '均线金叉买入、死叉离场(不做空)', market: 'spot' },

  // ── 合约(降级 · 进阶用户)──
  { id: 'ai', name: '智能网格', desc: '震荡里低买高卖 + 趋势腿(合约)', market: 'futures', advanced: true },
  { id: 'macross', name: '趋势跟随', desc: '均线交叉,追涨杀跌(合约)', market: 'futures', advanced: true },
  { id: 'grid', name: '合约网格', desc: '经典网格,合约低买高卖', market: 'futures', advanced: true },
]

export function strategyMeta(id: string): StrategyMeta | undefined {
  return STRATEGIES.find((s) => s.id === id)
}

export function strategyLabel(id: string): string {
  const m = strategyMeta(id)
  return m ? m.name : id
}

export function strategiesForMarket(market: MarketKind): StrategyMeta[] {
  return STRATEGIES.filter((s) => s.market === market)
}

// A common-symbols list to power the searchable symbol picker (a fuller list than
// the old hardcoded four). Users can also type any valid symbol.
export const COMMON_SYMBOLS = [
  'BTCUSDT', 'ETHUSDT', 'BNBUSDT', 'SOLUSDT', 'XRPUSDT', 'ADAUSDT', 'DOGEUSDT',
  'AVAXUSDT', 'LINKUSDT', 'DOTUSDT', 'MATICUSDT', 'LTCUSDT', 'TRXUSDT', 'ATOMUSDT',
  'UNIUSDT', 'APTUSDT', 'ARBUSDT', 'OPUSDT', 'SUIUSDT', 'TONUSDT', 'NEARUSDT', 'FILUSDT',
]
