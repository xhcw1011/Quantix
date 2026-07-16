import { useEffect, useState } from 'react'
import { getTicker, listCredentials, listEngines, listStrategies, listStrategyPresets, startEngine, stopEngineById } from '../api/trading'
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

  useTradeSocket((msg: any) => {
    if (msg?.type === 'status' && msg?.data?.engine_id === engineID) {
      setData(msg.data)
      setLastTs(Date.now())
    }
  })

  if (!data) {
    return (
      <p className="text-xs text-slate-600 mt-2">
        Live status: waiting for next snapshot (push interval ~60s)…
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
        <span>Live status</span>
        <span>updated {ageS}s ago</span>
      </div>
      <div className="grid grid-cols-2 md:grid-cols-4 gap-x-3 gap-y-1 text-slate-300">
        <div><span className="text-slate-500">Equity</span> <span className="font-mono">${num(data.equity)}</span></div>
        <div><span className="text-slate-500">Cash</span> <span className="font-mono">${num(data.cash)}</span></div>
        <div><span className="text-slate-500">Realized</span> <span className="font-mono">${num(data.realized_pnl)}</span></div>
        <div><span className="text-slate-500">Return</span> <span className="font-mono">{num(data.total_return_pct)}%</span></div>
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
              <span className="text-xs px-1.5 py-0.5 rounded bg-slate-700 text-slate-200">{stateLabel}</span>
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
          </div>
        )
      })()}

      {stratFields.length > 0 && (
        <details className="mt-2">
          <summary className="text-slate-500 cursor-pointer">Strategy detail ({stratFields.length} fields)</summary>
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
  // 自动守仓 "顺便帮我开仓" — off by default = 守护已有仓位.
  const [guardianEntry, setGuardianEntry] = useState<{ enabled: boolean; side: 'long' | 'short'; qty: number }>({
    enabled: false,
    side: 'long',
    qty: 0,
  })

  // Credentials and strategies filtered by the selected market tab.
  const filteredCreds = creds.filter((c) =>
    market === 'spot' ? c.market_type === 'spot' : (c.market_type === 'swap' || c.market_type === 'futures')
  )
  const availableStrategies = strategiesForMarket(market).filter((s) => strategies.includes(s.id))

  // Leverage and short toggle are futures-only concepts.
  const showLeverage = market === 'futures' && form.mode === 'live'
  const showShortToggle = market === 'futures' && form.strategy_id === 'macross'

  const handleMarketChange = (newMarket: MarketKind) => {
    setMarket(newMarket)
    const marketCreds = creds.filter((c) =>
      newMarket === 'spot' ? c.market_type === 'spot' : (c.market_type === 'swap' || c.market_type === 'futures')
    )
    const marketStrategies = strategiesForMarket(newMarket).filter((s) => strategies.includes(s.id))
    setForm((f) => ({
      ...f,
      credential_id: marketCreds[0]?.id ?? 0,
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
    setGuardianEntry({ enabled: false, side: 'long', qty: 0 })
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
      return
    }
    let cancelled = false
    setSymbolPrice(null) // 先显示"—",取到再更新
    getTicker(form.symbol)
      .then((r) => {
        if (cancelled) return
        const n = Number(r.data?.price)
        if (!isFinite(n)) {
          setSymbolPrice(null)
          return
        }
        const digits = n >= 1 ? 2 : 6
        setSymbolPrice(n.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: digits }))
      })
      .catch(() => {
        if (!cancelled) setSymbolPrice(null)
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
    if (!confirm(`Stop engine "${engineId}"? This will cancel all pending orders.`)) return
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
  const fields = fieldsForStrategy(form.strategy_id)

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold">Engine Control</h1>
        <button
          onClick={() => { setShowForm(!showForm); setError('') }}
          className="px-4 py-2 bg-green-600 hover:bg-green-700 rounded text-sm font-semibold"
        >
          {showForm ? '✕ Cancel' : '+ New Engine'}
        </button>
      </div>

      {/* New engine form */}
      {showForm && (
        <div className="bg-slate-800 rounded-xl p-5">
          <h2 className="text-sm font-semibold text-slate-300 mb-4">Start New Engine</h2>

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
              No credentials found.{' '}
              <a href="/credentials" className="text-blue-400 hover:underline">Add a credential</a> first.
            </p>
          ) : filteredCreds.length === 0 ? (
            <p className="text-slate-400 text-sm">
              还没有{market === 'spot' ? '现货' : '合约'}凭证，先去{' '}
              <a href="/credentials" className="text-blue-400 hover:underline">添加凭证</a>。
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
                  <label className="block text-xs text-slate-400 mb-1">Credential</label>
                  <select
                    value={form.credential_id}
                    onChange={(e) => setForm({ ...form, credential_id: +e.target.value })}
                    className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
                  >
                    {filteredCreds.map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.label} ({c.exchange} {c.market_type}{c.testnet ? ' testnet' : ''}{c.demo ? ' demo' : ''})
                      </option>
                    ))}
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

              {/* Dynamic strategy parameter fields — shown for spot strategies with a schema */}
              {fields.length > 0 && (
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

              {/* 自动守仓 — 顺便帮我开仓 (否则守护已有仓位) */}
              {form.strategy_id === 'guardian' && (
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
                        <label className="block text-xs text-slate-400 mb-1">数量</label>
                        <input
                          type="number" min="0" step="0.001"
                          value={guardianEntry.qty}
                          onChange={(e) => setGuardianEntry((g) => ({ ...g, qty: e.target.value === '' ? 0 : Number(e.target.value) }))}
                          className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
                          placeholder="例如 0.1"
                        />
                      </div>
                    </div>
                  )}
                </div>
              )}

              {/* Leverage slider — futures mode only, live only */}
              {showLeverage && (
                <div className="bg-orange-900/20 border border-orange-700/40 rounded-lg p-3">
                  <div className="flex items-center justify-between mb-1">
                    <label className="text-xs text-slate-400">Leverage</label>
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
          Running ({runningEngines.length})
        </h2>
        {runningEngines.length === 0 ? (
          <div className="bg-slate-800 rounded-xl p-5 text-slate-500 text-sm">
            No engines running. Click <strong>+ New Engine</strong> to start one.
          </div>
        ) : (
          runningEngines.map((eng) => (
            <div key={eng.engine_id} className="bg-slate-800 rounded-xl p-4 flex items-start gap-4">
              <div className="w-2.5 h-2.5 mt-1.5 rounded-full bg-green-400 animate-pulse flex-shrink-0" />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="font-semibold text-sm">{eng.engine_id}</span>
                  <span className="text-xs bg-green-900/50 text-green-300 px-1.5 py-0.5 rounded">running</span>
                  {eng.mode === 'paper' ? (
                    <span className="text-xs bg-blue-900/50 text-blue-300 px-1.5 py-0.5 rounded">paper</span>
                  ) : (
                    <span className="text-xs bg-slate-600 text-slate-300 px-1.5 py-0.5 rounded">live</span>
                  )}
                  {eng.leverage && eng.leverage > 1 && (
                    <span className="text-xs bg-orange-900/50 text-orange-300 px-1.5 py-0.5 rounded font-mono">
                      {eng.leverage}x
                    </span>
                  )}
                </div>
                <div className="grid grid-cols-2 md:grid-cols-4 gap-2 mt-2 text-xs text-slate-400">
                  <div><span className="block text-slate-500">Strategy</span>{strategyLabel(eng.strategy_id)}</div>
                  <div><span className="block text-slate-500">Symbol</span>{eng.symbol}</div>
                  <div><span className="block text-slate-500">Interval</span>{eng.interval}</div>
                  <div><span className="block text-slate-500">Started</span>{new Date(eng.started_at).toLocaleString()}</div>
                </div>
                {eng.mode === 'live' && <LiveStatus engineID={eng.engine_id} strategyId={eng.strategy_id} />}
              </div>
              <button
                onClick={() => handleStop(eng.engine_id)}
                disabled={stoppingId === eng.engine_id}
                className="px-3 py-1.5 bg-red-600 hover:bg-red-700 disabled:opacity-50 rounded text-xs font-semibold flex-shrink-0"
              >
                {stoppingId === eng.engine_id ? 'Stopping…' : '⏹ Stop'}
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
