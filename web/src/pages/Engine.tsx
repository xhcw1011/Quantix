import { useEffect, useState } from 'react'
import { getTicker, listCredentials, listEngines, listStrategies, listStrategyPresets, startEngine, stopEngineById, updateEngineParams } from '../api/trading'
import { useTradeSocket } from '../hooks/useTradeSocket'
import { COMMON_SYMBOLS, strategiesForMarket, strategyLabel, strategyMeta } from '../constants/strategies'
import type { MarketKind } from '../constants/strategies'
import { fieldsForStrategy } from '../constants/strategyFields'
import SymbolPicker from '../components/SymbolPicker'

interface Preset {
  name: string
  description: string
  params: Record<string, any>
}

// LiveStatus subscribes to WS "status" messages and renders the latest snapshot
// for the given engine_id. Server pushes once per minute from printStatus.
function LiveStatus({ engineID, strategyId }: { engineID: string; strategyId?: string }) {
  const [data, setData] = useState<Record<string, any> | null>(null)
  const [lastTs, setLastTs] = useState<number>(0)
  // 守仓风控档 live 编辑
  const [editing, setEditing] = useState(false)
  const [ev, setEv] = useState<Record<string, number | ''>>({})
  const [saving, setSaving] = useState(false)
  const [saveMsg, setSaveMsg] = useState('')

  useTradeSocket((msg: any) => {
    if (msg?.type === 'status' && msg?.data?.engine_id === engineID) {
      setData(msg.data)
      setLastTs(Date.now())
    }
  })

  const guardianFields = fieldsForStrategy('guardian')
  const openEdit = () => {
    const cur = (data?.strat_edit ?? {}) as Record<string, number>
    setEv(Object.fromEntries(guardianFields.map((f) => [f.key, cur[f.key] ?? f.default])))
    setSaveMsg('')
    setEditing(true)
  }
  const saveEdit = async () => {
    setSaving(true)
    setSaveMsg('')
    try {
      const params: Record<string, number> = {}
      for (const f of guardianFields) params[f.key] = (Number(ev[f.key]) || 0) / 100 // % → fraction
      await updateEngineParams(engineID, params)
      setSaveMsg('已保存,立即生效')
      setEditing(false)
    } catch (e: any) {
      setSaveMsg(e.response?.data?.error || '保存失败')
    } finally {
      setSaving(false)
    }
  }

  if (!data) {
    return (
      <p className="text-xs text-slate-600 mt-2">
        实时状态:等待下一次快照(约 60 秒一次)…
      </p>
    )
  }
  const ageS = Math.round((Date.now() - lastTs) / 1000)
  const num = (v: any, d = 2) => typeof v === 'number' ? v.toFixed(d) : String(v ?? '—')
  const stratFields = Object.entries(data).filter(([k]) => k.startsWith('strat_'))
  // 自动守仓 has its own plain-language panel. Detect it by strategy id, or by the
  // guardian-only `strat_state` key when the id isn't available.
  const isGuardian = strategyId === 'guardian' || data.strat_state !== undefined
  return (
    <div className="mt-3 border-t border-slate-700 pt-2 text-xs">
      <div className="flex items-center justify-between text-slate-500 mb-1.5">
        <span>实时状态</span>
        <span>{ageS}秒前更新</span>
      </div>
      <div className="grid grid-cols-2 md:grid-cols-4 gap-x-3 gap-y-1 text-slate-300">
        <div><span className="text-slate-500">权益</span> <span className="font-mono">${num(data.equity)}</span></div>
        <div><span className="text-slate-500">可用资金</span> <span className="font-mono">${num(data.cash)}</span></div>
        <div><span className="text-slate-500">已实现</span> <span className="font-mono">${num(data.realized_pnl)}</span></div>
        <div><span className="text-slate-500">收益率</span> <span className="font-mono">{num(data.total_return_pct)}%</span></div>
        {data.strat_regime !== undefined && (
          <div><span className="text-slate-500">Regime</span> <span className="font-mono">{data.strat_regime}</span></div>
        )}
        {data.strat_has_long !== undefined && (
          <div><span className="text-slate-500">LONG</span> <span className={`font-mono ${data.strat_has_long ? 'text-green-300' : 'text-slate-500'}`}>{data.strat_has_long ? 'open' : '—'}</span></div>
        )}
        {data.strat_has_short !== undefined && (
          <div><span className="text-slate-500">SHORT</span> <span className={`font-mono ${data.strat_has_short ? 'text-red-300' : 'text-slate-500'}`}>{data.strat_has_short ? 'open' : '—'}</span></div>
        )}
        {data.strat_hedge_cooldown_remaining !== undefined && data.strat_hedge_cooldown_remaining !== '0s' && (
          <div><span className="text-slate-500">Hedge CD</span> <span className="font-mono">{data.strat_hedge_cooldown_remaining}</span></div>
        )}
      </div>

      {/* 自动守仓 — 明白话面板 */}
      {isGuardian && data.strat_state !== undefined && (() => {
        const stateMap: Record<string, string> = {
          watching: '守护中', closing: '平仓中', closed: '已结束', arming: '准备中',
        }
        const stateLabel = stateMap[data.strat_state] ?? String(data.strat_state)
        const armed = data.strat_state !== 'arming'
        const sideLabel = data.strat_side === 'long' ? '做多' : data.strat_side === 'short' ? '做空' : '—'
        const pnlR = data.strat_pnl_r
        const pnlText = typeof pnlR === 'number' ? `${pnlR >= 0 ? '+' : ''}${pnlR.toFixed(1)}R` : '—'
        const pnlColor = typeof pnlR === 'number' ? (pnlR >= 0 ? 'text-green-300' : 'text-red-300') : 'text-slate-300'
        return (
          <div className="mt-2 border border-slate-700 rounded-lg bg-slate-900/40 p-3">
            <div className="flex items-center justify-between mb-2">
              <span className="text-xs font-semibold text-slate-300">自动守仓 · {data.strat_symbol ?? ''}</span>
              <div className="flex items-center gap-2">
                {armed && !editing && (
                  <button type="button" onClick={openEdit} className="text-xs px-2 py-0.5 rounded bg-slate-700 hover:bg-slate-600 text-slate-200">✎ 改风控档</button>
                )}
                <span className="text-xs px-1.5 py-0.5 rounded bg-slate-700 text-slate-200">{stateLabel}</span>
              </div>
            </div>
            {armed ? (
              <div className="grid grid-cols-2 md:grid-cols-4 gap-x-3 gap-y-1.5 text-slate-300">
                <div><span className="block text-slate-500">方向</span>{sideLabel}</div>
                <div><span className="block text-slate-500">成本价</span><span className="font-mono">{num(data.strat_entry)}</span></div>
                <div><span className="block text-slate-500">数量</span><span className="font-mono">{num(data.strat_qty, 4)}</span></div>
                <div><span className="block text-slate-500">当前止损价</span><span className="font-mono">{num(data.strat_stop)}</span></div>
                <div><span className="block text-slate-500">浮盈</span><span className={`font-mono ${pnlColor}`}>{pnlText}</span></div>
                <div><span className="block text-slate-500">止损保护</span>{data.strat_trail_active ? '已上移锁利' : '未激活'}</div>
                {typeof data.strat_tp === 'number' && data.strat_tp > 0 && (
                  <div><span className="block text-slate-500">止盈价</span><span className="font-mono">{num(data.strat_tp)}</span></div>
                )}
              </div>
            ) : (
              <p className="text-slate-400">正在准备守护你的仓位…</p>
            )}
            {editing && (
              <div className="mt-3 border-t border-slate-700 pt-2">
                <p className="text-[10px] text-slate-500 mb-2">改完立即生效,不停引擎、不动仓位。止损在已开始移动后只能收紧、不能放宽。</p>
                <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
                  {guardianFields.map((f) => (
                    <div key={f.key}>
                      <label className="block text-[11px] text-slate-400 mb-0.5">{f.label} (%)</label>
                      <input
                        type="number" step="any" min={0}
                        value={ev[f.key] ?? ''}
                        onChange={(e) => setEv((p) => ({ ...p, [f.key]: e.target.value === '' ? '' : Number(e.target.value) }))}
                        className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1 text-sm"
                      />
                    </div>
                  ))}
                </div>
                <div className="flex items-center gap-2 mt-2">
                  <button type="button" onClick={saveEdit} disabled={saving} className="px-3 py-1 bg-emerald-600 hover:bg-emerald-700 disabled:opacity-50 rounded text-xs font-semibold">{saving ? '保存中…' : '保存'}</button>
                  <button type="button" onClick={() => setEditing(false)} className="px-3 py-1 bg-slate-700 hover:bg-slate-600 rounded text-xs">取消</button>
                  {saveMsg && <span className="text-[11px] text-slate-400">{saveMsg}</span>}
                </div>
              </div>
            )}
            {!editing && saveMsg && <p className="text-[11px] text-emerald-400 mt-1.5">{saveMsg}</p>}
          </div>
        )
      })()}

      {stratFields.length > 0 && (
        <details className="mt-2">
          <summary className="text-slate-500 cursor-pointer">策略详情 ({stratFields.length} 项)</summary>
          <pre className="text-[10px] text-slate-400 mt-1 overflow-x-auto">{JSON.stringify(Object.fromEntries(stratFields), null, 2)}</pre>
        </details>
      )}
    </div>
  )
}

