import { useEffect, useState } from 'react'
import { getPositions, closeEnginePosition, listCredentials } from '../api/trading'
import { useTradeSocket } from '../hooks/useTradeSocket'

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
  credential_id: number
  last_price: number
  cash: number
  equity: number
  positions: PositionView[]
}

// 该引擎对应账户的真钱属性(判断是否真金白银)
interface CredMeta {
  testnet: boolean
  demo: boolean
}

// 方向的中文
const sideCn = (side: string): string =>
  side === 'LONG' ? '做多' : side === 'SHORT' ? '做空' : '净'

// 价格格式化:>=1 用千分位 + 2 位小数;不足 1 的小币保留更多小数位
const fmtPrice = (v: number): string => {
  if (v == null || !isFinite(v)) return '—'
  if (Math.abs(v) >= 1) {
    return v.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  }
  return v.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 8 })
}

// 金额(可用资金/权益):千分位 + 2 位小数
const fmtUsd2 = (v: number): string => {
  if (v == null || !isFinite(v)) return '—'
  return v.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}

// 数量:最多 6 位小数,去掉多余的末尾 0(避免 0.063000 这种)
const fmtQty = (v: number): string => {
  if (v == null || !isFinite(v)) return '—'
  const s = v.toFixed(6)
  return s.includes('.') ? s.replace(/\.?0+$/, '') : s
}

// 带符号的盈亏金额:+$12.3400 / -$12.3400
const fmtSignedUsd = (v: number): string => {
  const sign = v >= 0 ? '+' : '-'
  return `${sign}$${Math.abs(v).toFixed(4)}`
}

