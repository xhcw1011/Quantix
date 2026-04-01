import { useEffect, useState } from 'react'
import { getOrders } from '../api/trading'

interface Order {
  id: string
  exchange_id: string
  symbol: string
  side: string
  type: string
  status: string
  quantity: number
  price: number
  filled_quantity: number
  avg_fill_price: number
  commission: number
  strategy_id: string
  mode: string
  created_at: string
}

const statusColor: Record<string, string> = {
  FILLED: 'bg-green-900/50 text-green-300',
  CANCELLED: 'bg-slate-600 text-slate-300',
  REJECTED: 'bg-red-900/50 text-red-300',
  PENDING: 'bg-yellow-900/50 text-yellow-300',
  OPEN: 'bg-blue-900/50 text-blue-300',
}

const inputCls = 'bg-slate-700 border border-slate-600 rounded px-2 py-1 text-sm text-slate-100 focus:outline-none focus:ring-1 focus:ring-blue-500'

export default function Orders() {
  const [orders, setOrders] = useState<Order[]>([])
  const [loading, setLoading] = useState(true)
  const [offset, setOffset] = useState(0)
  const [apiError, setApiError] = useState<string | null>(null)
  const limit = 50

  // Filters
  const [symbol, setSymbol] = useState('')
  const [strategy, setStrategy] = useState('')
  const [mode, setMode] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')

  useEffect(() => { setOffset(0) }, [symbol, strategy, mode, from, to])

  useEffect(() => {
    setLoading(true)
    setApiError(null)
    getOrders(limit, offset, symbol, strategy, mode, from, to)
      .then((r) => setOrders(r.data.orders || []))
      .catch((e) => setApiError(e.response?.data?.error || 'Failed to load orders'))
      .finally(() => setLoading(false))
  }, [offset, symbol, strategy, mode, from, to])

  const clearFilters = () => {
    setSymbol(''); setStrategy(''); setMode(''); setFrom(''); setTo('')
  }
  const hasFilters = symbol || strategy || mode || from || to

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-bold">Order History</h1>

      {apiError && (
        <div className="bg-red-900/30 border border-red-700/50 rounded-lg px-4 py-2 text-red-400 text-sm">
          {apiError}
        </div>
      )}

      {/* Filter bar */}
      <div className="bg-slate-800 rounded-xl p-4 flex flex-wrap items-end gap-3">
        <div className="flex flex-col gap-1">
          <label className="text-xs text-slate-400">Symbol</label>
          <input className={inputCls} placeholder="e.g. BTCUSDT" value={symbol}
            onChange={e => setSymbol(e.target.value.toUpperCase())} />
        </div>
        <div className="flex flex-col gap-1">
          <label className="text-xs text-slate-400">Strategy</label>
          <input className={inputCls} placeholder="e.g. macross" value={strategy}
            onChange={e => setStrategy(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1">
          <label className="text-xs text-slate-400">Mode</label>
          <select className={inputCls} value={mode} onChange={e => setMode(e.target.value)}>
            <option value="">All</option>
            <option value="live">Live</option>
            <option value="paper">Paper</option>
          </select>
        </div>
        <div className="flex flex-col gap-1">
          <label className="text-xs text-slate-400">From</label>
          <input type="date" className={inputCls} value={from} onChange={e => setFrom(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1">
          <label className="text-xs text-slate-400">To</label>
          <input type="date" className={inputCls} value={to} onChange={e => setTo(e.target.value)} />
        </div>
        {hasFilters && (
          <button onClick={clearFilters}
            className="px-3 py-1 text-xs bg-slate-600 hover:bg-slate-500 text-slate-300 rounded transition-colors">
            Clear
          </button>
        )}
      </div>

      <div className="bg-slate-800 rounded-xl p-4">
        {loading ? (
          <p className="text-slate-400 text-sm">Loading...</p>
        ) : orders.length === 0 ? (
          <p className="text-slate-500 text-sm">
            {hasFilters ? 'No orders match the selected filters.' : 'No orders found. Orders are linked to your user account when the engine is started via the API.'}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-slate-400 text-xs border-b border-slate-700">
                  <th className="pb-2">Symbol</th>
                  <th className="pb-2">Side</th>
                  <th className="pb-2">Type</th>
                  <th className="pb-2">Status</th>
                  <th className="pb-2 text-right">Qty</th>
                  <th className="pb-2 text-right">Filled Qty</th>
                  <th className="pb-2 text-right">Avg Price</th>
                  <th className="pb-2 text-right">Commission</th>
                  <th className="pb-2">Strategy</th>
                  <th className="pb-2">Mode</th>
                  <th className="pb-2 text-right">Time</th>
                </tr>
              </thead>
              <tbody>
                {orders.map((o) => (
                  <tr key={o.id} className="border-b border-slate-700/50 hover:bg-slate-700/30">
                    <td className="py-2 font-medium">{o.symbol}</td>
                    <td className={`py-2 font-semibold ${o.side === 'BUY' ? 'text-green-400' : 'text-red-400'}`}>
                      {o.side}
                    </td>
                    <td className="py-2 text-slate-400">{o.type}</td>
                    <td className="py-2">
                      <span className={`text-xs px-1.5 py-0.5 rounded ${statusColor[o.status] || 'bg-slate-600 text-slate-300'}`}>
                        {o.status}
                      </span>
                    </td>
                    <td className="py-2 text-right font-mono">{o.quantity.toFixed(6)}</td>
                    <td className="py-2 text-right font-mono">{o.filled_quantity.toFixed(6)}</td>
                    <td className="py-2 text-right font-mono">{o.avg_fill_price > 0 ? o.avg_fill_price.toFixed(2) : '—'}</td>
                    <td className="py-2 text-right font-mono text-slate-400">{o.commission.toFixed(4)}</td>
                    <td className="py-2 text-xs text-slate-300">{o.strategy_id}</td>
                    <td className="py-2">
                      <span className={`text-xs px-1.5 py-0.5 rounded ${o.mode === 'live' ? 'bg-green-900/50 text-green-300' : 'bg-slate-600 text-slate-300'}`}>
                        {o.mode}
                      </span>
                    </td>
                    <td className="py-2 text-right text-xs text-slate-400">
                      {new Date(o.created_at).toLocaleString()}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <div className="flex gap-2 mt-4">
          <button
            onClick={() => setOffset(Math.max(0, offset - limit))}
            disabled={offset === 0}
            className="px-3 py-1 text-sm bg-slate-700 rounded disabled:opacity-40 hover:bg-slate-600"
          >
            ← Prev
          </button>
          <button
            onClick={() => setOffset(offset + limit)}
            disabled={orders.length < limit}
            className="px-3 py-1 text-sm bg-slate-700 rounded disabled:opacity-40 hover:bg-slate-600"
          >
            Next →
          </button>
        </div>
      </div>
    </div>
  )
}
