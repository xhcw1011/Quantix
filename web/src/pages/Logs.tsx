import { useEffect, useMemo, useRef, useState } from 'react'
import { listEngines, getRecentLogs } from '../api/trading'

interface EngineInfo {
  engine_id: string
  strategy_id: string
  symbol: string
  mode: string
  running: boolean
}

export default function Logs() {
  const [engines, setEngines] = useState<EngineInfo[]>([])
  const [selectedID, setSelectedID] = useState<string>('')
  const [lines, setLines] = useState<string[]>([])
  const [grep, setGrep] = useState('')
  const [tailN, setTailN] = useState(300)
  const [autoRefresh, setAutoRefresh] = useState(false)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const scrollRef = useRef<HTMLDivElement | null>(null)
  // Sticky tail: true when user is at the bottom; we then auto-scroll on
  // new content. Toggles to false if they scroll up to read history.
  const stickyRef = useRef<boolean>(true)

  useEffect(() => {
    listEngines().then(r => {
      const list = (r.data || []) as EngineInfo[]
      setEngines(list)
      if (list.length > 0 && !selectedID) setSelectedID(list[0].engine_id)
    }).catch(e => setErr(e.response?.data?.error || 'Failed to load engines'))
    // intentionally only run once
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const fetchLogs = () => {
    if (!selectedID) return
    setLoading(true)
    getRecentLogs(selectedID, tailN, grep)
      .then(r => { setLines(r.data.lines || []); setErr(null) })
      .catch(e => setErr(e.response?.data?.error || 'Failed to fetch logs'))
      .finally(() => setLoading(false))
  }

  useEffect(() => { fetchLogs() /* eslint-disable-next-line */ }, [selectedID, tailN])

  useEffect(() => {
    if (!autoRefresh) return
    const t = setInterval(fetchLogs, 5000)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoRefresh, selectedID, tailN, grep])

  // After lines change, if user was at the bottom, scroll to bottom.
  useEffect(() => {
    if (stickyRef.current && scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight
    }
  }, [lines])

  const onScroll = () => {
    const el = scrollRef.current
    if (!el) return
    // Within 8px of bottom counts as "at bottom" — small slack for sub-pixel.
    stickyRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 8
  }

  // Color-code zap log levels for quick scanning.
  const rendered = useMemo(() => lines.map((l, i) => {
    let cls = 'text-slate-300'
    if (l.includes('"level":"error"') || l.includes('ERROR')) cls = 'text-red-400'
    else if (l.includes('"level":"warn"') || l.includes('WARN')) cls = 'text-amber-300'
    else if (l.includes('"level":"debug"')) cls = 'text-slate-500'
    return <div key={i} className={`font-mono text-[11px] leading-tight whitespace-pre-wrap break-all ${cls}`}>{l}</div>
  }), [lines])

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold">Live Logs</h1>
        <div className="flex items-center gap-2">
          <label className="text-xs text-slate-400 flex items-center gap-1">
            <input type="checkbox" checked={autoRefresh} onChange={e => setAutoRefresh(e.target.checked)} />
            Auto-refresh 5s
          </label>
          <button onClick={fetchLogs} disabled={loading}
            className="px-3 py-1 text-xs bg-slate-700 hover:bg-slate-600 disabled:opacity-40 text-slate-200 rounded">
            {loading ? 'Loading…' : 'Refresh'}
          </button>
        </div>
      </div>

      <div className="bg-slate-800 rounded-xl p-4 space-y-3">
        <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
          <div>
            <label className="block text-xs text-slate-400 mb-1">Engine</label>
            <select value={selectedID} onChange={e => setSelectedID(e.target.value)}
              className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm">
              {engines.length === 0 && <option value="">No engines</option>}
              {engines.map(e => (
                <option key={e.engine_id} value={e.engine_id}>
                  {e.engine_id} {e.running ? '●' : '○'}
                </option>
              ))}
            </select>
          </div>
          <div>
            <label className="block text-xs text-slate-400 mb-1">Tail lines (max 2000)</label>
            <input type="number" min={50} max={2000} step={50}
              value={tailN} onChange={e => setTailN(Math.min(2000, Math.max(50, +e.target.value)))}
              className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm" />
          </div>
          <div>
            <label className="block text-xs text-slate-400 mb-1">Filter (substring)</label>
            <div className="flex gap-2">
              <input type="text" value={grep} placeholder="e.g. AI signal, OPEN GRID, regime"
                onChange={e => setGrep(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') fetchLogs() }}
                className="flex-1 bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm" />
              <button onClick={fetchLogs}
                className="px-2 py-1 text-xs bg-slate-700 hover:bg-slate-600 text-slate-200 rounded">Go</button>
            </div>
          </div>
        </div>
      </div>

      {err && <div className="bg-red-900/30 border border-red-700/50 rounded-lg px-3 py-2 text-red-400 text-sm">{err}</div>}

      <div
        ref={scrollRef}
        onScroll={onScroll}
        className="bg-slate-900 rounded-xl p-3 max-h-[70vh] overflow-y-auto border border-slate-800"
      >
        {lines.length === 0 ? (
          <p className="text-slate-500 text-sm">No log lines{grep ? ` matching "${grep}"` : ''}.</p>
        ) : (
          rendered
        )}
      </div>
      <p className="text-xs text-slate-600 text-right">
        Showing last {lines.length} line(s) of today's process log
        {autoRefresh && stickyRef.current && ' · auto-scroll on'}
        {autoRefresh && !stickyRef.current && ' · scrolled up (auto-scroll paused)'}.
      </p>
    </div>
  )
}
