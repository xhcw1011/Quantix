import { useEffect, useState } from 'react'
import { getOrders, listCredentials } from '../api/trading'

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
  credential_id: number
  created_at: string
}

// 该订单对应账户的真钱属性(判断是否真金白银)
interface CredMeta {
  testnet: boolean
  demo: boolean
}

const statusColor: Record<string, string> = {
  FILLED: 'bg-green-900/50 text-green-300',
  CANCELLED: 'bg-slate-600 text-slate-300',
  REJECTED: 'bg-red-900/50 text-red-300',
  PENDING: 'bg-yellow-900/50 text-yellow-300',
  OPEN: 'bg-blue-900/50 text-blue-300',
}

const inputCls = 'bg-slate-700 border border-slate-600 rounded px-2 py-1 text-sm text-slate-100 focus:outline-none focus:ring-1 focus:ring-blue-500'

// 守护仓 = 守护你手动开的仓位;否则是机器人自动交易
const isGuardian = (strategyId: string) => (strategyId || '').includes('guardian')

// 方向的中文
const sideLabel = (side: string) => (side === 'BUY' ? '买' : side === 'SELL' ? '卖' : side)

// 订单类型的中文(未知类型原样显示)
const typeLabel = (type: string): string => {
  const map: Record<string, string> = {
    MARKET: '市价单',
    LIMIT: '限价单',
    STOP_MARKET: '止损市价单',
    STOP_LIMIT: '止损限价单',
  }
  return map[type] || type
}

// 订单状态的中文(未知状态原样显示)
const statusLabel = (status: string): string => {
  const map: Record<string, string> = {
    OPEN: '挂单中',
    NEW: '挂单中',
    FILLED: '已成交',
    PARTIALLY_FILLED: '部分成交',
    CANCELLED: '已撤单',
    CANCELED: '已撤单',
    REJECTED: '被拒绝',
    EXPIRED: '已过期',
  }
  return map[status] || status
}

