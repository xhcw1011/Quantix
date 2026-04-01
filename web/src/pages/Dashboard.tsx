import { useEffect, useState } from 'react'
import {
  LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer
} from 'recharts'
import { getEquity, getSummary, getFills } from '../api/trading'
import { useTradeSocket } from '../hooks/useTradeSocket'

interface Snapshot {
  id: number
  equity: number
  cash: number
  unrealized_pnl: number
  realized_pnl: number
  snapshotted_at: string
}

interface Summary {
  equity: number
  cash: number
  unrealized_pnl: number
  realized_pnl: number
  total_fills: number
  win_rate: number
  engine_status: string
  strategy_id: string
}

interface Fill {
  id: number
  symbol: string
  side: string
  qty: number
  price: number
  fee: number
  realized_pnl: number
  filled_at: string
}

function StatCard({ label, value, sub, color = 'text-white' }: {
  label: string; value: string; sub?: string; color?: string
}) {
  return (
    <div className="bg-slate-800 rounded-xl p-4">
      <p className="text-xs text-slate-400 mb-1">{label}</p>
      <p className={`text-xl font-bold ${color}`}>{value}</p>
      {sub && <p className="text-xs text-slate-500 mt-0.5">{sub}</p>}
    </div>
  )
}

export default function Dashboard() {
  const [summary, setSummary] = useState<Summary | null>(null)
  const [snapshots, setSnapshots] = useState<Snapshot[]>([])
  const [fills, setFills] = useState<Fill[]>([])
  const [apiError, setApiError] = useState<string | null>(null)

  useEffect(() => {
    getSummary()
      .then((r) => setSummary(r.data))
      .catch((e) => setApiError(e.response?.data?.error || 'Failed to load summary'))
    getEquity(undefined, 200)
      .then((r) => setSnapshots(r.data.snapshots || []))
      .catch(() => {})
    getFills(10, 0)
      .then((r) => setFills(r.data.fills || []))
      .catch(() => {})

    const interval = setInterval(() => {
      getSummary().then((r) => { setSummary(r.data); setApiError(null) }).catch(() => {})
    }, 30000)
    return () => clearInterval(interval)
  }, [])

  // Real-time WS: update summary equity on equity events, prepend fills on fill events
  useTradeSocket((msg: any) => {
    if (msg?.type === 'equity' && typeof msg.equity === 'number') {
      setSummary((prev) => prev ? { ...prev, equity: msg.equity } : prev)
    } else if (msg?.type === 'fill' && msg.data) {
      setFills((prev) => [msg.data as Fill, ...prev].slice(0, 10))
      // Refresh summary to pick up win_rate / total_fills changes
      getSummary().then((r) => setSummary(r.data)).catch(() => {})
    }
  })

  const chartData = snapshots.map((s) => ({
    time: new Date(s.snapshotted_at).toLocaleTimeString(),
    equity: +s.equity.toFixed(2),
    cash: +s.cash.toFixed(2),
  }))

  const fmt = (n: number) => `$${n.toFixed(2)}`
  const statusColor = summary?.engine_status === 'running' ? 'text-green-400' : 'text-slate-400'

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-bold">Dashboard</h1>

      {apiError && (
        <div className="bg-red-900/30 border border-red-700/50 rounded-lg px-4 py-2 text-red-400 text-sm">
          {apiError}
        </div>
      )}

      {/* Stat cards */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        <StatCard label="Equity" value={fmt(summary?.equity || 0)} />
        <StatCard label="Cash" value={fmt(summary?.cash || 0)} />
        <StatCard
          label="Realized P&L"
          value={fmt(summary?.realized_pnl || 0)}
          color={summary?.realized_pnl && summary.realized_pnl >= 0 ? 'text-green-400' : 'text-red-400'}
        />
        <StatCard
          label="Win Rate"
          value={`${(summary?.win_rate || 0).toFixed(1)}%`}
          sub={`${summary?.total_fills || 0} trades`}
        />
      </div>

      {/* Engine status */}
      <div className="bg-slate-800 rounded-xl p-4 flex items-center gap-3">
        <div className={`w-2.5 h-2.5 rounded-full ${summary?.engine_status === 'running' ? 'bg-green-400 animate-pulse' : 'bg-slate-500'}`} />
        <span className={`text-sm font-medium ${statusColor}`}>
          Engine: {summary?.engine_status || 'stopped'}
        </span>
        {summary?.strategy_id && (
          <span className="text-xs text-slate-400 ml-2">Strategy: {summary.strategy_id}</span>
        )}
      </div>

      {/* Equity chart */}
      <div className="bg-slate-800 rounded-xl p-4">
        <h2 className="text-sm font-semibold text-slate-300 mb-4">Equity Curve</h2>
        {chartData.length === 0 ? (
          <p className="text-slate-500 text-sm text-center py-8">
            No equity data yet. Start the engine to begin recording.
          </p>
        ) : (
          <ResponsiveContainer width="100%" height={240}>
            <LineChart data={chartData}>
              <CartesianGrid strokeDasharray="3 3" stroke="#334155" />
              <XAxis dataKey="time" tick={{ fontSize: 11, fill: '#94a3b8' }} />
              <YAxis tick={{ fontSize: 11, fill: '#94a3b8' }} />
              <Tooltip
                contentStyle={{ background: '#1e293b', border: '1px solid #334155', borderRadius: 8 }}
                labelStyle={{ color: '#94a3b8' }}
              />
              <Line type="monotone" dataKey="equity" stroke="#3b82f6" strokeWidth={2} dot={false} name="Equity" />
              <Line type="monotone" dataKey="cash" stroke="#10b981" strokeWidth={1.5} dot={false} name="Cash" strokeDasharray="4 4" />
            </LineChart>
          </ResponsiveContainer>
        )}
      </div>

      {/* Recent fills */}
      <div className="bg-slate-800 rounded-xl p-4">
        <h2 className="text-sm font-semibold text-slate-300 mb-4">Recent Fills</h2>
        {fills.length === 0 ? (
          <p className="text-slate-500 text-sm">No fills recorded yet.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-slate-400 text-xs border-b border-slate-700">
                  <th className="pb-2">Symbol</th>
                  <th className="pb-2">Side</th>
                  <th className="pb-2 text-right">Qty</th>
                  <th className="pb-2 text-right">Price</th>
                  <th className="pb-2 text-right">P&L</th>
                  <th className="pb-2 text-right">Time</th>
                </tr>
              </thead>
              <tbody>
                {fills.map((f) => (
                  <tr key={f.id} className="border-b border-slate-700/50">
                    <td className="py-1.5 font-medium">{f.symbol}</td>
                    <td className={`py-1.5 font-medium ${f.side === 'BUY' ? 'text-green-400' : 'text-red-400'}`}>
                      {f.side}
                    </td>
                    <td className="py-1.5 text-right">{f.qty.toFixed(6)}</td>
                    <td className="py-1.5 text-right">{f.price.toFixed(2)}</td>
                    <td className={`py-1.5 text-right ${f.realized_pnl >= 0 ? 'text-green-400' : 'text-red-400'}`}>
                      {f.realized_pnl === 0 ? '—' : `$${f.realized_pnl.toFixed(2)}`}
                    </td>
                    <td className="py-1.5 text-right text-slate-400">
                      {new Date(f.filled_at).toLocaleString()}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  )
}
