import { useEffect, useState } from 'react'
import { getPositions, closeEnginePosition } from '../api/trading'
import { useTradeSocket } from '../hooks/useTradeSocket'
import { toast } from '../store/toastStore'

// LiveSnap tracks the most recent strategy status snapshot per engine_id so the
// Positions page can compute pnlR + show SL without a second API call.
interface LiveSnap {
  strat_long_entry?: number
  strat_long_sl?: number
  strat_short_entry?: number
  strat_short_sl?: number
}

interface PositionView {
  symbol: string
  position_side: string  // "" (net/spot), "LONG", "SHORT"
  qty: number
  avg_entry_price: number
  unrealized_pnl: number
  realized_pnl: number
}

interface EnginePositions {
  engine_id: string
  strategy_id: string
  symbol: string
  mode: string
  last_price: number
  cash: number
  equity: number
  positions: PositionView[]
}

export default function Positions() {
  const [engines, setEngines] = useState<EnginePositions[]>([])
  const [loading, setLoading] = useState(true)
  const [apiError, setApiError] = useState<string | null>(null)
  const [closingKey, setClosingKey] = useState<string | null>(null)
  const [liveSnaps, setLiveSnaps] = useState<Record<string, LiveSnap>>({})

  const handleClose = async (engineID: string, symbol: string, side: 'LONG' | 'SHORT', qty: number) => {
    const ok = window.confirm(
      `Close ${side} ${qty} ${symbol} on engine ${engineID}?\n\n` +
      `This places a reduce-only market order on the exchange. ` +
      `The engine continues running.`
    )
    if (!ok) return
    const key = `${engineID}-${side}`
    setClosingKey(key)
    try {
      const r = await closeEnginePosition(engineID, side)
      toast.success(`Closed ${side}: ${r.data.qty} ${r.data.symbol} @ $${r.data.fill_price.toFixed(2)}`)
      refresh()
    } catch (e: any) {
      toast.error(`Close failed: ${e.response?.data?.error || e.message}`)
    } finally {
      setClosingKey(null)
    }
  }

  const refresh = () => {
    setLoading(true)
    getPositions()
      .then((r) => { setEngines(r.data.positions || []); setApiError(null) })
      .catch((e) => setApiError(e.response?.data?.error || 'Failed to load positions'))
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    refresh()
    const id = setInterval(refresh, 5000)
    return () => clearInterval(id)
  }, [])

  // Refresh positions on fill events; cache strategy snapshots for pnlR/SL.
  useTradeSocket((msg: any) => {
    if (msg?.type === 'fill') {
      refresh()
    } else if (msg?.type === 'status' && msg?.data?.engine_id) {
      const d = msg.data
      setLiveSnaps((prev) => ({
        ...prev,
        [d.engine_id]: {
          strat_long_entry:  d.strat_long_entry,
          strat_long_sl:     d.strat_long_sl,
          strat_short_entry: d.strat_short_entry,
          strat_short_sl:    d.strat_short_sl,
        },
      }))
    }
  })

  const totalUnrealized = engines.reduce(
    (sum, e) => sum + e.positions.reduce((s, p) => s + p.unrealized_pnl, 0), 0,
  )

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold">Open Positions</h1>
        <div className="flex items-center gap-4">
          {engines.length > 0 && (
            <span className={`text-sm font-semibold ${totalUnrealized >= 0 ? 'text-green-400' : 'text-red-400'}`}>
              Total Unrealized P&L: {totalUnrealized >= 0 ? '+' : ''}${totalUnrealized.toFixed(4)}
            </span>
          )}
          <button onClick={refresh}
            className="px-3 py-1 text-xs bg-slate-700 hover:bg-slate-600 text-slate-300 rounded transition-colors">
            Refresh
          </button>
        </div>
      </div>

      {apiError && (
        <div className="bg-red-900/30 border border-red-700/50 rounded-lg px-4 py-2 text-red-400 text-sm">
          {apiError}
        </div>
      )}

      {loading && engines.length === 0 ? (
        <p className="text-slate-400 text-sm">Loading...</p>
      ) : engines.length === 0 ? (
        <div className="bg-slate-800 rounded-xl p-6 text-center text-slate-500 text-sm">
          No open positions. Start an engine to see live position data.
        </div>
      ) : (
        engines.map((eng) => (
          <div key={eng.engine_id} className="bg-slate-800 rounded-xl p-5 space-y-3">
            {/* Engine header */}
            <div className="flex items-center justify-between">
              <div>
                <span className="font-semibold text-slate-100">{eng.engine_id}</span>
                <span className={`ml-2 text-xs px-1.5 py-0.5 rounded ${eng.mode === 'live' ? 'bg-green-900/50 text-green-300' : 'bg-slate-600 text-slate-300'}`}>
                  {eng.mode}
                </span>
              </div>
              <div className="text-right text-xs text-slate-400 space-y-0.5">
                <div>Last price: <span className="text-slate-200 font-mono">${eng.last_price.toLocaleString()}</span></div>
                <div>Cash: <span className="text-slate-200 font-mono">${eng.cash.toFixed(2)}</span></div>
                <div>Equity: <span className="text-slate-200 font-mono">${eng.equity.toFixed(2)}</span></div>
              </div>
            </div>

            {eng.positions.length === 0 ? (
              <p className="text-xs text-slate-500">No open positions in this engine.</p>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="text-left text-slate-400 text-xs border-b border-slate-700">
                      <th className="pb-2">Symbol</th>
                      <th className="pb-2">Side</th>
                      <th className="pb-2 text-right">Qty</th>
                      <th className="pb-2 text-right">Avg Entry</th>
                      <th className="pb-2 text-right">Last Price</th>
                      <th className="pb-2 text-right">Unrealized P&L</th>
                      <th className="pb-2 text-right" title="P&L in units of risk (R = entry-to-SL distance). >0 = profit, <0 = loss.">pnlR</th>
                      <th className="pb-2 text-right">SL</th>
                      <th className="pb-2 text-right">Realized P&L</th>
                      <th className="pb-2 text-right">Action</th>
                    </tr>
                  </thead>
                  <tbody>
                    {eng.positions.map((p, i) => {
                      const side = p.position_side === 'LONG' || p.position_side === 'SHORT' ? p.position_side : null
                      const key = `${eng.engine_id}-${side}`
                      const isClosing = closingKey === key
                      // pnlR + SL come from strategy snapshot (WS push), join by engine_id + side
                      const snap = liveSnaps[eng.engine_id]
                      const entry = side === 'LONG' ? snap?.strat_long_entry : side === 'SHORT' ? snap?.strat_short_entry : undefined
                      const sl    = side === 'LONG' ? snap?.strat_long_sl    : side === 'SHORT' ? snap?.strat_short_sl    : undefined
                      let pnlR: number | null = null
                      if (entry && sl && eng.last_price > 0) {
                        const r = Math.abs(entry - sl)
                        if (r > 0) {
                          pnlR = side === 'LONG'
                            ? (eng.last_price - entry) / r
                            : (entry - eng.last_price) / r
                        }
                      }
                      return (
                      <tr key={i} className="border-b border-slate-700/50 hover:bg-slate-700/30">
                        <td className="py-2 font-medium">{p.symbol}</td>
                        <td className="py-2">
                          {p.position_side ? (
                            <span className={`text-xs font-semibold px-1.5 py-0.5 rounded ${p.position_side === 'LONG' ? 'bg-green-900/50 text-green-300' : 'bg-red-900/50 text-red-300'}`}>
                              {p.position_side}
                            </span>
                          ) : (
                            <span className="text-xs text-slate-400">NET</span>
                          )}
                        </td>
                        <td className="py-2 text-right font-mono">{p.qty.toFixed(6)}</td>
                        <td className="py-2 text-right font-mono">${p.avg_entry_price.toFixed(4)}</td>
                        <td className="py-2 text-right font-mono">${eng.last_price.toLocaleString()}</td>
                        <td className={`py-2 text-right font-mono font-semibold ${p.unrealized_pnl >= 0 ? 'text-green-400' : 'text-red-400'}`}>
                          {p.unrealized_pnl >= 0 ? '+' : ''}${p.unrealized_pnl.toFixed(4)}
                        </td>
                        <td className={`py-2 text-right font-mono font-semibold ${pnlR === null ? 'text-slate-500' : pnlR >= 0 ? 'text-green-400' : 'text-red-400'}`}>
                          {pnlR === null ? '—' : `${pnlR >= 0 ? '+' : ''}${pnlR.toFixed(2)}R`}
                        </td>
                        <td className="py-2 text-right font-mono text-slate-300">
                          {sl ? `$${sl.toFixed(2)}` : '—'}
                        </td>
                        <td className={`py-2 text-right font-mono ${p.realized_pnl > 0 ? 'text-green-400' : p.realized_pnl < 0 ? 'text-red-400' : 'text-slate-400'}`}>
                          {p.realized_pnl === 0 ? '—' : `$${p.realized_pnl.toFixed(4)}`}
                        </td>
                        <td className="py-2 text-right">
                          {side ? (
                            <button
                              disabled={isClosing || eng.mode !== 'live'}
                              onClick={() => handleClose(eng.engine_id, p.symbol, side, p.qty)}
                              className="px-2 py-0.5 text-xs bg-red-900/40 hover:bg-red-900/60 disabled:opacity-40 disabled:cursor-not-allowed text-red-200 border border-red-800/40 rounded transition-colors"
                              title={eng.mode !== 'live' ? 'Only live engines support close-position' : `Close ${side} side via reduce-only market order`}
                            >
                              {isClosing ? 'Closing…' : `Close ${side}`}
                            </button>
                          ) : (
                            <span className="text-xs text-slate-600">—</span>
                          )}
                        </td>
                      </tr>
                    )})}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        ))
      )}
      <p className="text-xs text-slate-600 text-right">Auto-refreshes every 5s</p>
    </div>
  )
}
