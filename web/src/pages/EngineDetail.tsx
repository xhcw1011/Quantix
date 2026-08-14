import { useEffect, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import {
  LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer,
} from 'recharts'
import { listEngines, listCredentials, getOrders, getFills, getEquity, getPositions } from '../api/trading'
import { strategyLabel } from '../constants/strategies'
import StatusBadge from '../components/StatusBadge'
import LogViewer from '../components/LogViewer'
import PositionRows, { type PositionView } from '../components/PositionRows'
import { useTradeSocket } from '../hooks/useTradeSocket'
import { isClosingSide, actionLabel, derivedEntryPrice } from '../lib/positionFormat'

// One place for everything about a single engine — order records, fills, equity
// curve, logs — instead of hunting across four separate global pages and mentally
// filtering by strategy_id each time.

interface EngineInfo {
  engine_id: string
  credential_id: number
  strategy_id: string
  symbol: string
  interval: string
  mode: string
  leverage?: number
  running: boolean
  started_at: string
  error?: string
}

interface Credential {
  id: number
  testnet: boolean
  demo: boolean
}

interface Order {
  id: string
  symbol: string
  side: string
  type: string
  status: string
  quantity: number
  filled_quantity: number
  avg_fill_price: number
  commission: number
  created_at: string
  position_side: string // "LONG" | "SHORT" | "" (hedge mode direction)
  stop_price: number // stop trigger price (STOP_MARKET / STOP_LIMIT only); 0 otherwise
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
  position_side: string // "LONG" | "SHORT" | "" (hedge mode direction)
}

// 一条记录只会是开仓或平仓之一(不会同时是两者)。qty/fee/realizedPnl 只有成交记录
// 才有——传了就能把平仓记录反推出对应的(均)开仓价,不用再去翻找是哪一笔开仓单跟它
// 配对的;订单记录没有盈亏数据,反推不出来就还是显示 '—'。
function entryExitPrice(
  side: string, positionSide: string, filledPrice: number,
  qty?: number, fee?: number, realizedPnl?: number,
): { entry: string; exit: string } {
  if (filledPrice <= 0) return { entry: '—', exit: '—' }
  const priceStr = filledPrice.toFixed(2)
  if (!isClosingSide(side, positionSide)) return { entry: priceStr, exit: '—' }
  if (qty != null && fee != null && realizedPnl != null) {
    const derived = derivedEntryPrice(positionSide, filledPrice, qty, fee, realizedPnl)
    if (derived != null) return { entry: `${derived.toFixed(2)}(均价)`, exit: priceStr }
  }
  return { entry: '—', exit: priceStr }
}

interface Snapshot {
  equity: number
  cash: number
  snapshotted_at: string
}

interface EnginePositions {
  engine_id: string
  last_price: number
  positions: PositionView[]
}

function moneyBadge(eng: EngineInfo, credById: Record<number, Credential>) {
  if (eng.mode === 'paper') return { text: '回测/模拟', cls: 'bg-slate-600 text-slate-300' }
  const c = credById[eng.credential_id]
  if (!c) return { text: '实时', cls: 'bg-slate-600 text-slate-300' }
  if (c.testnet || c.demo) return { text: '模拟盘', cls: 'bg-blue-900/50 text-blue-300' }
  return { text: '真钱', cls: 'bg-red-900/50 text-red-300' }
}

type Tab = 'position' | 'orders' | 'fills' | 'equity' | 'logs'
const TAB_LABEL: Record<Tab, string> = {
  position: '持仓', orders: '订单记录', fills: '成交明细', equity: '权益曲线', logs: '日志',
}

// 之前只有引擎列表页的卡片上能看持仓/平仓,进了某个引擎的详情页反而看不到、也不能平——
// 这里补上同一份 PositionRows,不用再退回列表页操作。
function PositionTab({ engineId, mode }: { engineId: string; mode: string }) {
  const [enginePositions, setEnginePositions] = useState<EnginePositions | null>(null)
  const [loading, setLoading] = useState(true)

  const refresh = () => {
    getPositions()
      .then((r) => {
        const found = ((r.data.positions || []) as EnginePositions[]).find((e) => e.engine_id === engineId)
        setEnginePositions(found ?? null)
      })
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    refresh()
    const t = setInterval(refresh, 5000)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [engineId])

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  useTradeSocket((msg: any) => {
    if (msg?.type === 'fill') refresh()
  })

  if (loading) {
    return <p className="text-slate-400 text-sm">加载中...</p>
  }
  const positions = enginePositions?.positions ?? []
  if (positions.length === 0) {
    return (
      <div className="bg-slate-800 rounded-xl p-4 text-slate-500 text-sm">
        当前无持仓。
      </div>
    )
  }
  return (
    <div className="bg-slate-800 rounded-xl p-4">
      <PositionRows
        engineId={engineId}
        mode={mode}
        lastPrice={enginePositions?.last_price ?? 0}
        positions={positions}
        onClosed={refresh}
      />
    </div>
  )
}

function OrdersTab({ engineId }: { engineId: string }) {
  const [orders, setOrders] = useState<Order[]>([])
  const [offset, setOffset] = useState(0)
  const [loading, setLoading] = useState(true)
  const limit = 50

  useEffect(() => {
    setLoading(true)
    getOrders(limit, offset, '', engineId)
      .then((r) => setOrders(r.data.orders || []))
      .finally(() => setLoading(false))
  }, [offset, engineId])

  return (
    <div className="bg-slate-800 rounded-xl p-4">
      {loading ? (
        <p className="text-slate-400 text-sm">加载中...</p>
      ) : orders.length === 0 ? (
        <p className="text-slate-500 text-sm">暂无订单。</p>
      ) : (
        <>
          {/* Table — sm and up. */}
          <div className="hidden sm:block overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-slate-400 text-xs border-b border-slate-700">
                  <th className="pb-2">交易对</th>
                  <th className="pb-2">方向</th>
                  <th className="pb-2">类型</th>
                  <th className="pb-2">状态</th>
                  <th className="pb-2 text-right">数量</th>
                  <th className="pb-2 text-right">已成交</th>
                  <th className="pb-2 text-right">开仓价格</th>
                  <th className="pb-2 text-right">平仓价格</th>
                  <th className="pb-2 text-right">止损价</th>
                  <th className="pb-2 text-right">手续费</th>
                  <th className="pb-2 text-right">时间</th>
                </tr>
              </thead>
              <tbody>
                {orders.map((o) => {
                  const { entry, exit } = entryExitPrice(o.side, o.position_side, o.avg_fill_price)
                  return (
                  <tr key={o.id} className="border-b border-slate-700/50 hover:bg-slate-700/30">
                    <td className="py-2 font-medium">{o.symbol}</td>
                    <td className={`py-2 font-semibold ${o.side === 'BUY' ? 'text-green-400' : 'text-red-400'}`}>
                      {actionLabel(o.side, o.position_side)}
                    </td>
                    <td className="py-2 text-slate-400">{o.type}</td>
                    <td className="py-2">
                      <StatusBadge status={o.status} />
                    </td>
                    <td className="py-2 text-right font-mono">{o.quantity.toFixed(6)}</td>
                    <td className="py-2 text-right font-mono">{o.filled_quantity.toFixed(6)}</td>
                    <td className="py-2 text-right font-mono">{entry}</td>
                    <td className="py-2 text-right font-mono">{exit}</td>
                    <td className="py-2 text-right font-mono text-slate-400">{o.stop_price > 0 ? o.stop_price.toFixed(2) : '—'}</td>
                    <td className="py-2 text-right font-mono text-slate-400">{o.commission.toFixed(4)}</td>
                    <td className="py-2 text-right text-xs text-slate-400">{new Date(o.created_at).toLocaleString()}</td>
                  </tr>
                  )
                })}
              </tbody>
            </table>
          </div>

          {/* Cards — below sm. */}
          <div className="sm:hidden space-y-2">
            {orders.map((o) => {
              const { entry, exit } = entryExitPrice(o.side, o.position_side, o.avg_fill_price)
              return (
              <div key={o.id} className="bg-slate-900/40 border border-slate-700 rounded-lg p-3 text-xs">
                <div className="flex items-center justify-between mb-1.5">
                  <span className="font-medium text-slate-200">{o.symbol}</span>
                  <StatusBadge status={o.status} />
                </div>
                <div className="flex items-center gap-1.5 mb-2">
                  <span className={`font-semibold ${o.side === 'BUY' ? 'text-green-400' : 'text-red-400'}`}>{actionLabel(o.side, o.position_side)}</span>
                  <span className="text-slate-400">· {o.type}</span>
                </div>
                <div className="grid grid-cols-2 gap-y-1.5 gap-x-3 text-slate-300">
                  <div><span className="block text-slate-500">数量</span><span className="font-mono">{o.quantity.toFixed(6)}</span></div>
                  <div><span className="block text-slate-500">已成交</span><span className="font-mono">{o.filled_quantity.toFixed(6)}</span></div>
                  <div><span className="block text-slate-500">开仓价格</span><span className="font-mono">{entry}</span></div>
                  <div><span className="block text-slate-500">平仓价格</span><span className="font-mono">{exit}</span></div>
                  <div><span className="block text-slate-500">止损价</span><span className="font-mono">{o.stop_price > 0 ? o.stop_price.toFixed(2) : '—'}</span></div>
                  <div><span className="block text-slate-500">手续费</span><span className="font-mono">{o.commission.toFixed(4)}</span></div>
                </div>
                <div className="mt-1.5 text-slate-500">{new Date(o.created_at).toLocaleString()}</div>
              </div>
              )
            })}
          </div>
        </>
      )}
      <div className="flex gap-2 mt-4">
        <button onClick={() => setOffset(Math.max(0, offset - limit))} disabled={offset === 0}
          className="px-3 py-1 text-sm bg-slate-700 rounded disabled:opacity-40 hover:bg-slate-600">← 上一页</button>
        <button onClick={() => setOffset(offset + limit)} disabled={orders.length < limit}
          className="px-3 py-1 text-sm bg-slate-700 rounded disabled:opacity-40 hover:bg-slate-600">下一页 →</button>
      </div>
    </div>
  )
}

function FillsTab({ engineId }: { engineId: string }) {
  const [fills, setFills] = useState<Fill[]>([])
  const [offset, setOffset] = useState(0)
  const [loading, setLoading] = useState(true)
  const limit = 50

  useEffect(() => {
    setLoading(true)
    getFills(limit, offset, '', engineId)
      .then((r) => setFills(r.data.fills || []))
      .finally(() => setLoading(false))
  }, [offset, engineId])

  const totalRealized = fills.reduce((s, f) => s + f.realized_pnl, 0)
  const totalFee = fills.reduce((s, f) => s + f.fee, 0)

  return (
    <div className="space-y-3">
      <div className="grid grid-cols-2 md:grid-cols-3 gap-3">
        <div className="bg-slate-800 rounded-xl p-4">
          <p className="text-xs text-slate-400 mb-1">本页已实现盈亏</p>
          <p className={`text-lg font-bold ${totalRealized >= 0 ? 'text-green-400' : 'text-red-400'}`}>${totalRealized.toFixed(2)}</p>
        </div>
        <div className="bg-slate-800 rounded-xl p-4">
          <p className="text-xs text-slate-400 mb-1">本页手续费</p>
          <p className="text-lg font-bold text-slate-300">${totalFee.toFixed(2)}</p>
        </div>
        <div className="bg-slate-800 rounded-xl p-4">
          <p className="text-xs text-slate-400 mb-1">本页净盈亏</p>
          <p className={`text-lg font-bold ${totalRealized - totalFee >= 0 ? 'text-green-400' : 'text-red-400'}`}>${(totalRealized - totalFee).toFixed(2)}</p>
        </div>
      </div>
      <div className="bg-slate-800 rounded-xl p-4">
        {loading ? (
          <p className="text-slate-400 text-sm">加载中...</p>
        ) : fills.length === 0 ? (
          <p className="text-slate-500 text-sm">暂无成交。</p>
        ) : (
          <>
            {/* Table — sm and up. */}
            <div className="hidden sm:block overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="text-left text-slate-400 text-xs border-b border-slate-700">
                    <th className="pb-2">交易对</th>
                    <th className="pb-2">方向</th>
                    <th className="pb-2 text-right">数量</th>
                    <th className="pb-2 text-right">开仓价格</th>
                    <th className="pb-2 text-right">平仓价格</th>
                    <th className="pb-2 text-right">手续费</th>
                    <th className="pb-2 text-right">已实现盈亏</th>
                    <th className="pb-2 text-right">时间</th>
                  </tr>
                </thead>
                <tbody>
                  {fills.map((f) => {
                    const { entry, exit } = entryExitPrice(f.side, f.position_side, f.price, f.qty, f.fee, f.realized_pnl)
                    return (
                    <tr key={f.id} className="border-b border-slate-700/50 hover:bg-slate-700/30">
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
                      <td className="py-2 text-right text-xs text-slate-400">{new Date(f.filled_at).toLocaleString()}</td>
                    </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>

            {/* Cards — below sm. */}
            <div className="sm:hidden space-y-2">
              {fills.map((f) => {
                const { entry, exit } = entryExitPrice(f.side, f.position_side, f.price, f.qty, f.fee, f.realized_pnl)
                return (
                <div key={f.id} className="bg-slate-900/40 border border-slate-700 rounded-lg p-3 text-xs">
                  <div className="flex items-center justify-between mb-2">
                    <span className="font-medium text-slate-200">{f.symbol}</span>
                    <span className={`font-semibold ${f.side === 'BUY' ? 'text-green-400' : 'text-red-400'}`}>{actionLabel(f.side, f.position_side)}</span>
                  </div>
                  <div className="grid grid-cols-2 gap-y-1.5 gap-x-3 text-slate-300">
                    <div><span className="block text-slate-500">数量</span><span className="font-mono">{f.qty.toFixed(6)}</span></div>
                    <div><span className="block text-slate-500">开仓价格</span><span className="font-mono">{entry}</span></div>
                    <div><span className="block text-slate-500">平仓价格</span><span className="font-mono">{exit}</span></div>
                    <div><span className="block text-slate-500">手续费</span><span className="font-mono">{f.fee.toFixed(4)}</span></div>
                    <div><span className="block text-slate-500">已实现盈亏</span><span className={`font-mono font-semibold ${f.realized_pnl > 0 ? 'text-green-400' : f.realized_pnl < 0 ? 'text-red-400' : 'text-slate-400'}`}>{f.realized_pnl === 0 ? '—' : `$${f.realized_pnl.toFixed(4)}`}</span></div>
                  </div>
                  <div className="mt-1.5 text-slate-500">{new Date(f.filled_at).toLocaleString()}</div>
                </div>
                )
              })}
            </div>
          </>
        )}
        <div className="flex gap-2 mt-4">
          <button onClick={() => setOffset(Math.max(0, offset - limit))} disabled={offset === 0}
            className="px-3 py-1 text-sm bg-slate-700 rounded disabled:opacity-40 hover:bg-slate-600">← 上一页</button>
          <button onClick={() => setOffset(offset + limit)} disabled={fills.length < limit}
            className="px-3 py-1 text-sm bg-slate-700 rounded disabled:opacity-40 hover:bg-slate-600">下一页 →</button>
        </div>
      </div>
    </div>
  )
}

function EquityTab({ engineId }: { engineId: string }) {
  const [snapshots, setSnapshots] = useState<Snapshot[]>([])
  const [period, setPeriod] = useState<'1d' | '7d' | '30d' | 'all'>('7d')
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    setLoading(true)
    getEquity(engineId, 5000, period)
      .then((r) => setSnapshots(r.data.snapshots || []))
      .finally(() => setLoading(false))
  }, [engineId, period])

  const longRange = period !== '1d'
  const chartData = snapshots.map((s) => {
    const d = new Date(s.snapshotted_at)
    return {
      time: longRange
        ? `${d.getMonth() + 1}/${d.getDate()} ${d.getHours().toString().padStart(2, '0')}:${d.getMinutes().toString().padStart(2, '0')}`
        : d.toLocaleTimeString(),
      equity: +s.equity.toFixed(2),
      cash: +s.cash.toFixed(2),
    }
  })

  return (
    <div className="bg-slate-800 rounded-xl p-4">
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-sm font-semibold text-slate-300">权益曲线</h2>
        <div className="flex gap-1">
          {(['1d', '7d', '30d', 'all'] as const).map((p) => (
            <button key={p} onClick={() => setPeriod(p)}
              className={`px-2 py-0.5 text-xs rounded ${period === p ? 'bg-blue-600 text-white' : 'bg-slate-700 text-slate-400 hover:bg-slate-600 hover:text-slate-200'}`}>
              {p}
            </button>
          ))}
        </div>
      </div>
      {loading ? (
        <p className="text-slate-400 text-sm">加载中...</p>
      ) : chartData.length === 0 ? (
        <p className="text-slate-500 text-sm text-center py-8">暂无权益数据。</p>
      ) : (
        <ResponsiveContainer width="100%" height={280}>
          <LineChart data={chartData}>
            <CartesianGrid strokeDasharray="3 3" stroke="#334155" />
            <XAxis dataKey="time" tick={{ fontSize: 11, fill: '#94a3b8' }} />
            <YAxis tick={{ fontSize: 11, fill: '#94a3b8' }} />
            <Tooltip contentStyle={{ background: '#1e293b', border: '1px solid #334155', borderRadius: 8 }} labelStyle={{ color: '#94a3b8' }} />
            <Line type="monotone" dataKey="equity" stroke="#3b82f6" strokeWidth={2} dot={false} name="Equity" />
            <Line type="monotone" dataKey="cash" stroke="#10b981" strokeWidth={1.5} dot={false} name="Cash" strokeDasharray="4 4" />
          </LineChart>
        </ResponsiveContainer>
      )}
    </div>
  )
}

export default function EngineDetail() {
  const { engineId } = useParams<{ engineId: string }>()
  const navigate = useNavigate()
  const [engine, setEngine] = useState<EngineInfo | null>(null)
  const [creds, setCreds] = useState<Credential[]>([])
  const [notFound, setNotFound] = useState(false)
  const [tab, setTab] = useState<Tab>('position')

  useEffect(() => {
    setNotFound(false)
    setEngine(null)
    Promise.all([listEngines(), listCredentials()])
      .then(([er, cr]) => {
        const list: EngineInfo[] = er.data || []
        const found = list.find((e) => e.engine_id === engineId)
        if (!found) { setNotFound(true); return }
        setEngine(found)
        setCreds(cr.data || [])
      })
      .catch(() => setNotFound(true))
  }, [engineId])

  if (notFound) {
    return (
      <div className="space-y-4">
        <button onClick={() => navigate('/engine')} className="text-slate-400 hover:text-slate-200 text-sm">← 返回引擎列表</button>
        <div className="bg-slate-800 rounded-xl p-4 text-slate-500 text-sm">
          没有找到引擎 <span className="font-mono">{engineId}</span>。
        </div>
      </div>
    )
  }
  if (!engine) {
    return <p className="text-slate-400 text-sm">加载中...</p>
  }

  const credById = Object.fromEntries(creds.map((c) => [c.id, c]))
  const money = moneyBadge(engine, credById)

  return (
    <div className="space-y-4">
      <button onClick={() => navigate('/engine')} className="text-slate-400 hover:text-slate-200 text-sm">← 返回引擎列表</button>

      <div className="flex items-center gap-2 flex-wrap">
        <h1 className="text-xl font-bold">{engine.engine_id}</h1>
        <span className={`text-xs px-1.5 py-0.5 rounded ${engine.running ? 'bg-green-900/50 text-green-300' : 'bg-slate-700 text-slate-400'}`}>
          {engine.running ? '运行中' : '已停止'}
        </span>
        <span className={`text-xs px-1.5 py-0.5 rounded ${money.cls}`}>{money.text}</span>
        {engine.leverage != null && engine.leverage > 1 && (
          <span className="text-xs bg-orange-900/50 text-orange-300 px-1.5 py-0.5 rounded font-mono">{engine.leverage}x</span>
        )}
      </div>

      <div className="bg-slate-800 rounded-xl p-4 grid grid-cols-2 md:grid-cols-5 gap-3 text-xs text-slate-400">
        <div><span className="block text-slate-500">策略</span><span className="text-slate-200">{strategyLabel(engine.strategy_id)}</span></div>
        <div><span className="block text-slate-500">交易对</span><span className="text-slate-200">{engine.symbol}</span></div>
        <div><span className="block text-slate-500">周期</span><span className="text-slate-200">{engine.interval}</span></div>
        <div><span className="block text-slate-500">模式</span><span className="text-slate-200">{engine.mode}</span></div>
        <div><span className="block text-slate-500">启动于</span><span className="text-slate-200">{new Date(engine.started_at).toLocaleString()}</span></div>
      </div>

      {engine.error && (
        <div className="bg-red-900/30 border border-red-700/50 rounded-lg px-4 py-2 text-red-400 text-sm">
          Error: {engine.error}
        </div>
      )}

      <div className="flex gap-1 border-b border-slate-700 overflow-x-auto">
        {(['position', 'orders', 'fills', 'equity', 'logs'] as Tab[]).map((t) => (
          <button key={t} onClick={() => setTab(t)}
            className={`px-4 py-2 text-sm font-medium border-b-2 -mb-px transition-colors whitespace-nowrap ${
              tab === t ? 'text-blue-400 border-blue-400' : 'text-slate-400 border-transparent hover:text-slate-200'
            }`}>
            {TAB_LABEL[t]}
          </button>
        ))}
      </div>

      {tab === 'position' && <PositionTab engineId={engine.engine_id} mode={engine.mode} />}
      {tab === 'orders' && <OrdersTab engineId={engine.engine_id} />}
      {tab === 'fills' && <FillsTab engineId={engine.engine_id} />}
      {tab === 'equity' && <EquityTab engineId={engine.engine_id} />}
      {tab === 'logs' && <LogViewer engineId={engine.engine_id} />}
    </div>
  )
}
