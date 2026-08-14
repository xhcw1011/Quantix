import { useEffect, useState } from 'react'
import { getFills, listEngines, getPositions } from '../api/trading'
import { useTradeSocket } from '../hooks/useTradeSocket'
import { actionLabel } from '../lib/positionFormat'

interface EngineInfo {
  engine_id: string
  strategy_id: string
  symbol: string
  mode: string
  running: boolean
}

interface PositionView {
  symbol: string
  position_side: string
  qty: number
  avg_entry_price: number
  unrealized_pnl: number
  realized_pnl: number
}

interface EnginePositions {
  engine_id: string
  equity: number
  positions: PositionView[]
}

interface Fill {
  id: number
  strategy_id: string
  symbol: string
  side: string
  position_side: string // "LONG" | "SHORT" | "" (hedge mode direction)
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
  const [engines, setEngines] = useState<EngineInfo[]>([])
  const [positionsByEngine, setPositionsByEngine] = useState<Record<string, EnginePositions>>({})
  const [fills, setFills] = useState<Fill[]>([])
  const [apiError, setApiError] = useState<string | null>(null)

  // 引擎列表+持仓/权益 —— 跟 /engine 页面用的是同一套接口,统计口径永远跟引擎页保持一致,
  // 不再走那个"随便挑一个引擎代表全局状态"的旧版单引擎 /summary 接口。
  const refresh = () => {
    listEngines()
      .then((r) => { setEngines(r.data || []); setApiError(null) })
      .catch((e) => setApiError(e.response?.data?.error || '加载引擎列表失败'))
    getPositions()
      .then((r) => {
        const byEngine: Record<string, EnginePositions> = {}
        for (const eng of (r.data.positions || []) as EnginePositions[]) {
          byEngine[eng.engine_id] = eng
        }
        setPositionsByEngine(byEngine)
      })
      .catch(() => {})
  }

  useEffect(() => {
    refresh()
    getFills(200, 0).then((r) => setFills(r.data.fills || [])).catch(() => {})
    const interval = setInterval(refresh, 10000)
    return () => clearInterval(interval)
  }, [])

  // Real-time WS: refresh on every fill so the stat cards and recent-activity
  // feed stay current without waiting for the next 10s poll.
  useTradeSocket((msg: any) => {
    if (msg?.type === 'fill' && msg.data) {
      setFills((prev) => [msg.data as Fill, ...prev].slice(0, 200))
      refresh()
    }
  })

  const runningEngines = engines.filter((e) => e.running)
  const totalEquity = runningEngines.reduce((sum, e) => sum + (positionsByEngine[e.engine_id]?.equity ?? 0), 0)
  const totalUnrealized = runningEngines.reduce((sum, e) => {
    const positions = positionsByEngine[e.engine_id]?.positions ?? []
    return sum + positions.reduce((s, p) => s + p.unrealized_pnl, 0)
  }, 0)

  const totalFills = fills.length
  const wins = fills.filter((f) => f.realized_pnl > 0).length
  const winRate = totalFills > 0 ? (wins / totalFills) * 100 : 0

  const fmt = (n: number) => `$${n.toFixed(2)}`
  const recentFills = fills.slice(0, 10)

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-bold">总览</h1>

      {apiError && (
        <div className="bg-red-900/30 border border-red-700/50 rounded-lg px-4 py-2 text-red-400 text-sm">
          {apiError}
        </div>
      )}

      {/* Stat cards — aggregated across every currently running engine */}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
        <StatCard label="运行中引擎" value={`${runningEngines.length}`} sub={`共 ${engines.length} 个(含已停止)`} />
        <StatCard label="总权益" value={fmt(totalEquity)} />
        <StatCard
          label="总浮动盈亏"
          value={fmt(totalUnrealized)}
          color={totalUnrealized >= 0 ? 'text-green-400' : 'text-red-400'}
        />
        <StatCard
          label="胜率"
          value={`${winRate.toFixed(1)}%`}
          sub={`最近 ${totalFills} 笔成交`}
        />
      </div>

      {/* Recent fills */}
      <div className="bg-slate-800 rounded-xl p-4">
        <h2 className="text-sm font-semibold text-slate-300 mb-4">最近成交</h2>
        {recentFills.length === 0 ? (
          <p className="text-slate-500 text-sm">暂无成交记录。</p>
        ) : (
          <>
            {/* Table — sm and up. */}
            <div className="hidden sm:block overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="text-left text-slate-400 text-xs border-b border-slate-700">
                    <th className="pb-2">引擎</th>
                    <th className="pb-2">交易对</th>
                    <th className="pb-2">方向</th>
                    <th className="pb-2 text-right">数量</th>
                    <th className="pb-2 text-right">价格</th>
                    <th className="pb-2 text-right">盈亏</th>
                    <th className="pb-2 text-right">时间</th>
                  </tr>
                </thead>
                <tbody>
                  {recentFills.map((f) => (
                    <tr key={f.id} className="border-b border-slate-700/50">
                      <td className="py-1.5 text-xs text-slate-400">{f.strategy_id}</td>
                      <td className="py-1.5 font-medium">{f.symbol}</td>
                      <td className={`py-1.5 font-medium ${f.side === 'BUY' ? 'text-green-400' : 'text-red-400'}`}>
                        {actionLabel(f.side, f.position_side)}
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

            {/* Cards — below sm. */}
            <div className="sm:hidden space-y-2">
              {recentFills.map((f) => (
                <div key={f.id} className="bg-slate-900/40 border border-slate-700 rounded-lg p-3 text-xs">
                  <div className="flex items-center justify-between mb-1.5">
                    <span className="font-medium text-slate-200">{f.symbol}</span>
                    <span className={`font-semibold ${f.side === 'BUY' ? 'text-green-400' : 'text-red-400'}`}>{actionLabel(f.side, f.position_side)}</span>
                  </div>
                  <div className="text-slate-400 mb-2">{f.strategy_id}</div>
                  <div className="grid grid-cols-2 gap-y-1.5 gap-x-3 text-slate-300">
                    <div><span className="block text-slate-500">数量</span>{f.qty.toFixed(6)}</div>
                    <div><span className="block text-slate-500">价格</span>{f.price.toFixed(2)}</div>
                    <div><span className="block text-slate-500">盈亏</span><span className={f.realized_pnl >= 0 ? 'text-green-400' : 'text-red-400'}>{f.realized_pnl === 0 ? '—' : `$${f.realized_pnl.toFixed(2)}`}</span></div>
                  </div>
                  <div className="mt-1.5 text-slate-500">{new Date(f.filled_at).toLocaleString()}</div>
                </div>
              ))}
            </div>
          </>
        )}
      </div>
    </div>
  )
}
