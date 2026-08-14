import { useState } from 'react'
import type { EngineInfo } from '../pages/Engine'

const STOPPED_PREVIEW_COUNT = 5

interface StoppedEnginesListProps {
  engines: EngineInfo[] // already filtered to !running and sorted newest-first
  onNavigate: (engineId: string) => void
}

// Stopped engines accumulate forever (every past forward-test, every restart) —
// show only the most recent by default so old dead engines don't bury the
// ones that actually matter right now.
export default function StoppedEnginesList({ engines, onNavigate }: StoppedEnginesListProps) {
  const [showAll, setShowAll] = useState(false)

  if (engines.length === 0) return null
  const visible = showAll ? engines : engines.slice(0, STOPPED_PREVIEW_COUNT)

  return (
    <div className="space-y-3">
      <h2 className="text-sm font-semibold text-slate-400 flex items-center gap-2">
        Stopped ({engines.length})
        {engines.length > STOPPED_PREVIEW_COUNT && (
          <button onClick={() => setShowAll((v) => !v)}
            className="text-xs text-blue-400 hover:text-blue-300 font-normal">
            {showAll ? '只看最近几个' : `查看全部 ${engines.length} 个`}
          </button>
        )}
      </h2>
      {visible.map((eng) => (
        <div key={eng.engine_id}
          onClick={() => onNavigate(eng.engine_id)}
          className="bg-slate-800/50 hover:bg-slate-700/40 transition-colors cursor-pointer rounded-xl p-4 flex items-start gap-4">
          <div className="w-2.5 h-2.5 mt-1.5 rounded-full bg-slate-500 flex-shrink-0" />
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2 flex-wrap">
              <span className="font-semibold text-sm text-slate-400">{eng.engine_id}</span>
              <span className="text-xs bg-slate-700 text-slate-400 px-1.5 py-0.5 rounded">stopped</span>
              {eng.mode === 'paper' && (
                <span className="text-xs bg-blue-900/30 text-blue-400 px-1.5 py-0.5 rounded">paper</span>
              )}
            </div>
            {eng.error && <p className="text-xs text-red-400 mt-1">Error: {eng.error}</p>}
          </div>
        </div>
      ))}
    </div>
  )
}
