import { useEffect, useState } from 'react'
import { getFills } from '../api/trading'
import { FILTER_INPUT_CLASS } from '../lib/inputStyles'
import { isClosingSide, actionLabel, derivedEntryPrice } from '../lib/positionFormat'

interface Fill {
  id: number
  strategy_id: string
  symbol: string
  side: string
  qty: number
  price: number
  fee: number
  realized_pnl: number
  exchange_order_id: string
  mode: string
  filled_at: string
  position_side: string // "LONG" | "SHORT" | "" (hedge mode direction)
}

const inputCls = FILTER_INPUT_CLASS

// 一条成交记录只会是开仓或平仓之一(不会同时是两者)。平仓记录额外反推出对应的
// (均)开仓价,不用再去翻找是哪一笔开仓单跟它配对的。
const entryExitPrice = (f: Fill): { entry: string; exit: string } => {
  const priceStr = f.price.toFixed(2)
  if (!isClosingSide(f.side, f.position_side)) return { entry: priceStr, exit: '—' }
  const derived = derivedEntryPrice(f.position_side, f.price, f.qty, f.fee, f.realized_pnl)
  return { entry: derived != null ? `${derived.toFixed(2)}(均价)` : '—', exit: priceStr }
}

export default function Fills() {
  const [fills, setFills] = useState<Fill[]>([])
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

  // Reset to first page when any filter changes
  useEffect(() => { setOffset(0) }, [symbol, strategy, mode, from, to])

  useEffect(() => {
    setLoading(true)
    setApiError(null)
    getFills(limit, offset, symbol, strategy, mode, from, to)
      .then((r) => setFills(r.data.fills || []))
      .catch((e) => setApiError(e.response?.data?.error || 'Failed to load fills'))
      .finally(() => setLoading(false))
  }, [offset, symbol, strategy, mode, from, to])

  const clearFilters = () => {
    setSymbol(''); setStrategy(''); setMode(''); setFrom(''); setTo('')
  }
  const hasFilters = symbol || strategy || mode || from || to

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-bold">Fill Records</h1>

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
          <p className="text-slate-400 text-sm">加载中…</p>
        ) : fills.length === 0 ? (
          <p className="text-slate-500 text-sm">No fills found{hasFilters ? ' for the selected filters' : ''}.</p>
        ) : (
          <>
            {/* Table — sm and up. */}
            <div className="hidden sm:block overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="text-left text-slate-400 text-xs border-b border-slate-700">
                    <th className="pb-2">ID</th>
                    <th className="pb-2">Strategy</th>
                    <th className="pb-2">Symbol</th>
                    <th className="pb-2">Side</th>
                    <th className="pb-2 text-right">Qty</th>
                    <th className="pb-2 text-right">开仓价格</th>
                    <th className="pb-2 text-right">平仓价格</th>
                    <th className="pb-2 text-right">Fee</th>
                    <th className="pb-2 text-right">Realized P&L</th>
                    <th className="pb-2">Mode</th>
                    <th className="pb-2 text-right">Time</th>
                  </tr>
                </thead>
                <tbody>
                  {fills.map((f) => {
                    const { entry, exit } = entryExitPrice(f)
                    return (
                    <tr key={f.id} className="border-b border-slate-700/50 hover:bg-slate-700/30">
                      <td className="py-2 text-slate-400 text-xs">{f.id}</td>
                      <td className="py-2 text-xs text-slate-300">{f.strategy_id}</td>
                      <td className="py-2 font-medium">{f.symbol}</td>
                      <td className={`py-2 font-semibold ${f.side === 'BUY' ? 'text-green-400' : 'text-red-400'}`}>
                        {actionLabel(f.side, f.position_side)}
                      </td>
                      <td className="py-2 text-right font-mono">{f.qty.toFixed(6)}</td>
                      <td className="py-2 text-right font-mono">{entry}</td>
                      <td className="py-2 text-right font-mono">{exit}</td>
                      <td className="py-2 text-right font-mono text-slate-400">{f.fee.toFixed(4)}</td>
                      <td className={`py-2 text-right font-mono font-semibold ${f.realized_pnl > 0 ? 'text-green-400' : f.realized_pnl < 0 ? 'text-red-400' : 'text-slate-400'}`}>
                        {f.realized_pnl === 0 ? '—' : `$${f.realized_pnl.toFixed(4)}`}
                      </td>
                      <td className="py-2">
                        <span className={`text-xs px-1.5 py-0.5 rounded ${f.mode === 'live' ? 'bg-green-900/50 text-green-300' : 'bg-slate-600 text-slate-300'}`}>
                          {f.mode}
                        </span>
                      </td>
                      <td className="py-2 text-right text-xs text-slate-400">
                        {new Date(f.filled_at).toLocaleString()}
                      </td>
                    </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>

            {/* Cards — below sm. */}
            <div className="sm:hidden space-y-2">
              {fills.map((f) => {
                const { entry, exit } = entryExitPrice(f)
                return (
                <div key={f.id} className="bg-slate-900/40 border border-slate-700 rounded-lg p-3 text-xs">
                  <div className="flex items-center justify-between mb-1.5">
                    <span className="font-medium text-slate-200">{f.symbol}</span>
                    <span className={`px-1.5 py-0.5 rounded ${f.mode === 'live' ? 'bg-green-900/50 text-green-300' : 'bg-slate-600 text-slate-300'}`}>
                      {f.mode}
                    </span>
                  </div>
                  <div className="flex items-center gap-1.5 mb-2 flex-wrap">
                    <span className={`font-semibold ${f.side === 'BUY' ? 'text-green-400' : 'text-red-400'}`}>{actionLabel(f.side, f.position_side)}</span>
                    <span className="text-slate-400">· {f.strategy_id}</span>
                    <span className="text-slate-500">#{f.id}</span>
                  </div>
                  <div className="grid grid-cols-2 gap-y-1.5 gap-x-3 text-slate-300">
                    <div><span className="block text-slate-500">Qty</span><span className="font-mono">{f.qty.toFixed(6)}</span></div>
                    <div><span className="block text-slate-500">开仓价格</span><span className="font-mono">{entry}</span></div>
                    <div><span className="block text-slate-500">平仓价格</span><span className="font-mono">{exit}</span></div>
                    <div><span className="block text-slate-500">Fee</span><span className="font-mono">{f.fee.toFixed(4)}</span></div>
                    <div><span className="block text-slate-500">Realized P&L</span><span className={`font-mono font-semibold ${f.realized_pnl > 0 ? 'text-green-400' : f.realized_pnl < 0 ? 'text-red-400' : 'text-slate-400'}`}>{f.realized_pnl === 0 ? '—' : `$${f.realized_pnl.toFixed(4)}`}</span></div>
                  </div>
                  <div className="mt-1.5 text-slate-500">{new Date(f.filled_at).toLocaleString()}</div>
                </div>
                )
              })}
            </div>
          </>
        )}
        {/* Pagination */}
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
            disabled={fills.length < limit}
            className="px-3 py-1 text-sm bg-slate-700 rounded disabled:opacity-40 hover:bg-slate-600"
          >
            Next →
          </button>
        </div>
      </div>
    </div>
  )
}
