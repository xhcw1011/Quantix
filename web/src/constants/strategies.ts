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
  defaultInterval?: string // preferred check frequency; applied when the strategy is selected
}

export const STRATEGIES: StrategyMeta[] = [
  // ── 现货(主推:不需要方向 edge,靠纪律 + 波动 + 长期向上)──
  { id: 'dca', name: '定投', desc: '定期定额自动买入,长期囤币', market: 'spot' },
  { id: 'dipdca', name: '逢跌加码定投', desc: '定投升级:价格越低于均线,买得越多', market: 'spot' },
  { id: 'spotgrid', name: '现货网格', desc: '震荡里低买高卖,吃波动(长仓)', market: 'spot' },
  { id: 'rebalance', name: '再平衡', desc: '固定比例持仓,定期机械高抛低吸', market: 'spot' },
  { id: 'spottrend', name: '长仓趋势', desc: '均线金叉买入、死叉离场(不做空)', market: 'spot' },

  // ── 帮你守仓(不猜涨跌,只帮你守住已经开的单)──
  { id: 'guardian', name: '自动守仓', desc: '帮你盯着已开的单:亏到设定就自动平掉,赚了自动把止损往上抬锁住利润,有情况发消息提醒', market: 'futures', defaultInterval: '5m' },

  // ── 合约(降级 · 进阶用户)──
  { id: 'ai', name: '智能网格', desc: '震荡里低买高卖 + 趋势腿(合约)', market: 'futures', advanced: true },
  { id: 'macross', name: '趋势跟随', desc: '均线交叉,追涨杀跌(合约)', market: 'futures', advanced: true },
  { id: 'grid', name: '合约网格', desc: '经典网格,合约低买高卖', market: 'futures', advanced: true },
  { id: 'breakout', name: '突破跟随', desc: '短周期确认突破/跌破开仓,长周期通道离场,不轻易反手(合约)', market: 'futures', advanced: true, defaultInterval: '15m' },
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
// the old hardcoded four). Users can also type any valid symbol. All *USDT pairs,
// uppercase, deduplicated.
export const COMMON_SYMBOLS = [
  // Majors
  'BTCUSDT', 'ETHUSDT', 'BNBUSDT', 'SOLUSDT', 'XRPUSDT', 'ADAUSDT', 'DOGEUSDT',
  'TRXUSDT', 'TONUSDT', 'DOTUSDT', 'LTCUSDT', 'BCHUSDT', 'ETCUSDT', 'XLMUSDT',
  'AVAXUSDT', 'LINKUSDT', 'ATOMUSDT', 'UNIUSDT', 'MATICUSDT',
  // Layer-1 / Layer-2 / infra
  'NEARUSDT', 'APTUSDT', 'SUIUSDT', 'ARBUSDT', 'OPUSDT', 'INJUSDT', 'TIAUSDT',
  'SEIUSDT', 'RUNEUSDT', 'STXUSDT', 'IMXUSDT', 'FILUSDT', 'ICPUSDT', 'HBARUSDT',
  'ALGOUSDT', 'VETUSDT', 'EGLDUSDT', 'FTMUSDT', 'THETAUSDT', 'KAVAUSDT', 'ROSEUSDT',
  'FLOWUSDT', 'MINAUSDT', 'ZILUSDT', 'ONEUSDT', 'QNTUSDT', 'XTZUSDT', 'EOSUSDT',
  'NEOUSDT', 'IOTAUSDT', 'KSMUSDT', 'CFXUSDT', 'KASUSDT',
  // DeFi
  'LDOUSDT', 'AAVEUSDT', 'MKRUSDT', 'CRVUSDT', 'DYDXUSDT', 'GMXUSDT', 'PENDLEUSDT',
  'COMPUSDT', 'SNXUSDT', 'SUSHIUSDT', '1INCHUSDT', 'CAKEUSDT', 'ENSUSDT', 'GRTUSDT',
  // AI / new narratives
  'WLDUSDT', 'PYTHUSDT', 'JUPUSDT', 'FETUSDT', 'AGIXUSDT', 'RNDRUSDT', 'ARKMUSDT',
  'ORDIUSDT', 'JTOUSDT', 'STRKUSDT', 'DYMUSDT', 'MANTAUSDT', 'WUSDT',
  // Meme
  'PEPEUSDT', 'WIFUSDT', 'BONKUSDT', 'FLOKIUSDT', 'SHIBUSDT',
  // Gaming / metaverse / NFT
  'GALAUSDT', 'SANDUSDT', 'MANAUSDT', 'AXSUSDT', 'CHZUSDT', 'APEUSDT', 'ENJUSDT',
  'GMTUSDT', 'MAGICUSDT',
]