export default function Positions() {
  const [engines, setEngines] = useState<EnginePositions[]>([])
  const [loading, setLoading] = useState(true)
  const [apiError, setApiError] = useState<string | null>(null)
  const [closingKey, setClosingKey] = useState<string | null>(null)
  // credentialId -> { testnet, demo } —— 用来判断持仓是真钱还是模拟资金
  const [credMeta, setCredMeta] = useState<Record<number, CredMeta>>({})

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

  const handleClose = async (engineID: string, symbol: string, side: 'LONG' | 'SHORT', qty: number) => {
    const ok = window.confirm(
      `确定平掉 ${symbol} 的${sideCn(side)}仓位(${fmtQty(qty)})吗?\n\n` +
      `这会在交易所下一个"只减仓"的市价单立即平掉该方向,策略引擎会继续运行。`
    )
    if (!ok) return
    const key = `${engineID}-${side}`
    setClosingKey(key)
    try {
      const r = await closeEnginePosition(engineID, side)
      alert(`已平仓:${r.data.qty} ${r.data.symbol} @ ${r.data.fill_price}`)
      refresh()
    } catch (e: any) {
      alert(`平仓失败:${e.response?.data?.error || e.message}`)
    } finally {
      setClosingKey(null)
    }
  }

  const refresh = () => {
    setLoading(true)
    getPositions()
      .then((r) => { setEngines(r.data.positions || []); setApiError(null) })
      .catch((e) => setApiError(e.response?.data?.error || '加载持仓失败'))
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    refresh()
    const id = setInterval(refresh, 5000)
    return () => clearInterval(id)
  }, [])

  // Refresh positions on any fill event (position sizes change after fills)
  useTradeSocket((msg: any) => {
    if (msg?.type === 'fill') {
      refresh()
    }
  })

  const totalUnrealized = engines.reduce(
    (sum, e) => sum + e.positions.reduce((s, p) => s + p.unrealized_pnl, 0), 0,
  )

  // 资金属性:这个引擎动的是真钱还是模拟钱。
  // 注意 mode==='live' 只代表"实时撮合",配的可能仍是 testnet/demo 账户(假钱),
  // 所以要回查凭证的 testnet/demo 才能判断是不是真金白银。
  const moneyBadge = (eng: EnginePositions) => {
    const title = `mode=${eng.mode} · credential_id=${eng.credential_id}`
    // 回测/模拟:根本没真下过单
    if (eng.mode === 'paper') {
      return { text: '回测/模拟', cls: 'bg-slate-600 text-slate-300', title }
    }
    const cred = credMeta[eng.credential_id]
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
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold">当前持仓</h1>
        <div className="flex items-center gap-4">
          {engines.length > 0 && (
            <span className={`text-sm font-semibold ${totalUnrealized >= 0 ? 'text-green-400' : 'text-red-400'}`}>
              总浮动盈亏: {fmtSignedUsd(totalUnrealized)}
            </span>
          )}
          <button onClick={refresh}
            className="px-3 py-1 text-xs bg-slate-700 hover:bg-slate-600 text-slate-300 rounded transition-colors">
            刷新
          </button>
        </div>
      </div>

      {apiError && (
        <div className="bg-red-900/30 border border-red-700/50 rounded-lg px-4 py-2 text-red-400 text-sm">
          {apiError}
        </div>
      )}

      {loading && engines.length === 0 ? (
        <p className="text-slate-400 text-sm">加载中...</p>
      ) : engines.length === 0 ? (
        <div className="bg-slate-800 rounded-xl p-6 text-center text-slate-500 text-sm">
          暂无持仓。启动一个策略引擎后,这里会显示实时持仓。
        </div>
      ) : (
        engines.map((eng) => {
          const badge = moneyBadge(eng)
          return (
          <div key={eng.engine_id} className="bg-slate-800 rounded-xl p-5 space-y-3">
            {/* Engine header */}
            <div className="flex items-center justify-between">
              <div>
                <span className="font-semibold text-slate-100">{eng.engine_id}</span>
                <span title={badge.title} className={`ml-2 text-xs px-1.5 py-0.5 rounded ${badge.cls}`}>
                  {badge.text}
                </span>
              </div>
              <div className="text-right text-xs text-slate-400 space-y-0.5">
                <div>最新价: <span className="text-slate-200 font-mono">${fmtPrice(eng.last_price)}</span></div>
                <div>可用资金: <span className="text-slate-200 font-mono">${fmtUsd2(eng.cash)}</span></div>
                <div>权益: <span className="text-slate-200 font-mono">${fmtUsd2(eng.equity)}</span></div>
              </div>
            </div>

            {eng.positions.length === 0 ? (
              <p className="text-xs text-slate-500">该引擎暂无持仓。</p>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="text-left text-slate-400 text-xs border-b border-slate-700">
                      <th className="pb-2">交易对</th>
                      <th className="pb-2">方向</th>
                      <th className="pb-2 text-right">数量</th>
                      <th className="pb-2 text-right">开仓均价</th>
                      <th className="pb-2 text-right">最新价</th>
                      <th className="pb-2 text-right">浮动盈亏</th>
                      <th className="pb-2 text-right">已实现盈亏</th>
                      <th className="pb-2 text-right">操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    {eng.positions.map((p, i) => {
                      const side = p.position_side === 'LONG' || p.position_side === 'SHORT' ? p.position_side : null
                      const key = `${eng.engine_id}-${side}`
                      const isClosing = closingKey === key
                      return (
                      <tr key={i} className="border-b border-slate-700/50 hover:bg-slate-700/30">
                        <td className="py-2 font-medium">{p.symbol}</td>
                        <td className="py-2">
                          {p.position_side ? (
                            <span className={`text-xs font-semibold px-1.5 py-0.5 rounded ${p.position_side === 'LONG' ? 'bg-green-900/50 text-green-300' : 'bg-red-900/50 text-red-300'}`}>
                              {sideCn(p.position_side)}
                            </span>
                          ) : (
                            <span className="text-xs text-slate-400">净</span>
                          )}
                        </td>
                        <td className="py-2 text-right font-mono">{fmtQty(p.qty)}</td>
                        <td className="py-2 text-right font-mono">${fmtPrice(p.avg_entry_price)}</td>
                        <td className="py-2 text-right font-mono">${fmtPrice(eng.last_price)}</td>
                        <td className={`py-2 text-right font-mono font-semibold ${p.unrealized_pnl >= 0 ? 'text-green-400' : 'text-red-400'}`}>
                          {fmtSignedUsd(p.unrealized_pnl)}
                        </td>
                        <td className={`py-2 text-right font-mono ${p.realized_pnl > 0 ? 'text-green-400' : p.realized_pnl < 0 ? 'text-red-400' : 'text-slate-400'}`}>
                          {p.realized_pnl == null || p.realized_pnl === 0 ? '—' : fmtSignedUsd(p.realized_pnl)}
                        </td>
                        <td className="py-2 text-right">
                          {side ? (
                            <button
                              disabled={isClosing || eng.mode !== 'live'}
                              onClick={() => handleClose(eng.engine_id, p.symbol, side, p.qty)}
                              className="px-2 py-0.5 text-xs bg-red-900/40 hover:bg-red-900/60 disabled:opacity-40 disabled:cursor-not-allowed text-red-200 border border-red-800/40 rounded transition-colors"
                              title={eng.mode !== 'live' ? '仅实时引擎支持一键平仓' : `以"只减仓"市价单平掉${sideCn(side)}方向`}
                            >
                              {isClosing ? '平仓中…' : side === 'SHORT' ? '平空仓' : '平多仓'}
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
          )
        })
      )}
      <p className="text-xs text-slate-600 text-right">每 5 秒自动刷新</p>
    </div>
  )
}
