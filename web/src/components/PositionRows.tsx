import { useState } from 'react'
import { closeEnginePosition } from '../api/trading'
import { fmtPrice, fmtQty, fmtSignedUsd, sideCn } from '../lib/positionFormat'
import { useConfirm } from '../hooks/useConfirm'
import { toast } from '../store/toastStore'

export interface PositionView {
  symbol: string
  position_side: string // "" (net/spot), "LONG", "SHORT"
  qty: number
  avg_entry_price: number
  unrealized_pnl: number
  realized_pnl: number
}

interface PositionRowsProps {
  engineId: string
  mode: string
  lastPrice: number
  positions: PositionView[]
  onClosed: () => void
}

export default function PositionRows({ engineId, mode, lastPrice, positions, onClosed }: PositionRowsProps) {
  const [closingKey, setClosingKey] = useState<string | null>(null)
  const confirm = useConfirm()

  if (positions.length === 0) return null

  const handleClose = async (symbol: string, side: 'LONG' | 'SHORT', qty: number) => {
    const ok = await confirm({
      title: `平掉 ${symbol} 的${sideCn(side)}仓位？`,
      message: `数量 ${fmtQty(qty)}。这会在交易所下一个"只减仓"的市价单立即平掉该方向，策略引擎会继续运行。`,
      confirmLabel: '平仓',
    })
    if (!ok) return
    const key = `${engineId}-${side}`
    setClosingKey(key)
    try {
      const r = await closeEnginePosition(engineId, side)
      toast.success(`已平仓:${r.data.qty} ${r.data.symbol} @ ${r.data.fill_price}`)
      onClosed()
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
    } catch (e: any) {
      toast.error(`平仓失败:${e.response?.data?.error || e.message}`)
    } finally {
      setClosingKey(null)
    }
  }

  const rowMeta = (p: PositionView): { side: 'LONG' | 'SHORT' | null; isClosing: boolean } => {
    const side = p.position_side === 'LONG' || p.position_side === 'SHORT' ? p.position_side : null
    return { side, isClosing: closingKey === `${engineId}-${side}` }
  }
  const closeButtonLabel = (side: 'LONG' | 'SHORT', isClosing: boolean) =>
    isClosing ? '平仓中…' : side === 'SHORT' ? '平空仓' : '平多仓'

  return (
    <div className="mt-2">
      {/* Table — sm and up. */}
      <div className="hidden sm:block overflow-x-auto">
        <table className="w-full text-xs">
          <thead>
            <tr className="text-left text-slate-500 border-b border-slate-700">
              <th className="pb-1">交易对</th>
              <th className="pb-1">方向</th>
              <th className="pb-1 text-right">数量</th>
              <th className="pb-1 text-right">开仓均价</th>
              <th className="pb-1 text-right">最新价</th>
              <th className="pb-1 text-right">浮动盈亏</th>
              <th className="pb-1 text-right">已实现盈亏</th>
              <th className="pb-1 text-right">操作</th>
            </tr>
          </thead>
          <tbody>
            {positions.map((p, i) => {
              const { side, isClosing } = rowMeta(p)
              return (
                <tr key={i} className="border-b border-slate-700/50">
                  <td className="py-1.5 font-medium text-slate-200">{p.symbol}</td>
                  <td className="py-1.5">
                    {p.position_side ? (
                      <span className={`font-semibold px-1.5 py-0.5 rounded ${p.position_side === 'LONG' ? 'bg-green-900/50 text-green-300' : 'bg-red-900/50 text-red-300'}`}>
                        {sideCn(p.position_side)}
                      </span>
                    ) : (
                      <span className="text-slate-400">净</span>
                    )}
                  </td>
                  <td className="py-1.5 text-right font-mono text-slate-300">{fmtQty(p.qty)}</td>
                  <td className="py-1.5 text-right font-mono text-slate-300">${fmtPrice(p.avg_entry_price)}</td>
                  <td className="py-1.5 text-right font-mono text-slate-300">${fmtPrice(lastPrice)}</td>
                  <td className={`py-1.5 text-right font-mono font-semibold ${p.unrealized_pnl >= 0 ? 'text-green-400' : 'text-red-400'}`}>
                    {fmtSignedUsd(p.unrealized_pnl)}
                  </td>
                  <td className={`py-1.5 text-right font-mono ${p.realized_pnl > 0 ? 'text-green-400' : p.realized_pnl < 0 ? 'text-red-400' : 'text-slate-400'}`}>
                    {p.realized_pnl == null || p.realized_pnl === 0 ? '—' : fmtSignedUsd(p.realized_pnl)}
                  </td>
                  <td className="py-1.5 text-right">
                    {side ? (
                      <button
                        disabled={isClosing || mode !== 'live'}
                        onClick={(e) => { e.stopPropagation(); handleClose(p.symbol, side, p.qty) }}
                        className="px-2 py-0.5 bg-red-600 hover:bg-red-700 disabled:opacity-40 disabled:cursor-not-allowed text-white font-semibold rounded transition-colors"
                        title={mode !== 'live' ? '仅实时引擎支持一键平仓' : `以"只减仓"市价单平掉${sideCn(side)}方向`}
                      >
                        {closeButtonLabel(side, isClosing)}
                      </button>
                    ) : (
                      <span className="text-slate-600">—</span>
                    )}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>

      {/* Cards — below sm. The 8-column table doesn't reflow usefully on a
          phone-width screen, so this is a genuinely different layout rather
          than the same content squeezed narrower. */}
      <div className="sm:hidden space-y-2">
        {positions.map((p, i) => {
          const { side, isClosing } = rowMeta(p)
          return (
            <div key={i} className="bg-slate-900/40 border border-slate-700 rounded-lg p-3 text-xs" onClick={(e) => e.stopPropagation()}>
              <div className="flex items-center justify-between mb-2">
                <span className="font-medium text-slate-200">{p.symbol}</span>
                {p.position_side ? (
                  <span className={`font-semibold px-1.5 py-0.5 rounded ${p.position_side === 'LONG' ? 'bg-green-900/50 text-green-300' : 'bg-red-900/50 text-red-300'}`}>
                    {sideCn(p.position_side)}
                  </span>
                ) : (
                  <span className="text-slate-400">净</span>
                )}
              </div>
              <div className="grid grid-cols-2 gap-y-1.5 gap-x-3 text-slate-300">
                <div><span className="block text-slate-500">数量</span><span className="font-mono">{fmtQty(p.qty)}</span></div>
                <div><span className="block text-slate-500">开仓均价</span><span className="font-mono">${fmtPrice(p.avg_entry_price)}</span></div>
                <div><span className="block text-slate-500">最新价</span><span className="font-mono">${fmtPrice(lastPrice)}</span></div>
                <div><span className="block text-slate-500">浮动盈亏</span><span className={`font-mono font-semibold ${p.unrealized_pnl >= 0 ? 'text-green-400' : 'text-red-400'}`}>{fmtSignedUsd(p.unrealized_pnl)}</span></div>
                <div><span className="block text-slate-500">已实现盈亏</span><span className={`font-mono ${p.realized_pnl > 0 ? 'text-green-400' : p.realized_pnl < 0 ? 'text-red-400' : 'text-slate-400'}`}>{p.realized_pnl == null || p.realized_pnl === 0 ? '—' : fmtSignedUsd(p.realized_pnl)}</span></div>
              </div>
              {side && (
                <button
                  disabled={isClosing || mode !== 'live'}
                  onClick={() => handleClose(p.symbol, side, p.qty)}
                  className="mt-2.5 w-full py-1.5 bg-red-600 hover:bg-red-700 disabled:opacity-40 disabled:cursor-not-allowed text-white font-semibold rounded transition-colors"
                >
                  {closeButtonLabel(side, isClosing)}
                </button>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