export default function Orders() {
  const [orders, setOrders] = useState<Order[]>([])
  const [loading, setLoading] = useState(true)
  const [offset, setOffset] = useState(0)
  const [apiError, setApiError] = useState<string | null>(null)
  // credentialId -> { testnet, demo } —— 用来判断订单是真钱还是模拟资金
  const [credMeta, setCredMeta] = useState<Record<number, CredMeta>>({})
  const limit = 50

  // 只在挂载时拉一次凭证,建立 id -> 账户属性 的映射
  useEffect(() => {
    listCredentials()
      .then((r) => {
        const map: Record<number, CredMeta> = {}
        for (const c of (r.data || [])) {
          map[c.id] = { testnet: !!c.testnet, demo: !!c.demo }
        }
        setCredMeta(map)
      })
      .catch(() => {})
  }, [])

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
      .catch((e) => setApiError(e.response?.data?.error || '加载订单失败'))
      .finally(() => setLoading(false))
  }, [offset, symbol, strategy, mode, from, to])

  const clearFilters = () => {
    setSymbol(''); setStrategy(''); setMode(''); setFrom(''); setTo('')
  }
  const hasFilters = symbol || strategy || mode || from || to

  // 资金属性:这笔订单动的是真钱还是模拟钱。
  // 注意 mode==='live' 只代表"实时撮合",配的可能仍是 testnet/demo 账户(假钱),
  // 所以要回查凭证的 testnet/demo 才能判断是不是真金白银。
  const moneyBadge = (o: Order) => {
    const title = `mode=${o.mode} · credential_id=${o.credential_id}`
    // 回测/模拟:根本没真下过单
    if (o.mode === 'paper') {
      return { text: '回测/模拟', cls: 'bg-slate-600 text-slate-300', title }
    }
    const cred = credMeta[o.credential_id]
    if (!cred) {
      // 凭证查不到(引擎已停/凭证已删):无法判断,保守显示"实时"
      return { text: '实时', cls: 'bg-slate-600 text-slate-300', title: `${title} · 账户未知` }
    }
    if (cred.testnet || cred.demo) {
      // 实时撮合但用的是测试/模拟账户 = 假钱
      return { text: '模拟盘', cls: 'bg-blue-900/50 text-blue-300', title }
    }
    // 正式账户 = 真金白银
    return { text: '真钱', cls: 'bg-red-900/50 text-red-300', title }
  }

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-bold">订单记录</h1>

      {apiError && (
        <div className="bg-red-900/30 border border-red-700/50 rounded-lg px-4 py-2 text-red-400 text-sm">
          {apiError}
        </div>
      )}

      {/* Filter bar */}
      <div className="bg-slate-800 rounded-xl p-4 flex flex-wrap items-end gap-3">
        <div className="flex flex-col gap-1">
          <label className="text-xs text-slate-400">交易对</label>
          <input className={inputCls} placeholder="例如 BTCUSDT" value={symbol}
            onChange={e => setSymbol(e.target.value.toUpperCase())} />
        </div>
        <div className="flex flex-col gap-1">
          <label className="text-xs text-slate-400">来源/策略</label>
          <input className={inputCls} placeholder="例如 macross 或 guardian" value={strategy}
            onChange={e => setStrategy(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1">
          <label className="text-xs text-slate-400">模式</label>
          <select className={inputCls} value={mode} onChange={e => setMode(e.target.value)}>
            <option value="">全部</option>
            <option value="live">实盘</option>
            <option value="paper">模拟</option>
          </select>
        </div>
        <div className="flex flex-col gap-1">
          <label className="text-xs text-slate-400">起始</label>
          <input type="date" className={inputCls} value={from} onChange={e => setFrom(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1">
          <label className="text-xs text-slate-400">结束</label>
          <input type="date" className={inputCls} value={to} onChange={e => setTo(e.target.value)} />
        </div>
        {hasFilters && (
          <button onClick={clearFilters}
            className="px-3 py-1 text-xs bg-slate-600 hover:bg-slate-500 text-slate-300 rounded transition-colors">
            清除
          </button>
        )}
      </div>

      <div className="bg-slate-800 rounded-xl p-4">
        {loading ? (
          <p className="text-slate-400 text-sm">加载中...</p>
        ) : orders.length === 0 ? (
          <p className="text-slate-500 text-sm">
            {hasFilters ? '没有符合筛选条件的订单。' : '暂无订单。启动机器人后产生的订单会显示在这里。'}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-slate-400 text-xs border-b border-slate-700">
                  <th className="pb-2">交易对</th>
                  <th className="pb-2">方向</th>
                  <th className="pb-2">类型</th>
                  <th className="pb-2">状态</th>
                  <th className="pb-2 text-right">数量</th>
                  <th className="pb-2 text-right">已成交</th>
                  <th className="pb-2 text-right">均价</th>
                  <th className="pb-2 text-right">手续费</th>
                  <th className="pb-2">来源</th>
                  <th className="pb-2">资金</th>
                  <th className="pb-2 text-right">时间</th>
                </tr>
              </thead>
              <tbody>
                {orders.map((o) => (
                  <tr key={o.id} className="border-b border-slate-700/50 hover:bg-slate-700/30">
                    <td className="py-2 font-medium">{o.symbol}</td>
                    <td className={`py-2 font-semibold ${o.side === 'BUY' ? 'text-green-400' : 'text-red-400'}`}>
                      {sideLabel(o.side)}
                    </td>
                    <td className="py-2 text-slate-400">{typeLabel(o.type)}</td>
                    <td className="py-2">
                      <span className={`text-xs px-1.5 py-0.5 rounded ${statusColor[o.status] || 'bg-slate-600 text-slate-300'}`}>
                        {statusLabel(o.status)}
                      </span>
                    </td>
                    <td className="py-2 text-right font-mono">{o.quantity.toFixed(6)}</td>
                    <td className="py-2 text-right font-mono">{o.filled_quantity.toFixed(6)}</td>
                    <td className="py-2 text-right font-mono">{o.avg_fill_price > 0 ? o.avg_fill_price.toFixed(2) : '—'}</td>
                    <td className="py-2 text-right font-mono text-slate-400">{o.commission.toFixed(4)}</td>
                    <td className="py-2">
                      <span
                        title={o.strategy_id}
                        className={`text-xs px-1.5 py-0.5 rounded ${isGuardian(o.strategy_id) ? 'bg-emerald-900/50 text-emerald-300' : 'bg-blue-900/50 text-blue-300'}`}
                      >
                        {isGuardian(o.strategy_id) ? '守护仓' : '机器人自动'}
                      </span>
                    </td>
                    <td className="py-2">
                      {(() => {
                        const b = moneyBadge(o)
                        return (
                          <span title={b.title} className={`text-xs px-1.5 py-0.5 rounded ${b.cls}`}>
                            {b.text}
                          </span>
                        )
                      })()}
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
            ← 上一页
          </button>
          <button
            onClick={() => setOffset(offset + limit)}
            disabled={orders.length < limit}
            className="px-3 py-1 text-sm bg-slate-700 rounded disabled:opacity-40 hover:bg-slate-600"
          >
            下一页 →
          </button>
        </div>
      </div>
    </div>
  )
}
