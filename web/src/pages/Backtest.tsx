import { useEffect, useRef, useState } from 'react'
import {
  LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer
} from 'recharts'
import { submitBacktest, listBacktests, getBacktest, deleteBacktest, listStrategies } from '../api/trading'

// ─── Types ────────────────────────────────────────────────────────────────────

interface BacktestJob {
  ID: string
  StrategyID: string
  Symbol: string
  Interval: string
  Status: string  // running | completed | failed
  InitialCapital: number
  FeeRate: number
  Slippage: number
  StartDate?: string
  EndDate?: string
  Params?: Record<string, number>
  Result?: BacktestReport
  ErrorMsg?: string
  CreatedAt: string
}

interface BacktestReport {
  StrategyName: string
  Symbol: string
  Interval: string
  TotalBars: number
  InitialCapital: number
  FinalEquity: number
  TotalReturn: number
  AnnualReturn: number
  SharpeRatio: number
  CalmarRatio: number
  MaxDrawdown: number
  MaxDrawdownAbs: number
  TotalTrades: number
  WinningTrades: number
  LosingTrades: number
  WinRate: number
  AvgWinPct: number
  AvgLossPct: number
  ProfitFactor: number
  Trades: Trade[]
  EquityCurve: EquityPoint[]
}

interface Trade {
  Symbol: string
  Side: string
  EntryTime: string
  ExitTime: string
  EntryPrice: number
  ExitPrice: number
  Qty: number
  GrossPnL: number
  Fee: number
  NetPnL: number
  PnLPct: number
}

interface EquityPoint {
  Time: string
  Equity: number
  Cash: number
}

// ─── Strategy param definitions ───────────────────────────────────────────────

interface ParamDef { key: string; label: string; default: number; step?: number }

const strategyParams: Record<string, ParamDef[]> = {
  macross: [
    { key: 'FastPeriod', label: 'Fast MA', default: 10 },
    { key: 'SlowPeriod', label: 'Slow MA', default: 30 },
  ],
  meanreversion: [
    { key: 'BBPeriod', label: 'BB Period', default: 20 },
    { key: 'RSIPeriod', label: 'RSI Period', default: 14 },
    { key: 'BBMultiplier', label: 'BB Multiplier', default: 2, step: 0.1 },
  ],
  grid: [
    { key: 'GridLevels', label: 'Grid Levels', default: 10 },
    { key: 'GridSpread', label: 'Grid Spread', default: 0.02, step: 0.01 },
  ],
  mlstrat: [],
}

const staticStrategies = Object.keys(strategyParams)
const symbols = ['BTCUSDT', 'ETHUSDT', 'BNBUSDT', 'SOLUSDT']
const intervals = ['1m', '5m', '15m', '1h', '4h', '1d']

