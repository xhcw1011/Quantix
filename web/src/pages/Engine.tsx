import { useEffect, useState } from 'react'
import { listCredentials, listEngines, listStrategies, startEngine, stopEngineById } from '../api/trading'

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

const symbols = ['BTCUSDT', 'ETHUSDT', 'BNBUSDT', 'SOLUSDT']
const intervals = ['1m', '5m', '15m', '1h', '4h', '1d']

const initialForm = {
  credential_id: 0,
  strategy_id: 'macross',
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

function isDerivative(cred: Credential | undefined) {
  return cred?.market_type === 'swap' || cred?.market_type === 'futures'
}

export default function Engine() {
  const [engines, setEngines] = useState<EngineInfo[]>([])
  const [creds, setCreds] = useState<Credential[]>([])
  const [strategies, setStrategies] = useState<string[]>(['macross', 'grid', 'meanreversion', 'mlstrat'])
  const [form, setForm] = useState(initialForm)
  const [showForm, setShowForm] = useState(false)
  const [showRisk, setShowRisk] = useState(false)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [stoppingId, setStoppingId] = useState<string | null>(null)

  const selectedCred = creds.find((c) => c.id === form.credential_id)
  const showLeverage = form.mode === 'live' && isDerivative(selectedCred)
  const showShortToggle = form.strategy_id === 'macross' && isDerivative(selectedCred)

  const loadEngines = () =>
    listEngines()
      .then((r) => setEngines(r.data || []))
      .catch(() => {})

  useEffect(() => {
    listCredentials().then((r) => {
      const c = r.data || []
      setCreds(c)
      if (c.length > 0) setForm((f) => ({ ...f, credential_id: c[0].id }))
    })
    listStrategies().then((r) => {
      if (Array.isArray(r.data) && r.data.length > 0) setStrategies(r.data)
    }).catch(() => {})
    loadEngines()
    const t = setInterval(loadEngines, 10000)
    return () => clearInterval(t)
  }, [])

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
      // Strategy-specific params
      const params: Record<string, any> = {}
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
          {creds.length === 0 ? (
            <p className="text-slate-400 text-sm">
              No credentials found.{' '}
              <a href="/credentials" className="text-blue-400 hover:underline">Add a credential</a> first.
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
                    {m === 'live' ? '⚡ Live' : '📋 Paper'}
                  </button>
                ))}
                <span className="text-xs text-slate-500 ml-2 self-center">
                  {form.mode === 'paper' ? 'Simulated fills — no real orders' : 'Real order execution'}
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
                    {creds.map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.label} ({c.exchange} {c.market_type}{c.testnet ? ' testnet' : ''}{c.demo ? ' demo' : ''})
                      </option>
                    ))}
                  </select>
                </div>
                <div>
                  <label className="block text-xs text-slate-400 mb-1">Strategy</label>
                  <select
                    value={form.strategy_id}
                    onChange={(e) => setForm({ ...form, strategy_id: e.target.value })}
                    className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
                  >
                    {strategies.map((s) => <option key={s} value={s}>{s}</option>)}
                  </select>
                </div>
                <div>
                  <label className="block text-xs text-slate-400 mb-1">Symbol</label>
                  <select
                    value={form.symbol}
                    onChange={(e) => setForm({ ...form, symbol: e.target.value })}
                    className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
                  >
                    {symbols.map((s) => <option key={s} value={s}>{s}</option>)}
                  </select>
                </div>
                <div>
                  <label className="block text-xs text-slate-400 mb-1">Interval</label>
                  <select
                    value={form.interval}
                    onChange={(e) => setForm({ ...form, interval: e.target.value })}
                    className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
                  >
                    {intervals.map((i) => <option key={i} value={i}>{i}</option>)}
                  </select>
                </div>
              </div>

              {/* Leverage slider — only for live + swap/futures credentials */}
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

              {/* Short toggle + Stop/TP — only for macross on derivatives */}
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
                  <div><span className="block text-slate-500">Strategy</span>{eng.strategy_id}</div>
                  <div><span className="block text-slate-500">Symbol</span>{eng.symbol}</div>
                  <div><span className="block text-slate-500">Interval</span>{eng.interval}</div>
                  <div><span className="block text-slate-500">Started</span>{new Date(eng.started_at).toLocaleString()}</div>
                </div>
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
