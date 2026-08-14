import { useMemo } from 'react'
import type { EngineInfo, Credential, EnginePositions } from '../pages/Engine'
import RunningEngineCard from './RunningEngineCard'

interface RunningEnginesListProps {
  engines: EngineInfo[] // already filtered to running
  credentials: Credential[]
  positionsByEngine: Record<string, EnginePositions>
  stoppingId: string | null
  onStop: (engineId: string) => void
  onNavigate: (engineId: string) => void
  onPositionClosed: () => void
}

export default function RunningEnginesList({
  engines, credentials, positionsByEngine, stoppingId, onStop, onNavigate, onPositionClosed,
}: RunningEnginesListProps) {
  // Memoized so its reference stays stable across polls (credentials itself
  // rarely changes) — required for RunningEngineCard's memoization to work,
  // since a fresh object here would make every card's props "different" every
  // render regardless of whether that card's own data actually changed.
  const credById = useMemo(
    () => Object.fromEntries(credentials.map((c) => [c.id, c])),
    [credentials]
  )

  return (
    <div className="space-y-3">
      <h2 className="text-sm font-semibold text-slate-400">
        运行中 ({engines.length})
      </h2>
      {engines.length === 0 ? (
        <div className="bg-slate-800 rounded-xl p-4 text-slate-500 text-sm">
          暂无运行中的策略。点 <strong>+ 新建策略</strong> 启动一个。
        </div>
      ) : (
        engines.map((eng) => (
          <RunningEngineCard
            key={eng.engine_id}
            eng={eng}
            credById={credById}
            enginePositions={positionsByEngine[eng.engine_id]}
            stoppingId={stoppingId}
            onStop={onStop}
            onNavigate={onNavigate}
            onPositionClosed={onPositionClosed}
          />
        ))
      )}
    </div>
  )
}
