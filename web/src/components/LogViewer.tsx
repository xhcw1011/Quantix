import { useEngineLogs } from '../hooks/useEngineLogs'
import NumberInput from './NumberInput'

interface LogViewerProps {
  engineId: string | undefined
  // Logs.tsx (standalone page) has more vertical room than EngineDetail's
  // tabbed panel, so it shows more of the log pane.
  maxHeightClass?: string
}

function levelColor(line: string) {
  if (line.includes('"level":"error"') || line.includes('ERROR')) return 'text-red-400'
  if (line.includes('"level":"warn"') || line.includes('WARN')) return 'text-amber-300'
  if (line.includes('"level":"debug"')) return 'text-slate-500'
  return 'text-slate-300'
}

export default function LogViewer({ engineId, maxHeightClass = 'max-h-[60vh]' }: LogViewerProps) {
  const { lines, grep, setGrep, tailN, setTailN, autoRefresh, setAutoRefresh, loading, err, fetchLogs } =
    useEngineLogs(engineId)

  return (
    <div className="space-y-3">
      <div className="bg-slate-800 rounded-xl p-4 flex flex-wrap items-end gap-3">
        <div className="flex flex-col gap-1">
          <label className="text-xs text-slate-400">最近行数(最多2000)</label>
          <NumberInput min={50} max={2000} value={tailN}
            onChange={(v) => setTailN(v === '' ? 50 : v)}
            className="w-32 bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm" />
        </div>
        <div className="flex flex-col gap-1 flex-1 min-w-[200px]">
          <label className="text-xs text-slate-400">过滤(包含关键字)</label>
          <div className="flex gap-2">
            <input type="text" value={grep} placeholder="例如 golden cross, STOP"
              onChange={(e) => setGrep(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') fetchLogs() }}
              className="flex-1 bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm" />
            <button onClick={fetchLogs} className="px-2 py-1 text-xs bg-slate-700 hover:bg-slate-600 text-slate-200 rounded">查询</button>
          </div>
        </div>
        <label className="text-xs text-slate-400 flex items-center gap-1">
          <input type="checkbox" checked={autoRefresh} onChange={(e) => setAutoRefresh(e.target.checked)} />
          每5秒自动刷新
        </label>
      </div>

      {err && <div className="bg-red-900/30 border border-red-700/50 rounded-lg px-3 py-2 text-red-400 text-sm">{err}</div>}

      <div className={`bg-slate-900 rounded-xl p-3 ${maxHeightClass} overflow-y-auto border border-slate-800`}>
        {loading && lines.length === 0 ? (
          <p className="text-slate-500 text-sm">加载中…</p>
        ) : lines.length === 0 ? (
          <p className="text-slate-500 text-sm">暂无匹配的日志行。</p>
        ) : (
          lines.map((l, i) => (
            <div key={i} className={`font-mono text-[11px] leading-tight whitespace-pre-wrap break-all ${levelColor(l)}`}>{l}</div>
          ))
        )}
      </div>
      <p className="text-xs text-slate-600 text-right">共 {lines.length} 行(今日日志)。</p>
    </div>
  )
}