interface Credential {
  id: number
  exchange: string
  label: string
  market_type: string // "spot" | "swap" | "futures"
  testnet: boolean
  demo: boolean
}

interface EngineInfo {
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

const intervals = ['1m', '5m', '15m', '1h', '4h', '1d']

const initialForm = {
  credential_id: 0,
  strategy_id: 'dca',
  symbol: 'BTCUSDT',
  interval: '1h',
  mode: 'live' as 'live' | 'paper',
  leverage: 1,
  enable_short: false,
  stop_loss_pct: 0,
  take_profit_pct: 0,
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
  const [engines, setEngines] = useState<EngineInfo[]>([])
  const [creds, setCreds] = useState<Credential[]>([])
  const [strategies, setStrategies] = useState<string[]>(['macross', 'grid', 'meanreversion', 'mlstrat'])
  const [market, setMarket] = useState<MarketKind>('spot')
  const [form, setForm] = useState(initialForm)
  const [showForm, setShowForm] = useState(false)
  const [showRisk, setShowRisk] = useState(false)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [stoppingId, setStoppingId] = useState<string | null>(null)
  const [presets, setPresets] = useState<Preset[]>([])
  const [selectedPresetIdx, setSelectedPresetIdx] = useState<number>(-1)
  const [extraParams, setExtraParams] = useState<string>('')  // JSON textarea
  const [stratParams, setStratParams] = useState<Record<string, number | string>>({})
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

  // Leverage and short toggle are futures-only concepts.
  const showLeverage = market === 'futures' && form.mode === 'live'
  const showShortToggle = market === 'futures' && form.strategy_id === 'macross'

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

  const loadEngines = () =>
    listEngines()
      .then((r) => setEngines(r.data || []))
      .catch(() => {})

  useEffect(() => {
    listCredentials().then((r) => {
      const c: Credential[] = r.data || []
      setCreds(c)
      // Default to first spot credential; fall back to first of any type.
      const spotFirst = c.find((cr) => cr.market_type === 'spot') ?? c[0]
      if (spotFirst) setForm((f) => ({ ...f, credential_id: spotFirst.id }))
    })
    listStrategies().then((r) => {
      if (Array.isArray(r.data) && r.data.length > 0) setStrategies(r.data)
    }).catch(() => {})
    loadEngines()
    const t = setInterval(loadEngines, 10000)
    return () => clearInterval(t)
  }, [])

  // Refetch presets and reset dynamic params whenever strategy_id changes.
  useEffect(() => {
    setSelectedPresetIdx(-1)
    setExtraParams('')
    setGuardianEntry({ enabled: false, side: 'long', qty: 0, orderType: 'market', limitPrice: 0 })
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
      if (showLeverage && form.leverage > 1) {
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
      for (const f of fieldsForStrategy(form.strategy_id)) {
        let v: any = stratParams[f.key]
        if (v === '' || v === undefined) continue
        if (f.type === 'number') { v = Number(v); if (f.pctOf1) v = v / 100 }
        params[f.key] = v
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
      if (form.strategy_id === 'macross') {
        if (showShortToggle && form.enable_short) {
          params.EnableShort = true
        }
        if (form.stop_loss_pct > 0) {
          params.StopLossPct = form.stop_loss_pct
        }
        if (form.take_profit_pct > 0) {
          params.TakeProfitPct = form.take_profit_pct
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

  const handleStop = async (engineId: string) => {
    if (!confirm(`确定停止「${engineId}」?会取消所有未成交挂单。`)) return
    setStoppingId(engineId)
    try {
      await stopEngineById(engineId)
      loadEngines()
    } catch (err: any) {
      setError(err.response?.data?.error || 'Failed to stop engine')
    } finally {
      setStoppingId(null)
    }
  }

  const runningEngines = engines.filter((e) => e.running)
  const stoppedEngines = engines.filter((e) => !e.running)

  // 资金属性:引擎动的是真钱还是模拟钱(mode=live 只是"实时",配 demo/testnet 账户仍是假钱)。
  const credById = Object.fromEntries(creds.map((c) => [c.id, c]))
  const engineMoney = (eng: EngineInfo) => {
    if (eng.mode === 'paper') return { text: '回测/模拟', cls: 'bg-slate-600 text-slate-300' }
    const c = credById[eng.credential_id]
    if (!c) return { text: '实时', cls: 'bg-slate-600 text-slate-300' }
    if (c.testnet || c.demo) return { text: '模拟盘', cls: 'bg-blue-900/50 text-blue-300' }
    return { text: '真钱', cls: 'bg-red-900/50 text-red-300' }
  }
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

          {/* Market tabs: 现货 (default/primary) vs 合约·进阶 (secondary) */}
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
              {/* Mode toggle */}
              <div className="flex gap-2">
                {(['live', 'paper'] as const).map((m) => (
                  <button key={m} type="button"
                    onClick={() => setForm({ ...form, mode: m })}
                    className={`px-4 py-1.5 rounded text-sm font-semibold border transition-colors ${
                      form.mode === m
                        ? m === 'live'
                          ? 'bg-green-600 border-green-600 text-white'
                          : 'bg-blue-600 border-blue-600 text-white'
                        : 'bg-transparent border-slate-600 text-slate-400 hover:border-slate-400'
                    }`}>
                    {m === 'live' ? '⚡ 实盘' : '📋 模拟'}
                  </button>
                ))}
                <span className="text-xs text-slate-500 ml-2 self-center">
                  {form.mode === 'paper' ? '模拟撮合,不涉及真钱' : '真钱下单(用正式账户前需先在「设置」里启用实盘交易)'}
                </span>
              </div>

              <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                <div>
                  <label className="block text-xs text-slate-400 mb-1">交易账户</label>
                  <select
                    value={form.credential_id}
                    onChange={(e) => setForm({ ...form, credential_id: +e.target.value })}
                    className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
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
                      className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
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
                    className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
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
                  <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                    {fields.map((f) => (
                      <div key={f.key}>
                        <label className="block text-xs text-slate-400 mb-1">
                          {f.label}{f.unit ? ` (${f.unit})` : ''}
                        </label>
                        {f.type === 'select' ? (
                          <select
                            value={String(stratParams[f.key] ?? f.default)}
                            onChange={(e) => setStratParams((p) => ({ ...p, [f.key]: e.target.value }))}
                            className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
                          >
                            {f.options?.map((o) => (
                              <option key={o.value} value={o.value}>{o.label}</option>
                            ))}
                          </select>
                        ) : (
                          <input
                            type="number"
                            value={stratParams[f.key] ?? f.default}
                            step={f.step}
                            min={f.min}
                            max={f.max}
                            onChange={(e) =>
                              setStratParams((p) => ({
                                ...p,
                                [f.key]: e.target.value === '' ? '' : Number(e.target.value),
                              }))
                            }
                            className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
                          />
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
                    <button
                      type="button"
                      onClick={() => setGuardianEntry((g) => ({ ...g, enabled: !g.enabled }))}
                      className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors ${
                        guardianEntry.enabled ? 'bg-emerald-600' : 'bg-slate-600'
                      }`}
                    >
                      <span className={`inline-block h-4 w-4 transform rounded-full bg-white transition-transform ${
                        guardianEntry.enabled ? 'translate-x-6' : 'translate-x-1'
                      }`} />
                    </button>
                  </div>
                  {guardianEntry.enabled && (
                    <>
                      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                        <div>
                          <label className="block text-xs text-slate-400 mb-1">方向</label>
                          <select
                            value={guardianEntry.side}
                            onChange={(e) => setGuardianEntry((g) => ({ ...g, side: e.target.value as 'long' | 'short' }))}
                            className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
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
                          <input
                            type="number" min="0"
                            step={guardianQtyMode === 'coin' || !symbolPriceNum ? '0.001' : 'any'}
                            value={guardianEntry.qty > 0 ? qtyToDisplay(guardianEntry.qty) : ''}
                            onChange={(e) => {
                              const v = e.target.value === '' ? 0 : Number(e.target.value)
                              setGuardianEntry((g) => ({ ...g, qty: v <= 0 ? 0 : displayToQty(v) }))
                            }}
                            className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
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
                            <input
                              type="number" min="0" step="any"
                              value={guardianEntry.limitPrice || ''}
                              onChange={(e) => setGuardianEntry((g) => ({ ...g, limitPrice: e.target.value === '' ? 0 : Number(e.target.value) }))}
                              className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
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
                        <div>
                          <div className="flex items-center justify-between mb-1">
                            <label className="text-xs text-slate-400">杠杆<span className="text-slate-500 font-normal">(只影响新开仓保证金,守护已有仓不用设)</span></label>
                            <span className="text-sm font-bold text-orange-300">{form.leverage}x</span>
                          </div>
                          <input
                            type="range" min="1" max="20" step="1"
                            value={form.leverage}
                            onChange={(e) => setForm({ ...form, leverage: +e.target.value })}
                            className="w-full accent-orange-500"
                          />
                          <div className="flex justify-between text-xs text-slate-500 mt-0.5">
                            <span>1x</span><span>10x</span><span>20x</span>
                          </div>
                        </div>
                      )}
                    </>
                  )}
                  {!guardianEntry.enabled && (
                    <div>
                      <label className="block text-xs text-slate-400 mb-1">持仓数量(用于估算盈亏,可留空)</label>
                      <input
                        type="number" min="0" step="0.001"
                        value={guardianAdoptQty || ''}
                        onChange={(e) => setGuardianAdoptQty(e.target.value === '' ? 0 : Number(e.target.value))}
                        className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
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
                      const raw = stratParams[f.key]
                      const pct = Number(raw === '' || raw == null ? f.default : raw) || 0
                      const useU = guardianUMode && canEstimateU
                      const u = pctToU(pct)
                      const uStr = u != null ? u.toLocaleString('en-US', { maximumFractionDigits: u >= 100 ? 0 : 2 }) : null
                      const verb = guardianFieldIsLoss(f.key) ? '约亏' : '约赚'
                      const displayVal = raw === '' ? '' : (useU ? round2(u ?? 0) : pct)
                      return (
                        <div key={f.key}>
                          <label className="block text-xs text-slate-400 mb-1">
                            {f.label}{useU ? ' (U)' : ' (%)'}
                          </label>
                          <input
                            type="number"
                            value={displayVal}
                            step={useU ? 'any' : f.step}
                            min={0}
                            onChange={(e) => {
                              const val = e.target.value
                              setStratParams((p) => ({
                                ...p,
                                [f.key]: val === '' ? '' : (useU ? uToPct(Number(val)) : Number(val)),
                              }))
                            }}
                            className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
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
                  <div className="flex items-center justify-between mb-1">
                    <label className="text-xs text-slate-400">杠杆</label>
                    <span className="text-sm font-bold text-orange-300">{form.leverage}x</span>
                  </div>
                  <input
                    type="range" min="1" max="20" step="1"
                    value={form.leverage}
                    onChange={(e) => setForm({ ...form, leverage: +e.target.value })}
                    className="w-full accent-orange-500"
                  />
                  <div className="flex justify-between text-xs text-slate-500 mt-0.5">
                    <span>1x</span><span>10x</span><span>20x</span>
                  </div>
                </div>
              )}

              {/* Short toggle — futures mode, macross strategy only */}
              {showShortToggle && (
                <div className="bg-purple-900/20 border border-purple-700/40 rounded-lg p-3 space-y-3">
                  <div className="flex items-center justify-between">
                    <div>
                      <span className="text-xs font-semibold text-slate-300">Hedge Mode (Enable Short)</span>
                      <p className="text-xs text-slate-500">Death cross opens SHORT position</p>
                    </div>
                    <button
                      type="button"
                      onClick={() => setForm({ ...form, enable_short: !form.enable_short })}
                      className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors ${
                        form.enable_short ? 'bg-purple-600' : 'bg-slate-600'
                      }`}
                    >
                      <span className={`inline-block h-4 w-4 transform rounded-full bg-white transition-transform ${
                        form.enable_short ? 'translate-x-6' : 'translate-x-1'
                      }`} />
                    </button>
                  </div>
                </div>
              )}

              {/* Stop-loss and Take-profit — always visible for macross */}
              {form.strategy_id === 'macross' && (
                <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                  <div>
                    <label className="block text-xs text-slate-400 mb-1">
                      Stop Loss % <span className="text-slate-500">(0 = disabled)</span>
                    </label>
                    <div className="flex items-center gap-1">
                      <input
                        type="number" step="0.005" min="0" max="0.5"
                        value={form.stop_loss_pct}
                        onChange={(e) => setForm({ ...form, stop_loss_pct: +e.target.value })}
                        className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
                        placeholder="e.g. 0.02"
                      />
                      <span className="text-xs text-slate-500 shrink-0">
                        {form.stop_loss_pct > 0 ? `${(form.stop_loss_pct * 100).toFixed(1)}%` : 'off'}
                      </span>
                    </div>
                  </div>
                  <div>
                    <label className="block text-xs text-slate-400 mb-1">
                      Take Profit % <span className="text-slate-500">(0 = disabled)</span>
                    </label>
                    <div className="flex items-center gap-1">
                      <input
                        type="number" step="0.005" min="0" max="1"
                        value={form.take_profit_pct}
                        onChange={(e) => setForm({ ...form, take_profit_pct: +e.target.value })}
                        className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
                        placeholder="e.g. 0.04"
                      />
                      <span className="text-xs text-slate-500 shrink-0">
                        {form.take_profit_pct > 0 ? `${(form.take_profit_pct * 100).toFixed(1)}%` : 'off'}
                      </span>
                    </div>
                  </div>
                </div>
              )}

              {/* Paper config */}
              {form.mode === 'paper' && (
                <div className="grid grid-cols-3 gap-3 bg-blue-900/20 border border-blue-700/40 rounded-lg p-3">
                  <div>
                    <label className="block text-xs text-slate-400 mb-1">Initial Capital (USDT)</label>
                    <input type="number" min="100" value={form.paper.initial_capital}
                      onChange={(e) => setForm({ ...form, paper: { ...form.paper, initial_capital: +e.target.value } })}
                      className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm" />
                  </div>
                  <div>
                    <label className="block text-xs text-slate-400 mb-1">Fee Rate</label>
                    <input type="number" step="0.0001" min="0" value={form.paper.fee_rate}
                      onChange={(e) => setForm({ ...form, paper: { ...form.paper, fee_rate: +e.target.value } })}
                      className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm" />
                  </div>
                  <div>
                    <label className="block text-xs text-slate-400 mb-1">Slippage</label>
                    <input type="number" step="0.0001" min="0" value={form.paper.slippage}
                      onChange={(e) => setForm({ ...form, paper: { ...form.paper, slippage: +e.target.value } })}
                      className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm" />
                  </div>
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
                      <input type="number" step="0.01" min="0.01" max="1"
                        value={form.risk.max_position_pct}
                        onChange={(e) => setForm({ ...form, risk: { ...form.risk, max_position_pct: +e.target.value } })}
                        className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm" />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-400 mb-1">Max Drawdown %</label>
                      <input type="number" step="0.01" min="0.01" max="1"
                        value={form.risk.max_drawdown_pct}
                        onChange={(e) => setForm({ ...form, risk: { ...form.risk, max_drawdown_pct: +e.target.value } })}
                        className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm" />
                    </div>
                    <div>
                      <label className="block text-xs text-slate-400 mb-1">Max Single Loss %</label>
                      <input type="number" step="0.01" min="0.001" max="0.5"
                        value={form.risk.max_single_loss_pct}
                        onChange={(e) => setForm({ ...form, risk: { ...form.risk, max_single_loss_pct: +e.target.value } })}
                        className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm" />
                    </div>
                  </div>
                )}
              </div>

              {error && <p className="text-red-400 text-sm">{error}</p>}

              {form.mode === 'live' && (
                <div className="bg-yellow-900/30 border border-yellow-700/50 rounded-lg p-3">
                  <p className="text-yellow-300 text-xs">
                    ⚠️ <strong>Warning:</strong> Live mode executes real orders.
                    Ensure you have selected the correct credential (testnet/demo) before proceeding.
                  </p>
                </div>
              )}

              <button type="submit" disabled={loading}
                className={`px-5 py-2.5 disabled:opacity-50 rounded text-sm font-semibold ${
                  form.mode === 'paper'
                    ? 'bg-blue-600 hover:bg-blue-700'
                    : 'bg-green-600 hover:bg-green-700'
                }`}>
                {loading ? 'Starting...' : form.mode === 'paper' ? '▶ Start Paper Engine' : '▶ Start Live Engine'}
              </button>
            </form>
          )}
        </div>
      )}

      {/* Running engines */}
      <div className="space-y-3">
        <h2 className="text-sm font-semibold text-slate-400">
          运行中 ({runningEngines.length})
        </h2>
        {runningEngines.length === 0 ? (
          <div className="bg-slate-800 rounded-xl p-5 text-slate-500 text-sm">
            暂无运行中的策略。点 <strong>+ 新建策略</strong> 启动一个。
          </div>
        ) : (
          runningEngines.map((eng) => (
            <div key={eng.engine_id} className="bg-slate-800 rounded-xl p-4 flex items-start gap-4">
              <div className="w-2.5 h-2.5 mt-1.5 rounded-full bg-green-400 animate-pulse flex-shrink-0" />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="font-semibold text-sm">{eng.engine_id}</span>
                  <span className="text-xs bg-green-900/50 text-green-300 px-1.5 py-0.5 rounded">运行中</span>
                  {(() => {
                    const m = engineMoney(eng)
                    return <span title={`mode=${eng.mode} · credential_id=${eng.credential_id}`} className={`text-xs ${m.cls} px-1.5 py-0.5 rounded`}>{m.text}</span>
                  })()}
                  {eng.leverage && eng.leverage > 1 && (
                    <span className="text-xs bg-orange-900/50 text-orange-300 px-1.5 py-0.5 rounded font-mono">
                      {eng.leverage}x
                    </span>
                  )}
                </div>
                <div className="grid grid-cols-2 md:grid-cols-4 gap-2 mt-2 text-xs text-slate-400">
                  <div><span className="block text-slate-500">策略</span>{strategyLabel(eng.strategy_id)}</div>
                  <div><span className="block text-slate-500">交易对</span>{eng.symbol}</div>
                  <div><span className="block text-slate-500">周期</span>{eng.interval}</div>
                  <div><span className="block text-slate-500">启动于</span>{new Date(eng.started_at).toLocaleString()}</div>
                </div>
                {eng.mode === 'live' && <LiveStatus engineID={eng.engine_id} strategyId={eng.strategy_id} />}
              </div>
              <button
                onClick={() => handleStop(eng.engine_id)}
                disabled={stoppingId === eng.engine_id}
                className="px-3 py-1.5 bg-red-600 hover:bg-red-700 disabled:opacity-50 rounded text-xs font-semibold flex-shrink-0"
              >
                {stoppingId === eng.engine_id ? '停止中…' : '⏹ 停止'}
              </button>
            </div>
          ))
        )}
      </div>

      {/* Stopped engines */}
      {stoppedEngines.length > 0 && (
        <div className="space-y-3">
          <h2 className="text-sm font-semibold text-slate-400">
            Stopped ({stoppedEngines.length})
          </h2>
          {stoppedEngines.map((eng) => (
            <div key={eng.engine_id} className="bg-slate-800/50 rounded-xl p-4 flex items-start gap-4">
              <div className="w-2.5 h-2.5 mt-1.5 rounded-full bg-slate-500 flex-shrink-0" />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="font-semibold text-sm text-slate-400">{eng.engine_id}</span>
                  <span className="text-xs bg-slate-700 text-slate-400 px-1.5 py-0.5 rounded">stopped</span>
                  {eng.mode === 'paper' && (
                    <span className="text-xs bg-blue-900/30 text-blue-400 px-1.5 py-0.5 rounded">paper</span>
                  )}
                </div>
                {eng.error && <p className="text-xs text-red-400 mt-1">Error: {eng.error}</p>}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
