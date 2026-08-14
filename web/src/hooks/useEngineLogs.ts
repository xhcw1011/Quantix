import { useEffect, useState } from 'react'
import { getRecentLogs } from '../api/trading'

export function useEngineLogs(engineId: string | undefined) {
  const [lines, setLines] = useState<string[]>([])
  const [grep, setGrep] = useState('')
  const [tailN, setTailN] = useState(300)
  const [autoRefresh, setAutoRefresh] = useState(false)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const fetchLogs = () => {
    if (!engineId) return
    setLoading(true)
    getRecentLogs(engineId, tailN, grep)
      .then((r) => { setLines(r.data.lines || []); setErr(null) })
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      .catch((e: any) => setErr(e.response?.data?.error || '获取日志失败'))
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    fetchLogs()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [engineId, tailN])

  useEffect(() => {
    if (!autoRefresh) return
    const t = setInterval(fetchLogs, 5000)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoRefresh, engineId, tailN, grep])

  return { lines, grep, setGrep, tailN, setTailN, autoRefresh, setAutoRefresh, loading, err, fetchLogs }
}
