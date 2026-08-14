import { memo } from 'react'
import { strategyLabel } from '../constants/strategies'
import type { EngineInfo, Credential, EnginePositions } from '../pages/Engine'
import LiveStatus from './LiveStatus'
import PositionRows from './PositionRows'

// 资金属性:引擎动的是真钱还是模拟钱(mode=live 只是"实时",配 demo/testnet 账户仍是假钱)。
function engineMoney(eng: EngineInfo, credById: Record<number, Credential>) {
  if (eng.mode === 'paper') return { text: '回测/模拟', cls: 'bg-slate-600 text-slate-300' }
  const c = credById[eng.credential_id]
  if (!c) return { text: '实时', cls: 'bg-slate-600 text-slate-300' }
  if (c.testnet || c.demo) return { text: '模拟盘', cls: 'bg-blue-900/50 text-blue-300' }
  return { text: '真钱', cls: 'bg-red-900/50 text-red-300' }
}

interface RunningEngineCardProps {
  eng: EngineInfo
  credById: Record<number, Credential>
  enginePositions: EnginePositions | undefined
  stoppingId: string | null
  onStop: (engineId: string) => void
  onNavigate: (engineId: string) => void
  onPositionClosed: () => void
}

function RunningEngineCardImpl({
  eng, credById, enginePositions, stoppingId, onStop, onNavigate, onPositionClosed,
}: RunningEngineCardProps) {
  const money = engineMoney(eng, credById)
  return (
    <div
      onClick={() => onNavigate(eng.engine_id)}
      className="bg-slate-800 hover:bg-slate-700/70 transition-colors cursor-pointer rounded-xl p-4 flex flex-col sm:flex-row sm:items-start gap-3 sm:gap-4">
      <div className="w-2.5 h-2.5 mt-1.5 rounded-full bg-green-400 animate-pulse flex-shrink-0 hidden sm:block" />
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2 flex-wrap">
          <span className="font-semibold text-sm">{eng.engine_id}</span>
          <span className="text-xs bg-green-900/50 text-green-300 px-1.5 py-0.5 rounded">运行中</span>
          <span title={`mode=${eng.mode} · credential_id=${eng.credential_id}`} className={`text-xs ${money.cls} px-1.5 py-0.5 rounded`}>{money.text}</span>
          {eng.leverage && eng.leverage > 1 && (
            <span className="text-xs bg-orange-900/50 text-orange-300 px-1.5 py-0.5 rounded font-mono">
              {eng.leverage}x
            </span>
          )}
        </div>
        <div className="grid grid-cols-2 md:grid-cols-4 gap-2 mt-2 text-xs text-slate-400">
          <div><span className="block text-slate-500">策略</span>{strategyLabel(eng.strategy_id)}</div>
          <div><span className="block text-slate-500">交易对</span>{eng.symbol}</div>
          <div><span className="block text-slate-500">周期</span>{eng.interval}</div>
          <div><span className="block text-slate-500">启动于</span>{new Date(eng.started_at).toLocaleString()}</div>
        </div>
        {eng.mode === 'live' && <LiveStatus engineID={eng.engine_id} strategyId={eng.strategy_id} />}
        {/* guardian's own panel above is a risk-management view (R-multiples,
            stop distance, trail status) — it has no $ P&L and no close button.
            This table is the plain "what's my money doing, can I get out" view,
            so it applies to every strategy including guardian, not just non-guardian. */}
        <PositionRows
          engineId={eng.engine_id}
          mode={eng.mode}
          lastPrice={enginePositions?.last_price ?? 0}
          positions={enginePositions?.positions ?? []}
          onClosed={onPositionClosed}
        />
      </div>
      <button
        onClick={(e) => { e.stopPropagation(); onStop(eng.engine_id) }}
        disabled={stoppingId === eng.engine_id}
        className="px-3 py-1.5 bg-red-600 hover:bg-red-700 disabled:opacity-50 rounded text-xs font-semibold flex-shrink-0 w-full sm:w-auto"
      >
        {stoppingId === eng.engine_id ? '停止中…' : '⏹ 停止'}
      </button>
    </div>
  )
}

// Custom comparator: eng/enginePositions are fresh object references every
// poll tick (loadEngines every 10s, loadPositions every 5s both build brand-new
// objects from the API response) even when nothing relevant changed for THIS
// specific engine — default React.memo reference-equality would re-render
// every card on every poll regardless. Compare by VALUE instead, so a poll
// tick only re-renders the card(s) whose own data actually changed
// (2026-08-12 ask: "局部刷新" — only the ticking price/pnl/return should update,
// not every other running engine's card).
function propsEqual(prev: RunningEngineCardProps, next: RunningEngineCardProps): boolean {
  return (
    prev.stoppingId === next.stoppingId &&
    prev.credById === next.credById &&
    prev.onStop === next.onStop &&
    prev.onNavigate === next.onNavigate &&
    prev.onPositionClosed === next.onPositionClosed &&
    JSON.stringify(prev.eng) === JSON.stringify(next.eng) &&
    JSON.stringify(prev.enginePositions) === JSON.stringify(next.enginePositions)
  )
}

export default memo(RunningEngineCardImpl, propsEqual)