const initialForm = {
  strategy_id: 'macross',
  symbol: 'BTCUSDT',
  interval: '1h',
  initial_capital: 10000,
  fee_rate: 0.001,
  slippage: 0.0005,
  start_date: '',
  end_date: '',
  params: {} as Record<string, number>,
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function fmt(n?: number, decimals = 2) {
  if (n == null || isNaN(n)) return '—'
  return n.toFixed(decimals)
}

function pct(n?: number) {
  if (n == null) return '—'
  return (n >= 0 ? '+' : '') + n.toFixed(2) + '%'
}

function colorPct(n?: number) {
  if (n == null) return 'text-slate-400'
  return n >= 0 ? 'text-green-400' : 'text-red-400'
}

// ─── Component ────────────────────────────────────────────────────────────────

export default function Backtest() {
  const [jobs, setJobs] = useState<BacktestJob[]>([])
  const [selected, setSelected] = useState<BacktestJob | null>(null)
  const [showForm, setShowForm] = useState(false)
  const [form, setForm] = useState(initialForm)
  const [paramValues, setParamValues] = useState<Record<string, number>>({})
  const [submitting, setSubmitting] = useState(false)
  const [formError, setFormError] = useState('')
  const [strategies, setStrategies] = useState<string[]>(staticStrategies)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const loadJobs = () =>
    listBacktests().then((r) => setJobs(r.data || [])).catch(() => {})

  useEffect(() => {
    loadJobs()
    listStrategies().then((r) => {
      if (Array.isArray(r.data) && r.data.length > 0) setStrategies(r.data)
    }).catch(() => {})
  }, [])

  // Init param values when strategy changes
  useEffect(() => {
    const defs = strategyParams[form.strategy_id] || []
    const defaults: Record<string, number> = {}
    defs.forEach((d) => { defaults[d.key] = d.default })
    setParamValues(defaults)
  }, [form.strategy_id])

  // Poll selected job if running
  useEffect(() => {
    if (pollRef.current) clearInterval(pollRef.current)
    if (!selected || selected.Status !== 'running') return

    pollRef.current = setInterval(async () => {
      try {
        const r = await getBacktest(selected.ID)
        const updated: BacktestJob = r.data
        setSelected(updated)
        if (updated.Status !== 'running') {
          clearInterval(pollRef.current!)
          loadJobs()
        }
      } catch {}
    }, 2000)

    return () => { if (pollRef.current) clearInterval(pollRef.current) }
  }, [selected?.ID, selected?.Status])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setFormError('')
    setSubmitting(true)
    try {
      const payload = {
        ...form,
        params: paramValues,
      }
      const r = await submitBacktest(payload)
      const newJob: BacktestJob = {
        ID: r.data.id,
        StrategyID: form.strategy_id,
        Symbol: form.symbol,
        Interval: form.interval,
        Status: 'running',
        InitialCapital: form.initial_capital,
        FeeRate: form.fee_rate,
        Slippage: form.slippage,
        CreatedAt: new Date().toISOString(),
      }
      setJobs((prev) => [newJob, ...prev])
      setSelected(newJob)
      setShowForm(false)
    } catch (err: any) {
      setFormError(err.response?.data?.error || 'Failed to submit backtest')
    } finally {
      setSubmitting(false)
    }
  }

  const handleDelete = async (id: string) => {
    if (!confirm('Delete this backtest?')) return
    await deleteBacktest(id)
    setJobs((prev) => prev.filter((j) => j.ID !== id))
    if (selected?.ID === id) setSelected(null)
  }

  const handleSelect = async (job: BacktestJob) => {
    if (job.Status === 'completed' && job.Result) {
      setSelected(job)
      return
    }
    try {
      const r = await getBacktest(job.ID)
      setSelected(r.data)
    } catch {
      setSelected(job)
    }
  }

  const report = selected?.Result
  const equityData = report?.EquityCurve?.map((p) => ({
    time: new Date(p.Time).toLocaleDateString(),
    equity: +p.Equity.toFixed(2),
  })) || []

  const paramDefs = strategyParams[form.strategy_id] || []

  return (
    <div className="space-y-4 h-full">
      {/* Header */}
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold">Backtest</h1>
        <button
          onClick={() => { setShowForm(!showForm); setFormError('') }}
          className="px-4 py-2 bg-blue-600 hover:bg-blue-700 rounded text-sm font-semibold"
        >
          {showForm ? '✕ Cancel' : '+ New Backtest'}
        </button>
      </div>

      {/* New backtest form */}
      {showForm && (
        <div className="bg-slate-800 rounded-xl p-5">
          <h2 className="text-sm font-semibold text-slate-300 mb-4">Configure Backtest</h2>
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="grid grid-cols-2 md:grid-cols-3 gap-3">
              <div>
                <label className="block text-xs text-slate-400 mb-1">Strategy</label>
                <select value={form.strategy_id}
                  onChange={(e) => setForm({ ...form, strategy_id: e.target.value })}
                  className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm">
                  {strategies.map((s) => <option key={s} value={s}>{s}</option>)}
                </select>
              </div>
              <div>
                <label className="block text-xs text-slate-400 mb-1">Symbol</label>
                <select value={form.symbol}
                  onChange={(e) => setForm({ ...form, symbol: e.target.value })}
                  className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm">
                  {symbols.map((s) => <option key={s} value={s}>{s}</option>)}
                </select>
              </div>
              <div>
                <label className="block text-xs text-slate-400 mb-1">Interval</label>
                <select value={form.interval}
                  onChange={(e) => setForm({ ...form, interval: e.target.value })}
                  className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm">
                  {intervals.map((i) => <option key={i} value={i}>{i}</option>)}
                </select>
              </div>
              <div>
                <label className="block text-xs text-slate-400 mb-1">Initial Capital (USDT)</label>
                <input type="number" min="100" value={form.initial_capital}
                  onChange={(e) => setForm({ ...form, initial_capital: +e.target.value })}
                  className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm" />
              </div>
              <div>
                <label className="block text-xs text-slate-400 mb-1">Fee Rate</label>
                <input type="number" step="0.0001" min="0" value={form.fee_rate}
                  onChange={(e) => setForm({ ...form, fee_rate: +e.target.value })}
                  className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm" />
              </div>
              <div>
                <label className="block text-xs text-slate-400 mb-1">Slippage</label>
                <input type="number" step="0.0001" min="0" value={form.slippage}
                  onChange={(e) => setForm({ ...form, slippage: +e.target.value })}
                  className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm" />
              </div>
              <div>
                <label className="block text-xs text-slate-400 mb-1">Start Date (optional)</label>
                <input type="date" value={form.start_date}
                  onChange={(e) => setForm({ ...form, start_date: e.target.value })}
                  className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm" />
              </div>
              <div>
                <label className="block text-xs text-slate-400 mb-1">End Date (optional)</label>
                <input type="date" value={form.end_date}
                  onChange={(e) => setForm({ ...form, end_date: e.target.value })}
                  className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm" />
              </div>
            </div>

            {/* Dynamic strategy params */}
            {paramDefs.length > 0 && (
              <div>
                <p className="text-xs text-slate-400 mb-2">Strategy Parameters</p>
                <div className="grid grid-cols-3 gap-3">
                  {paramDefs.map((d) => (
                    <div key={d.key}>
                      <label className="block text-xs text-slate-400 mb-1">{d.label}</label>
                      <input type="number" step={d.step ?? 1} value={paramValues[d.key] ?? d.default}
                        onChange={(e) => setParamValues({ ...paramValues, [d.key]: +e.target.value })}
                        className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm" />
                    </div>
                  ))}
                </div>
              </div>
            )}

            {formError && <p className="text-red-400 text-sm">{formError}</p>}
            <button type="submit" disabled={submitting}
              className="px-5 py-2 bg-blue-600 hover:bg-blue-700 disabled:opacity-50 rounded text-sm font-semibold">
              {submitting ? 'Submitting…' : '▶ Run Backtest'}
            </button>
          </form>
        </div>
      )}

      {/* Main content: history + results */}
      <div className="flex flex-col md:flex-row gap-4" style={{ minHeight: '60vh' }}>
        {/* History list */}
        <div className="w-full md:w-56 md:flex-shrink-0 space-y-2">
          <p className="text-xs text-slate-400 font-semibold uppercase tracking-wider">History</p>
          {jobs.length === 0 && (
            <p className="text-slate-500 text-xs">No backtests yet.</p>
          )}
          {jobs.map((job) => (
            <div key={job.ID}
              onClick={() => handleSelect(job)}
              className={`cursor-pointer p-3 rounded-lg border transition-colors ${
                selected?.ID === job.ID
                  ? 'border-blue-500 bg-slate-700'
                  : 'border-slate-700 bg-slate-800 hover:border-slate-500'
              }`}>
              <div className="flex items-center gap-1.5 mb-1">
                <span className={`w-2 h-2 rounded-full flex-shrink-0 ${
                  job.Status === 'running' ? 'bg-yellow-400 animate-pulse' :
                  job.Status === 'completed' ? 'bg-green-400' : 'bg-red-400'
                }`} />
                <span className="text-xs font-semibold truncate">{job.StrategyID}</span>
              </div>
              <p className="text-xs text-slate-400">{job.Symbol} · {job.Interval}</p>
              {job.Result && (
                <p className={`text-xs font-mono mt-1 ${colorPct(job.Result.TotalReturn)}`}>
                  {pct(job.Result.TotalReturn)}
                </p>
              )}
              {job.Status === 'failed' && (
                <p className="text-xs text-red-400 mt-1">Failed</p>
              )}
              <p className="text-xs text-slate-600 mt-1">
                {new Date(job.CreatedAt).toLocaleDateString()}
              </p>
            </div>
          ))}
        </div>

        {/* Results panel */}
        <div className="flex-1 min-w-0">
          {!selected && (
            <div className="h-full flex items-center justify-center text-slate-500 text-sm">
              Select a backtest from the history, or run a new one.
            </div>
          )}

          {selected?.Status === 'running' && (
            <div className="flex flex-col items-center justify-center h-64 gap-3">
              <div className="w-8 h-8 border-2 border-blue-400 border-t-transparent rounded-full animate-spin" />
              <p className="text-slate-400 text-sm">Running backtest…</p>
            </div>
          )}

          {selected?.Status === 'failed' && (
            <div className="bg-red-900/20 border border-red-700/50 rounded-xl p-5">
              <p className="text-red-400 font-semibold mb-1">Backtest failed</p>
              <p className="text-red-300 text-sm">{selected.ErrorMsg || 'Unknown error'}</p>
            </div>
          )}

          {selected?.Status === 'completed' && report && (
            <div className="space-y-4">
              {/* Header + delete */}
              <div className="flex items-center justify-between">
                <div>
                  <h2 className="font-semibold">
                    {report.StrategyName} · {report.Symbol} · {report.Interval}
                  </h2>
                  <p className="text-xs text-slate-400">
                    {report.TotalBars} bars · capital ${fmt(report.InitialCapital)}
                  </p>
                </div>
                <button onClick={() => handleDelete(selected.ID)}
                  className="text-xs px-2 py-1 bg-red-600/70 hover:bg-red-600 rounded">
                  Delete
                </button>
              </div>

              {/* Metrics cards */}
              <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
                {[
                  { label: 'Total Return', value: pct(report.TotalReturn), color: colorPct(report.TotalReturn) },
                  { label: 'Annual Return', value: pct(report.AnnualReturn), color: colorPct(report.AnnualReturn) },
                  { label: 'Sharpe Ratio', value: fmt(report.SharpeRatio), color: report.SharpeRatio >= 1 ? 'text-green-400' : 'text-slate-300' },
                  { label: 'Max Drawdown', value: '-' + fmt(report.MaxDrawdown) + '%', color: 'text-red-400' },
                  { label: 'Win Rate', value: fmt(report.WinRate) + '%', color: '' },
                  { label: 'Profit Factor', value: fmt(report.ProfitFactor), color: report.ProfitFactor >= 1 ? 'text-green-400' : 'text-red-400' },
                  { label: 'Total Trades', value: String(report.TotalTrades), color: '' },
                  { label: 'Final Equity', value: '$' + fmt(report.FinalEquity), color: colorPct(report.TotalReturn) },
                ].map((m) => (
                  <div key={m.label} className="bg-slate-800 rounded-lg p-3">
                    <p className="text-xs text-slate-400">{m.label}</p>
                    <p className={`text-lg font-mono font-semibold ${m.color || 'text-slate-100'}`}>{m.value}</p>
                  </div>
                ))}
              </div>

              {/* Equity curve */}
              {equityData.length > 0 && (
                <div className="bg-slate-800 rounded-xl p-4">
                  <p className="text-xs text-slate-400 mb-3">Equity Curve</p>
                  <ResponsiveContainer width="100%" height={220}>
                    <LineChart data={equityData}>
                      <CartesianGrid strokeDasharray="3 3" stroke="#334155" />
                      <XAxis dataKey="time" tick={{ fontSize: 10, fill: '#94a3b8' }}
                        interval="preserveStartEnd" />
                      <YAxis tick={{ fontSize: 10, fill: '#94a3b8' }} width={70}
                        tickFormatter={(v) => '$' + v.toLocaleString()} />
                      <Tooltip
                        contentStyle={{ background: '#1e293b', border: '1px solid #334155', fontSize: 12 }}
                        formatter={(v: number | undefined) => ['$' + (v ?? 0).toLocaleString(), 'Equity']} />
                      <Line type="monotone" dataKey="equity" stroke="#60a5fa"
                        dot={false} strokeWidth={2} />
                    </LineChart>
                  </ResponsiveContainer>
                </div>
              )}

              {/* Trades table */}
              {report.Trades && report.Trades.length > 0 && (
                <div className="bg-slate-800 rounded-xl p-4">
                  <p className="text-xs text-slate-400 mb-3">
                    Trades ({report.Trades.length})
                  </p>
                  <div className="overflow-x-auto">
                    <table className="w-full text-xs">
                      <thead>
                        <tr className="text-slate-400 text-left border-b border-slate-700">
                          <th className="pb-2">Side</th>
                          <th className="pb-2">Entry</th>
                          <th className="pb-2">Exit</th>
                          <th className="pb-2 text-right">Entry $</th>
                          <th className="pb-2 text-right">Exit $</th>
                          <th className="pb-2 text-right">Net PnL</th>
                          <th className="pb-2 text-right">PnL %</th>
                        </tr>
                      </thead>
                      <tbody>
                        {report.Trades.slice(0, 50).map((t, i) => (
                          <tr key={i} className="border-b border-slate-700/50 hover:bg-slate-700/30">
                            <td className={`py-1.5 font-semibold ${t.Side === 'BUY' ? 'text-green-400' : 'text-red-400'}`}>
                              {t.Side}
                            </td>
                            <td className="py-1.5 text-slate-400">
                              {new Date(t.EntryTime).toLocaleDateString()}
                            </td>
                            <td className="py-1.5 text-slate-400">
                              {new Date(t.ExitTime).toLocaleDateString()}
                            </td>
                            <td className="py-1.5 text-right font-mono">{fmt(t.EntryPrice)}</td>
                            <td className="py-1.5 text-right font-mono">{fmt(t.ExitPrice)}</td>
                            <td className={`py-1.5 text-right font-mono ${colorPct(t.NetPnL)}`}>
                              {t.NetPnL >= 0 ? '+' : ''}{fmt(t.NetPnL)}
                            </td>
                            <td className={`py-1.5 text-right font-mono ${colorPct(t.PnLPct)}`}>
                              {pct(t.PnLPct)}
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                    {report.Trades.length > 50 && (
                      <p className="text-xs text-slate-500 mt-2">
                        Showing 50 of {report.Trades.length} trades.
                      </p>
                    )}
                  </div>
                </div>
              )}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
