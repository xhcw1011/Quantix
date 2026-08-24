import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { getPositions, getTicker, listCredentials, listEngines, listStrategies, listStrategyPresets, startEngine, stopEngineById } from '../api/trading'
import { useTradeSocket } from '../hooks/useTradeSocket'
import { COMMON_SYMBOLS, strategiesForMarket, strategyMeta } from '../constants/strategies'
import type { MarketKind } from '../constants/strategies'
import { fieldsForStrategy } from '../constants/strategyFields'
import SymbolPicker from '../components/SymbolPicker'
import NumberInput from '../components/NumberInput'
import Toggle from '../components/Toggle'
import LeverageSlider from '../components/LeverageSlider'
import { type PositionView } from '../components/PositionRows'
import StoppedEnginesList from '../components/StoppedEnginesList'
import RunningEnginesList from '../components/RunningEnginesList'
import { INPUT_CLASS } from '../lib/inputStyles'
import { useConfirm } from '../hooks/useConfirm'

interface Preset {
  name: string
  description: string
  params: Record<string, any>
}

export interface Credential {
  id: number
  exchange: string
  label: string
  market_type: string // "spot" | "swap" | "futures"
  testnet: boolean
  demo: boolean
}

export interface EngineInfo {
  engine_id: string
  credential_id: number
  strategy_id: string
  symbol: string
  interval: string
  mode: string   // "live" | "paper"
  leverage?: number
  running: boolean
  started_at: string
  error?: string
}

export interface EnginePositions {
  engine_id: string
  last_price: number
  positions: PositionView[]
}

const intervals = ['1m', '5m', '15m', '1h', '4h', '1d']

const initialForm = {
  credential_id: 0,
  strategy_id: 'guardian',
  symbol: 'BTCUSDT',
  interval: '1h',
  mode: 'live' as 'live' | 'paper',
  leverage: 1,
  paper: {
    initial_capital: 10000,
    fee_rate: 0.001,
    slippage: 0.0005,
  },
  risk: {
    max_position_pct: 0.1,
    max_drawdown_pct: 0.15,
    max_single_loss_pct: 0.02,
  },
}


export default function Engine() {
  const navigate = useNavigate()
  const confirm = useConfirm()
  const [engines, setEngines] = useState<EngineInfo[]>([])
  const [creds, setCreds] = useState<Credential[]>([])
  const [strategies, setStrategies] = useState<string[]>(['macross', 'grid', 'meanreversion', 'mlstrat'])
  // 2026-08-12: 默认改成合约——用户实际常用的是合约,现货仍完全可选,只是不再预选,
  // 避免"以为选的是合约,结果表单还停在现货默认值"这种误配置。
  const [market, setMarket] = useState<MarketKind>('futures')
  const [form, setForm] = useState(initialForm)
  const [showForm, setShowForm] = useState(false)
  const [showRisk, setShowRisk] = useState(false)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [stoppingId, setStoppingId] = useState<string | null>(null)
  const [presets, setPresets] = useState<Preset[]>([])
  const [selectedPresetIdx, setSelectedPresetIdx] = useState<number>(-1)
  const [extraParams, setExtraParams] = useState<string>('')  // JSON textarea
  const [stratParams, setStratParams] = useState<Record<string, number | string | boolean>>({})
  // 交易对最新价:表单打开且选了交易对时展示,null = 加载中/取价失败
  const [symbolPrice, setSymbolPrice] = useState<string | null>(null)
  const [symbolPriceNum, setSymbolPriceNum] = useState<number | null>(null) // 原始数值,用于盈亏估算
  // 自动守仓 "顺便帮我开仓" — off by default = 守护已有仓位.
  const [guardianEntry, setGuardianEntry] = useState<{
    enabled: boolean; side: 'long' | 'short'; qty: number
    orderType: 'market' | 'limit'; limitPrice: number
  }>({
    enabled: false,
    side: 'long',
    qty: 0,
    orderType: 'market',
    limitPrice: 0,
  })
  // 自动守仓参数按 U 还是按 % 输入(默认按 U — U 本位用户想直接看会亏赚多少 U)。
  const [guardianUMode, setGuardianUMode] = useState<boolean>(true)
  // 止盈单独支持按绝对价格设置(后端 TPMode="price" 早就支持,只是之前没接到 UI 上)。
  // 只影响 TPValue 这一个字段,跟其它字段的 U/% 切换是独立的。
  const [guardianTPPriceMode, setGuardianTPPriceMode] = useState<boolean>(false)
  // 守护"账户已有持仓"(没开顺便帮我开仓)时,用于估算盈亏的持仓数量;仅前端估算,不发后端。
  const [guardianAdoptQty, setGuardianAdoptQty] = useState<number>(0)
  // 开仓"数量"按哪种单位填(默认名义 U:开多大的仓,与杠杆无关)。canonical 仍是币数量。
  const [guardianQtyMode, setGuardianQtyMode] = useState<'notional' | 'margin' | 'coin'>('notional')

  // Show ALL of the user's accounts in the credential picker — one exchange API
  // key usually works for both spot and futures, so we don't hard-filter by the
  // credential's market_type (it's shown in the label; the exchange enforces
  // actual permissions). Strategies are still filtered by the market tab.
  const filteredCreds = creds
  const availableStrategies = strategiesForMarket(market).filter((s) => strategies.includes(s.id))

  // Leverage is a futures-only concept.
  const showLeverage = market === 'futures' && form.mode === 'live'

  const handleMarketChange = (newMarket: MarketKind) => {
    setMarket(newMarket)
    const marketStrategies = strategiesForMarket(newMarket).filter((s) => strategies.includes(s.id))
    setForm((f) => ({
      ...f,
      // Keep the currently selected account (all accounts are available on both
      // tabs); only switch the strategy to one valid for the new market.
      credential_id: f.credential_id || creds[0]?.id || 0,
      strategy_id: marketStrategies[0]?.id ?? f.strategy_id,
    }))
  }

  // Stable references (useCallback, no reactive deps) so RunningEngineCard's
  // memoization actually works — an inline/recreated-every-render callback
  // prop would defeat it, forcing every card to re-render on every poll tick
  // regardless of whether that specific engine's own data changed.
  const loadEngines = useCallback(() => {
    listEngines()
      .then((r) => setEngines(r.data || []))
      .catch(() => {})
  }, [])

  // 持仓数据(按引擎分组),内嵌进下面运行中引擎卡片,替代原来独立的"持仓"页面。
  const [positionsByEngine, setPositionsByEngine] = useState<Record<string, EnginePositions>>({})
  const loadPositions = useCallback(() => {
    getPositions()
      .then((r) => {
        const byEngine: Record<string, EnginePositions> = {}
        for (const eng of (r.data.positions || []) as EnginePositions[]) {
          byEngine[eng.engine_id] = eng
        }
        setPositionsByEngine(byEngine)
      })
      .catch(() => {})
  }, [])

  useEffect(() => {
    listCredentials().then((r) => {
      const c: Credential[] = r.data || []
      setCreds(c)
      // Default to first non-spot (futures/swap) credential, matching the market
      // tab's default of 合约; fall back to first of any type.
      const preferred = c.find((cr) => cr.market_type !== 'spot') ?? c[0]
      if (preferred) setForm((f) => ({ ...f, credential_id: preferred.id }))
    })
    listStrategies().then((r) => {
      if (Array.isArray(r.data) && r.data.length > 0) setStrategies(r.data)
    }).catch(() => {})
    loadEngines()
    loadPositions()
    const t = setInterval(loadEngines, 10000)
    const pt = setInterval(loadPositions, 5000)
    return () => { clearInterval(t); clearInterval(pt) }
  }, [loadEngines, loadPositions])

  // Refresh positions promptly on any fill (sizes/PnL change immediately after a fill).
  useTradeSocket((msg: any) => {
    if (msg?.type === 'fill') loadPositions()
  })

  // Refetch presets and reset dynamic params whenever strategy_id changes.
  useEffect(() => {
    setSelectedPresetIdx(-1)
    setExtraParams('')
    setGuardianEntry({ enabled: false, side: 'long', qty: 0, orderType: 'market', limitPrice: 0 })
    // Apply the strategy's preferred check frequency (e.g. guardian → 5m; a risk
    // tool wants a responsive interval, not the global 1h default).
    const di = strategyMeta(form.strategy_id)?.defaultInterval
    if (di) setForm((f) => ({ ...f, interval: di }))
    setStratParams(
      Object.fromEntries(fieldsForStrategy(form.strategy_id).map((f) => [f.key, f.default]))
    )
    listStrategyPresets(form.strategy_id)
      .then((r) => setPresets(Array.isArray(r.data) ? r.data : []))
      .catch(() => setPresets([]))
  }, [form.strategy_id])

  // 取交易对最新价:表单打开且选了交易对时拉取,换交易对时重取。
  // 用 cancelled 标记忽略过期响应(避免快速切换时旧价格覆盖新价格)。
  useEffect(() => {
    if (!showForm || !form.symbol) {
      setSymbolPrice(null)
      setSymbolPriceNum(null)
      return
    }
    let cancelled = false
    setSymbolPrice(null) // 先显示"—",取到再更新
    setSymbolPriceNum(null)
    getTicker(form.symbol)
      .then((r) => {
        if (cancelled) return
        const n = Number(r.data?.price)
        if (!isFinite(n)) {
          setSymbolPrice(null)
          setSymbolPriceNum(null)
          return
        }
        const digits = n >= 1 ? 2 : 6
        setSymbolPrice(n.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: digits }))
        setSymbolPriceNum(n)
      })
      .catch(() => {
        if (!cancelled) { setSymbolPrice(null); setSymbolPriceNum(null) }
      })
    return () => { cancelled = true }
  }, [form.symbol, showForm])

  const handleStart = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    setLoading(true)
    try {
      const payload: any = {
        credential_id: form.credential_id,
        strategy_id: form.strategy_id,
        symbol: form.symbol,
        interval: form.interval,
        mode: form.mode,
        // What we're trading now (from the market tab) — the backend uses this,
        // not the credential's stored label, since one key does both.
        market_type: market === 'futures' ? 'futures' : 'spot',
        risk: form.risk,
      }
      if (form.mode === 'live') {
        payload.confirm_live = true
      }
      if (form.mode === 'paper') {
        payload.paper = form.paper
      }
      // Guardian adopt-only (顺便帮我开仓 off) never opens a new position, so it
      // doesn't need leverage — sending a stale value left over from switching
      // strategies/toggles could get rejected by the exchange for no reason
      // (e.g. a sub-account's leverage cap) even though nothing here needed it
      // (backend mirrors this: internal/api/manager.go's guardianAdoptOnly, 2026-08-06).
      const isGuardianAdoptOnly = form.strategy_id === 'guardian' && !guardianEntry.enabled
      if (showLeverage && form.leverage > 1 && !isGuardianAdoptOnly) {
        payload.leverage = form.leverage
      }
      // Strategy-specific params: merge preset → extra-params textarea → form fields.
      // Later writes win — textarea overrides preset, form fields override both.
      const params: Record<string, any> = {}
      if (selectedPresetIdx >= 0 && presets[selectedPresetIdx]) {
        Object.assign(params, presets[selectedPresetIdx].params)
      }
      if (extraParams.trim() !== '') {
        try {
          const parsed = JSON.parse(extraParams)
          if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
            Object.assign(params, parsed)
          } else {
            throw new Error('extra params must be a JSON object')
          }
        } catch (e: any) {
          setError(`Invalid extra params JSON: ${e.message}`)
          setLoading(false)
          return
        }
      }
      // Dynamic strategy fields — highest priority, override preset + textarea.
      const tpIsPrice = form.strategy_id === 'guardian' && guardianTPPriceMode
      for (const f of fieldsForStrategy(form.strategy_id)) {
        let v: any = stratParams[f.key]
        if (v === '' || v === undefined) continue
        const isTPPrice = tpIsPrice && f.key === 'TPValue'
        if (f.type === 'number') { v = Number(v); if (f.pctOf1 && !isTPPrice) v = v / 100 }
        params[f.key] = v
      }
      if (tpIsPrice && params.TPValue > 0) {
        params.TPMode = 'price'
      }
      // 自动守仓 — 顺便帮我开仓: place + protect. Otherwise adopt the existing position.
      if (form.strategy_id === 'guardian' && guardianEntry.enabled) {
        params.PlaceEntry = true
        params.Side = guardianEntry.side
        params.Qty = guardianEntry.qty
        if (guardianEntry.orderType === 'limit' && guardianEntry.limitPrice > 0) {
          params.EntryType = 'limit'
          params.EntryPrice = guardianEntry.limitPrice
        }
      }
      if (Object.keys(params).length > 0) {
        payload.params = params
      }
      await startEngine(payload)
      setShowForm(false)
      loadEngines()
    } catch (err: any) {
      setError(err.response?.data?.error || 'Failed to start engine')
    } finally {
      setLoading(false)
    }
  }

  // Mirrors positionsByEngine without being a reactive dependency, so
  // handleStop stays a stable reference (see loadEngines/loadPositions comment)
  // while still reading the LATEST positions when actually invoked.
  const positionsByEngineRef = useRef(positionsByEngine)
  useEffect(() => { positionsByEngineRef.current = positionsByEngine }, [positionsByEngine])

  const handleStop = useCallback(async (engineId: string) => {
    const hasPosition = (positionsByEngineRef.current[engineId]?.positions.length ?? 0) > 0
    const ok = await confirm({
      title: `停止「${engineId}」？`,
      message: hasPosition
        ? '⚠️ 停止会撤销该引擎的全部挂单，并按市价平掉它当前的持仓。此操作会真实成交、不可撤销。'
        : '会取消所有未成交挂单。',
      confirmLabel: '停止',
    })
    if (!ok) return
    setStoppingId(engineId)
    try {
      await stopEngineById(engineId)
      loadEngines()
    } catch (err: any) {
      setError(err.response?.data?.error || 'Failed to stop engine')
    } finally {
      setStoppingId(null)
    }
  }, [loadEngines, confirm])

  const handleNavigate = useCallback((engineId: string) => navigate(`/engine/${engineId}`), [navigate])

  const runningEngines = engines.filter((e) => e.running)
  // Stopped engines accumulate forever (every past forward-test, every restart) —
  // show only the most recent by default so old dead engines don't bury the
  // ones that actually matter right now.
  const allStoppedEngines = engines
    .filter((e) => !e.running)
    .sort((a, b) => new Date(b.started_at).getTime() - new Date(a.started_at).getTime())

  // 资金属性:引擎动的是真钱还是模拟钱(mode=live 只是"实时",配 demo/testnet 账户仍是假钱)。
  // 表单里的"是不是真钱"提示牌还要用 credById,RunningEnginesList 自己的那份是独立算的。
  const credById = Object.fromEntries(creds.map((c) => [c.id, c]))
  const fields = fieldsForStrategy(form.strategy_id)

  // ── 自动守仓:盈亏估算(U 本位)──────────────────────────────────────────
  // 名义价值 = 数量 × 当前价。开仓时用表单数量,守护已有仓用用户填的预估数量。
  // U 线性合约的盈亏 = 幅度% × 名义价值(与杠杆无关,方向只决定正负)。
  const isGuardianForm = form.strategy_id === 'guardian'
  const guardianQty = isGuardianForm
    ? (guardianEntry.enabled ? guardianEntry.qty : guardianAdoptQty)
    : 0
  const guardianNotional =
    symbolPriceNum && guardianQty > 0 ? symbolPriceNum * guardianQty : 0
  const canEstimateU = guardianNotional > 0
  // 规范存储始终是"百分比数字"(如 3 = 3%);提交时 /100。U 只是显示/输入的另一种视图。
  const pctToU = (pct: number) => (canEstimateU ? (pct / 100) * guardianNotional : null)
  const uToPct = (u: number) => (canEstimateU ? (u / guardianNotional) * 100 : 0)
  const round2 = (x: number) => Math.round(x * 100) / 100
  // 止损档=亏,其余(保本/落袋/全平)=赚。
  const guardianFieldIsLoss = (key: string) => key === 'StopValue'

  // 开仓"数量"单位换算:canonical = 币数量(BTC)。名义 U = 币×价(与杠杆无关);
  // 保证金 U = 名义/杠杆。价格未知时退回按币数量填。
  const guardianLev = form.leverage > 0 ? form.leverage : 1
  const qtyToDisplay = (qty: number): number => {
    if (!symbolPriceNum || guardianQtyMode === 'coin') return qty
    if (guardianQtyMode === 'notional') return round2(qty * symbolPriceNum)
    return round2((qty * symbolPriceNum) / guardianLev) // margin
  }
  const displayToQty = (v: number): number => {
    if (!symbolPriceNum || guardianQtyMode === 'coin') return v
    if (guardianQtyMode === 'notional') return v / symbolPriceNum
    return (v * guardianLev) / symbolPriceNum // margin
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold">策略引擎</h1>
        <button
          onClick={() => { setShowForm(!showForm); setError('') }}
          className="px-4 py-2 bg-green-600 hover:bg-green-700 rounded text-sm font-semibold"
        >
          {showForm ? '✕ 取消' : '+ 新建策略'}
        </button>
      </div>

      {/* New engine form */}
      {showForm && (
        <div className="bg-slate-800 rounded-xl p-5">
          <h2 className="text-sm font-semibold text-slate-300 mb-4">启动新策略</h2>

          {/* Market tabs: 合约 (default, matches actual usage) vs 现货 (still fully available) */}
          <div className="flex gap-2 mb-5">
            <button
              type="button"
              onClick={() => handleMarketChange('spot')}
              className={`px-4 py-1.5 rounded text-sm font-semibold border transition-colors ${
                market === 'spot'
                  ? 'bg-blue-600 border-blue-600 text-white'
                  : 'bg-transparent border-slate-600 text-slate-400 hover:border-slate-400'
              }`}
            >
              现货
            </button>
            <button
              type="button"
              onClick={() => handleMarketChange('futures')}
              className={`px-4 py-1.5 rounded text-sm font-semibold border transition-colors ${
                market === 'futures'
                  ? 'bg-slate-500 border-slate-500 text-white'
                  : 'bg-transparent border-slate-700 text-slate-500 hover:border-slate-500'
              }`}
            >
              合约 · 进阶
            </button>
          </div>

          {creds.length === 0 ? (
            <p className="text-slate-400 text-sm">
              还没有交易账户,先去{' '}
              <a href="/credentials" className="text-blue-400 hover:underline">「交易所账户」页添加</a>。
            </p>
          ) : (
            <form onSubmit={handleStart} className="space-y-4">
              {/* 是不是真钱由所选账户决定 —— demo 账户=模拟盘不涉真钱,正式账户=真钱 */}
              {(() => {
                const c = credById[form.credential_id]
                const isDemo = c ? (c.testnet || c.demo) : false
                return (
                  <p className="text-xs text-slate-500">
                    {isDemo
                      ? '📋 模拟盘账户 · 不涉及真钱,放心测试'
                      : '⚡ 真钱账户 · 真钱下单(用真钱前需先在「设置」里启用实盘交易)'}
                  </p>
                )
              })()}

              <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                <div>
                  <label className="block text-xs text-slate-400 mb-1">交易账户</label>
                  <select
                    value={form.credential_id}
                    onChange={(e) => setForm({ ...form, credential_id: +e.target.value })}
                    className={INPUT_CLASS}
                  >
                    {filteredCreds.length === 0 && <option value={0}>(还没有账户,先去「交易所账户」页添加)</option>}
                    {filteredCreds.map((c) => {
                      const mkt = c.market_type === 'spot' ? '现货' : c.market_type === 'swap' ? '永续' : '合约'
                      const kind = c.demo ? '模拟' : c.testnet ? '测试网' : '正式'
                      return (
                        <option key={c.id} value={c.id}>
                          {c.label}({c.exchange} · {mkt} · {kind})
                        </option>
                      )
                    })}
                  </select>
                </div>
                <div>
                  <label className="block text-xs text-slate-400 mb-1">策略</label>
                  {availableStrategies.length === 0 ? (
                    <p className="text-xs text-slate-500 py-1.5">当前市场无可用策略</p>
                  ) : (
                    <select
                      value={form.strategy_id}
                      onChange={(e) => setForm({ ...form, strategy_id: e.target.value })}
                      className={INPUT_CLASS}
                    >
                      {availableStrategies.map((s) => (
                        <option key={s.id} value={s.id}>{s.name}</option>
                      ))}
                    </select>
                  )}
                  {strategyMeta(form.strategy_id) && (
                    <p className="text-xs text-slate-500 mt-1">{strategyMeta(form.strategy_id)!.desc}</p>
                  )}
                </div>
                <div>
                  <label className="block text-xs text-slate-400 mb-1">交易对</label>
                  <SymbolPicker
                    value={form.symbol}
                    onChange={(v) => setForm({ ...form, symbol: v })}
                    options={COMMON_SYMBOLS}
                    placeholder="搜索或输入交易对,如 BTCUSDT"
                  />
                  <p className="text-xs text-slate-400 mt-1">
                    {symbolPrice ? `当前价 ≈ ${symbolPrice}` : '当前价 —'}
                  </p>
                </div>
                <div>
                  <label className="block text-xs text-slate-400 mb-1">检查频率(K线)</label>
                  <select
                    value={form.interval}
                    onChange={(e) => setForm({ ...form, interval: e.target.value })}
                    className={INPUT_CLASS}
                  >
                    {intervals.map((i) => <option key={i} value={i}>{i}</option>)}
                  </select>
                </div>
              </div>

              {/* Strategy presets + advanced JSON params — only for futures strategies without a field schema */}
              {fields.length === 0 && (presets.length > 0 || form.strategy_id === 'ai' || form.strategy_id === 'composite') && (
                <div className="bg-slate-900/40 border border-slate-700 rounded-lg p-3 space-y-2">
                  <div className="flex items-center justify-between">
                    <span className="text-xs font-semibold text-slate-300">Preset</span>
                    <span className="text-[10px] text-slate-500">params get merged into the start request</span>
                  </div>
                  {presets.length === 0 ? (
                    <p className="text-xs text-slate-500">No presets registered for "{form.strategy_id}" — use the JSON box below.</p>
                  ) : (
                    <div className="grid grid-cols-1 md:grid-cols-3 gap-2">
                      {presets.map((p, i) => (
                        <button
                          key={i}
                          type="button"
                          onClick={() => setSelectedPresetIdx(selectedPresetIdx === i ? -1 : i)}
                          className={`text-left px-3 py-2 rounded border text-xs transition-colors ${
                            selectedPresetIdx === i
                              ? 'border-blue-500 bg-blue-900/30 text-blue-200'
                              : 'border-slate-700 bg-slate-800 text-slate-300 hover:border-slate-500'
                          }`}
                          title={JSON.stringify(p.params, null, 2)}
                        >
                          <div className="font-semibold">{p.name}</div>
                          <div className="text-[11px] text-slate-400 mt-0.5">{p.description}</div>
                        </button>
                      ))}
                    </div>
                  )}
                  <details>
                    <summary className="text-xs text-slate-400 cursor-pointer">Advanced — extra params (JSON)</summary>
                    <textarea
                      value={extraParams}
                      onChange={(e) => setExtraParams(e.target.value)}
                      placeholder='{"HedgeMode": true, "MaxConsecLoss": 3}'
                      rows={4}
                      className="mt-2 w-full bg-slate-800 border border-slate-700 rounded p-2 text-xs font-mono text-slate-300 focus:outline-none focus:ring-1 focus:ring-slate-500"
                    />
                    <p className="text-[10px] text-slate-500 mt-1">Merged on top of preset. Form fields below (short toggle, SL/TP) override both.</p>
                  </details>
                </div>
              )}

              {/* Dynamic strategy parameter fields — non-guardian strategies (generic) */}
              {fields.length > 0 && !isGuardianForm && (
                <div className="bg-slate-900/40 border border-slate-700 rounded-lg p-3 space-y-3">
                  <span className="text-xs font-semibold text-slate-300">参数设置</span>
                  <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
                    {fields.map((f) => (
                      <div key={f.key}>
                        {f.type === 'boolean' ? (
                          <div className="flex items-center justify-between">
                            <label className="text-xs text-slate-400">{f.label}</label>
                            <Toggle
                              checked={(stratParams[f.key] ?? f.default) as boolean}
                              onChange={(v) => setStratParams((p) => ({ ...p, [f.key]: v }))}
                            />
                          </div>
                        ) : (
                          <>
                            <label className="block text-xs text-slate-400 mb-1">
                              {f.label}{f.unit ? ` (${f.unit})` : ''}
                            </label>
                            {f.type === 'select' ? (
                              <select
                                value={String(stratParams[f.key] ?? f.default)}
                                onChange={(e) => setStratParams((p) => ({ ...p, [f.key]: e.target.value }))}
                                className={INPUT_CLASS}
                              >
                                {f.options?.map((o) => (
                                  <option key={o.value} value={o.value}>{o.label}</option>
                                ))}
                              </select>
                            ) : (
                              <NumberInput
                                value={(stratParams[f.key] ?? f.default) as number | ''}
                                min={f.min}
                                max={f.max}
                                onChange={(v) => setStratParams((p) => ({ ...p, [f.key]: v }))}
                                className={INPUT_CLASS}
                              />
                            )}
                          </>
                        )}
                        {f.help && (
                          <p className="text-[10px] text-slate-500 mt-0.5">{f.help}</p>
                        )}
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* 自动守仓 — 顺便帮我开仓 (放最上面:先定开不开仓/方向/数量/杠杆,再设止损止盈) */}
              {isGuardianForm && (
                <div className="bg-emerald-900/20 border border-emerald-700/40 rounded-lg p-3 space-y-3">
                  <div className="flex items-center justify-between">
                    <div>
                      <span className="text-xs font-semibold text-slate-300">顺便帮我开仓</span>
                      <p className="text-xs text-slate-500">开启后先按下面的方向和数量下单,再自动守护;不开就守护你账户里已有的仓位</p>
                    </div>
                    <Toggle
                      checked={guardianEntry.enabled}
                      onChange={(v) => setGuardianEntry((g) => ({ ...g, enabled: v }))}
                    />
                  </div>
                  {guardianEntry.enabled && (
                    <>
                      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                        <div>
                          <label className="block text-xs text-slate-400 mb-1">方向</label>
                          <select
                            value={guardianEntry.side}
                            onChange={(e) => setGuardianEntry((g) => ({ ...g, side: e.target.value as 'long' | 'short' }))}
                            className={INPUT_CLASS}
                          >
                            <option value="long">做多</option>
                            <option value="short">做空</option>
                          </select>
                        </div>
                        <div>
                          <div className="flex items-center justify-between mb-1">
                            <label className="text-xs text-slate-400">数量</label>
                            {symbolPriceNum && (
                              <div className="inline-flex rounded-md border border-slate-600 overflow-hidden text-xs">
                                {([['notional', '名义U'], ['margin', '保证金U'], ['coin', '币']] as const).map(([m, lbl]) => (
                                  <button
                                    key={m}
                                    type="button"
                                    onClick={() => setGuardianQtyMode(m)}
                                    className={`px-2 py-1 ${guardianQtyMode === m ? 'bg-emerald-600 text-white' : 'bg-slate-700 text-slate-300'}`}
                                  >{lbl}</button>
                                ))}
                              </div>
                            )}
                          </div>
                          <NumberInput
                            min={0}
                            value={guardianEntry.qty > 0 ? qtyToDisplay(guardianEntry.qty) : ''}
                            onChange={(v) => {
                              const n = v === '' ? 0 : v
                              setGuardianEntry((g) => ({ ...g, qty: n <= 0 ? 0 : displayToQty(n) }))
                            }}
                            className={INPUT_CLASS}
                            placeholder={
                              guardianQtyMode === 'coin' || !symbolPriceNum
                                ? '币的个数,例如 0.005'
                                : guardianQtyMode === 'margin' ? '保证金 U,例如 100' : '名义 U,例如 1000'
                            }
                          />
                          {symbolPriceNum && guardianEntry.qty > 0 && (
                            <p className="text-[10px] text-slate-400 mt-0.5">
                              ≈ {guardianEntry.qty.toFixed(6)} 币 · 名义 {(guardianEntry.qty * symbolPriceNum).toLocaleString('en-US', { maximumFractionDigits: 0 })} U · 占保证金 {(guardianEntry.qty * symbolPriceNum / guardianLev).toLocaleString('en-US', { maximumFractionDigits: 0 })} U({guardianLev}x)
                            </p>
                          )}
                        </div>
                      </div>
                      {/* 开仓方式 + 价格:整齐两列。平仓一律市价。 */}
                      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                        <div>
                          <label className="block text-xs text-slate-400 mb-1">开仓方式</label>
                          <div className="inline-flex rounded-md border border-slate-600 overflow-hidden text-xs">
                            {([['market', '市价'], ['limit', '限价']] as const).map(([m, lbl]) => (
                              <button
                                key={m}
                                type="button"
                                onClick={() => setGuardianEntry((g) => ({
                                  ...g,
                                  orderType: m,
                                  limitPrice: m === 'limit' && g.limitPrice <= 0 && symbolPriceNum ? symbolPriceNum : g.limitPrice,
                                }))}
                                className={`px-3 py-1.5 ${guardianEntry.orderType === m ? 'bg-emerald-600 text-white' : 'bg-slate-700 text-slate-300'}`}
                              >{lbl}</button>
                            ))}
                          </div>
                          <p className="text-[10px] text-slate-500 mt-0.5">
                            {guardianEntry.orderType === 'market' ? '按当前价立即成交' : '挂单等成交,不到价就一直等'}
                          </p>
                        </div>
                        <div>
                          <label className="block text-xs text-slate-400 mb-1">{guardianEntry.orderType === 'limit' ? '挂单价' : '成交价'}</label>
                          {guardianEntry.orderType === 'limit' ? (
                            <NumberInput
                              min={0}
                              value={guardianEntry.limitPrice || ''}
                              onChange={(v) => setGuardianEntry((g) => ({ ...g, limitPrice: v === '' ? 0 : v }))}
                              className={INPUT_CLASS}
                              placeholder={`例如 ${symbolPrice ?? '64000'}`}
                            />
                          ) : (
                            <div className="w-full bg-slate-800/40 border border-slate-700 rounded px-2 py-1.5 text-sm text-slate-400">
                              当前价 ≈ {symbolPrice ?? '—'}
                            </div>
                          )}
                        </div>
                      </div>
                      {/* 杠杆:只在开新仓时才相关,所以跟着"顺便帮我开仓"一起 */}
                      {showLeverage && (
                        <LeverageSlider
                          value={form.leverage}
                          onChange={(v) => setForm({ ...form, leverage: v })}
                          hint="(只影响新开仓保证金,守护已有仓不用设)"
                        />
                      )}
                    </>
                  )}
                  {!guardianEntry.enabled && (
                    <div>
                      <label className="block text-xs text-slate-400 mb-1">持仓数量(用于估算盈亏,可留空)</label>
                      <NumberInput
                        min={0}
                        value={guardianAdoptQty || ''}
                        onChange={(v) => setGuardianAdoptQty(v === '' ? 0 : v)}
                        className={INPUT_CLASS}
                        placeholder="填上你账户里这个仓位的数量,下面各档就会显示 ≈ 多少 U"
                      />
                    </div>
                  )}
                </div>
              )}

              {/* 自动守仓 参数 — 支持按 U / 按 % 切换,实时显示预计盈亏(U 本位) */}
              {isGuardianForm && fields.length > 0 && (
                <div className="bg-slate-900/40 border border-slate-700 rounded-lg p-3 space-y-3">
                  <div className="flex items-center justify-between">
                    <span className="text-xs font-semibold text-slate-300">参数设置</span>
                    <div className="inline-flex rounded-md border border-slate-600 overflow-hidden text-xs">
                      <button
                        type="button"
                        onClick={() => setGuardianUMode(true)}
                        className={`px-2.5 py-1 ${guardianUMode ? 'bg-emerald-600 text-white' : 'bg-slate-700 text-slate-300'}`}
                      >按 U</button>
                      <button
                        type="button"
                        onClick={() => setGuardianUMode(false)}
                        className={`px-2.5 py-1 ${!guardianUMode ? 'bg-emerald-600 text-white' : 'bg-slate-700 text-slate-300'}`}
                      >按 %</button>
                    </div>
                  </div>

                  {guardianUMode && !canEstimateU && (
                    <p className="text-[10px] text-amber-400/80">
                      {guardianEntry.enabled ? '填好上面的「数量」' : '填上面的「持仓数量」'}后才能按 U 估算,暂时按 % 显示。
                    </p>
                  )}

                  <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                    {fields.map((f) => {
                      if (f.key === 'TPValue' && guardianTPPriceMode) {
                        const side = guardianEntry.enabled ? guardianEntry.side : null
                        const dirHint = side === 'long' ? '(做多,止盈价要比开仓价高)' : side === 'short' ? '(做空,止盈价要比开仓价低)' : ''
                        return (
                          <div key={f.key}>
                            <div className="flex items-center justify-between mb-1">
                              <label className="text-xs text-slate-400">{f.label} (价格)</label>
                              <button
                                type="button"
                                onClick={() => setGuardianTPPriceMode(false)}
                                className="text-[10px] px-1.5 py-0.5 rounded bg-slate-700 hover:bg-slate-600 text-slate-300"
                              >切回按 %</button>
                            </div>
                            <NumberInput
                              value={stratParams[f.key] === '' || stratParams[f.key] == null ? '' : Number(stratParams[f.key])}
                              min={0}
                              onChange={(v) => setStratParams((p) => ({ ...p, [f.key]: v }))}
                              className={INPUT_CLASS}
                              placeholder={symbolPriceNum ? `例如 ${symbolPriceNum}` : '目标平仓价'}
                            />
                            <p className="text-[10px] text-slate-500 mt-0.5">
                              价格到这个数就全平落袋{dirHint ? ' ' + dirHint : ''}
                            </p>
                          </div>
                        )
                      }
                      const raw = stratParams[f.key]
                      const pct = Number(raw === '' || raw == null ? f.default : raw) || 0
                      const useU = guardianUMode && canEstimateU
                      const u = pctToU(pct)
                      const uStr = u != null ? u.toLocaleString('en-US', { maximumFractionDigits: u >= 100 ? 0 : 2 }) : null
                      const verb = guardianFieldIsLoss(f.key) ? '约亏' : '约赚'
                      const displayVal = raw === '' ? '' : (useU ? round2(u ?? 0) : pct)
                      return (
                        <div key={f.key}>
                          <div className="flex items-center justify-between mb-1">
                            <label className="text-xs text-slate-400">
                              {f.label}{useU ? ' (U)' : ' (%)'}
                            </label>
                            {f.key === 'TPValue' && (
                              <button
                                type="button"
                                onClick={() => setGuardianTPPriceMode(true)}
                                className="text-[10px] px-1.5 py-0.5 rounded bg-slate-700 hover:bg-slate-600 text-slate-300"
                              >改按价格</button>
                            )}
                          </div>
                          <NumberInput
                            value={displayVal}
                            min={0}
                            onChange={(v) => {
                              setStratParams((p) => ({
                                ...p,
                                [f.key]: v === '' ? '' : (useU ? uToPct(v) : v),
                              }))
                            }}
                            className={INPUT_CLASS}
                          />
                          <div className="flex items-center justify-between mt-0.5 gap-2">
                            <span className="text-[10px] text-slate-400">
                              {pct <= 0
                                ? '未设'
                                : canEstimateU
                                  ? (useU ? `≈ ${round2(pct)}% · ${verb} ${uStr} U` : `${verb} ${uStr} U`)
                                  : `${round2(pct)}%`}
                            </span>
                            <span className="inline-flex gap-1">
                              {[5, 10, 15].map((p) => (
                                <button
                                  key={p}
                                  type="button"
                                  onClick={() => setStratParams((s) => ({ ...s, [f.key]: p }))}
                                  className="px-1.5 py-0.5 rounded bg-slate-700 hover:bg-slate-600 text-[10px] text-slate-300"
                                >{p}%</button>
                              ))}
                            </span>
                          </div>
                          {f.help && <p className="text-[10px] text-slate-500 mt-0.5">{f.help}</p>}
                        </div>
                      )
                    })}
                  </div>

                  {canEstimateU && (
                    <p className="text-[10px] text-slate-500">
                      按 数量 {guardianQty} × 当前价 {symbolPrice} ≈ 名义 {guardianNotional.toLocaleString('en-US', { maximumFractionDigits: 0 })} U 估算,仅供参考(U 本位线性合约,与杠杆无关)。
                    </p>
                  )}
                </div>
              )}

              {/* Leverage slider — futures mode only, live only. Guardian 的杠杆已并入"顺便帮我开仓"卡片。 */}
              {showLeverage && !isGuardianForm && (
                <div className="bg-orange-900/20 border border-orange-700/40 rounded-lg p-3">
                  <LeverageSlider value={form.leverage} onChange={(v) => setForm({ ...form, leverage: v })} />
                </div>
              )}

              {/* Risk override — collapsible */}
              <div>
                <button type="button" onClick={() => setShowRisk(!showRisk)}
                  className="text-xs text-slate-400 hover:text-slate-200">
                  {showRisk ? '▾' : '▸'} Advanced Risk Settings
                </button>
                {showRisk && (
                  <div className="grid grid-cols-3 gap-3 mt-2">
                    <div>
                      <label className="block text-xs text-slate-400 mb-1">Max Position %</label>
                      <NumberInput min={0.01} max={1}
                        value={form.risk.max_position_pct}
                        onChange={(v) => setForm({ ...form, risk: { ...form.risk, max_position_pct: v === '' ? 0.01 : v } })}
                        className={INPUT_CLASS} />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-400 mb-1">Max Drawdown %</label>
                      <NumberInput min={0.01} max={1}
                        value={form.risk.max_drawdown_pct}
                        onChange={(v) => setForm({ ...form, risk: { ...form.risk, max_drawdown_pct: v === '' ? 0.01 : v } })}
                        className={INPUT_CLASS} />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-400 mb-1">Max Single Loss %</label>
                      <NumberInput min={0.001} max={0.5}
                        value={form.risk.max_single_loss_pct}
                        onChange={(v) => setForm({ ...form, risk: { ...form.risk, max_single_loss_pct: v === '' ? 0.001 : v } })}
                        className={INPUT_CLASS} />
                    </div>
                  </div>
                )}
              </div>

              {error && <p className="text-red-400 text-sm">{error}</p>}

              {(() => {
                const c = credById[form.credential_id]
                const isReal = c ? !(c.testnet || c.demo) : false
                return isReal ? (
                  <div className="bg-yellow-900/30 border border-yellow-700/50 rounded-lg p-3">
                    <p className="text-yellow-300 text-xs">
                      ⚠️ <strong>真钱账户</strong>:启动后会用真钱下单。确认参数无误、且已在「设置」里启用实盘交易。
                    </p>
                  </div>
                ) : null
              })()}

              <button type="submit" disabled={loading}
                className="px-5 py-2.5 disabled:opacity-50 rounded text-sm font-semibold bg-green-600 hover:bg-green-700">
                {loading ? '启动中…' : '▶ 启动引擎'}
              </button>
            </form>
          )}
        </div>
      )}

      <RunningEnginesList
        engines={runningEngines}
        credentials={creds}
        positionsByEngine={positionsByEngine}
        stoppingId={stoppingId}
        onStop={handleStop}
        onNavigate={handleNavigate}
        onPositionClosed={loadPositions}
      />

      <StoppedEnginesList
        engines={allStoppedEngines}
        onNavigate={handleNavigate}
      />
    </div>
  )
}
