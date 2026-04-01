import { useEffect, useState } from 'react'
import {
  adminListUsers,
  adminSetUserActive,
  adminListEngines,
  adminForceStopEngine,
} from '../api/trading'

interface AdminUser {
  id: number
  username: string
  email: string
  role: string
  is_active: boolean
  created_at: string
  running_engines: number
}

interface EngineInfo {
  engine_id: string
  user_id: number
  strategy_id: string
  symbol: string
  interval: string
  mode: string
  leverage?: number
  running: boolean
  started_at: string
  error?: string
}

export default function Admin() {
  const [users, setUsers] = useState<AdminUser[]>([])
  const [engines, setEngines] = useState<EngineInfo[]>([])
  const [loadingUsers, setLoadingUsers] = useState(true)
  const [loadingEngines, setLoadingEngines] = useState(true)
  const [togglingId, setTogglingId] = useState<number | null>(null)
  const [stoppingKey, setStoppingKey] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [tab, setTab] = useState<'users' | 'engines'>('users')

  const loadUsers = () => {
    setLoadingUsers(true)
    adminListUsers()
      .then((r) => setUsers(r.data || []))
      .catch(() => setError('Failed to load users'))
      .finally(() => setLoadingUsers(false))
  }

  const loadEngines = () => {
    setLoadingEngines(true)
    adminListEngines()
      .then((r) => setEngines(r.data || []))
      .catch(() => setError('Failed to load engines'))
      .finally(() => setLoadingEngines(false))
  }

  useEffect(() => {
    loadUsers()
    loadEngines()
    const t = setInterval(loadEngines, 10000)
    return () => clearInterval(t)
  }, [])

  const handleToggleActive = async (user: AdminUser) => {
    setTogglingId(user.id)
    setError('')
    try {
      await adminSetUserActive(user.id, !user.is_active)
      loadUsers()
      loadEngines() // engine count may change
    } catch (err: any) {
      setError(err.response?.data?.error || 'Failed to update user')
    } finally {
      setTogglingId(null)
    }
  }

  const handleForceStop = async (userID: number, engineID: string) => {
    const key = `${userID}/${engineID}`
    setStoppingKey(key)
    setError('')
    try {
      await adminForceStopEngine(userID, engineID)
      loadEngines()
    } catch (err: any) {
      setError(err.response?.data?.error || 'Failed to stop engine')
    } finally {
      setStoppingKey(null)
    }
  }

  const runningEngines = engines.filter((e) => e.running)

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-bold">Admin Panel</h1>
        <span className="text-xs text-slate-500">
          {users.length} users · {runningEngines.length} running engines
        </span>
      </div>

      {error && (
        <div className="bg-red-900/30 border border-red-700/50 rounded-lg px-4 py-2 text-red-300 text-sm">
          {error}
        </div>
      )}

      {/* Tab switcher */}
      <div className="flex gap-2">
        {(['users', 'engines'] as const).map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={`px-4 py-1.5 rounded text-sm font-semibold border transition-colors ${
              tab === t
                ? 'bg-blue-600 border-blue-600 text-white'
                : 'bg-transparent border-slate-600 text-slate-400 hover:border-slate-400'
            }`}
          >
            {t === 'users' ? `Users (${users.length})` : `Running Engines (${runningEngines.length})`}
          </button>
        ))}
      </div>

      {/* Users tab */}
      {tab === 'users' && (
        <div className="bg-slate-800 rounded-xl overflow-hidden">
          {loadingUsers ? (
            <div className="p-8 text-center text-slate-500 text-sm">Loading users…</div>
          ) : users.length === 0 ? (
            <div className="p-8 text-center text-slate-500 text-sm">No users found.</div>
          ) : (
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-slate-700 text-xs text-slate-400">
                  <th className="px-4 py-3 text-left">User</th>
                  <th className="px-4 py-3 text-left">Email</th>
                  <th className="px-4 py-3 text-left">Role</th>
                  <th className="px-4 py-3 text-left">Engines</th>
                  <th className="px-4 py-3 text-left">Joined</th>
                  <th className="px-4 py-3 text-left">Status</th>
                </tr>
              </thead>
              <tbody>
                {users.map((u) => (
                  <tr key={u.id} className="border-b border-slate-700/50 hover:bg-slate-700/30 transition-colors">
                    <td className="px-4 py-3">
                      <div className="font-medium">{u.username}</div>
                      <div className="text-xs text-slate-500">#{u.id}</div>
                    </td>
                    <td className="px-4 py-3 text-slate-400">{u.email}</td>
                    <td className="px-4 py-3">
                      <span className={`text-xs px-2 py-0.5 rounded font-semibold ${
                        u.role === 'admin'
                          ? 'bg-yellow-900/50 text-yellow-300'
                          : 'bg-slate-700 text-slate-400'
                      }`}>
                        {u.role}
                      </span>
                    </td>
                    <td className="px-4 py-3">
                      {u.running_engines > 0 ? (
                        <span className="text-green-400 font-semibold">{u.running_engines} running</span>
                      ) : (
                        <span className="text-slate-500">—</span>
                      )}
                    </td>
                    <td className="px-4 py-3 text-slate-400 text-xs">
                      {new Date(u.created_at).toLocaleDateString()}
                    </td>
                    <td className="px-4 py-3">
                      <button
                        onClick={() => handleToggleActive(u)}
                        disabled={togglingId === u.id || u.role === 'admin'}
                        className={`px-3 py-1 rounded text-xs font-semibold disabled:opacity-40 transition-colors ${
                          u.is_active
                            ? 'bg-red-900/40 text-red-300 hover:bg-red-900/70'
                            : 'bg-green-900/40 text-green-300 hover:bg-green-900/70'
                        }`}
                        title={u.role === 'admin' ? 'Cannot deactivate admin' : undefined}
                      >
                        {togglingId === u.id
                          ? '…'
                          : u.is_active ? 'Deactivate' : 'Activate'}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}

      {/* Engines tab */}
      {tab === 'engines' && (
        <div className="space-y-3">
          {loadingEngines ? (
            <div className="bg-slate-800 rounded-xl p-8 text-center text-slate-500 text-sm">
              Loading engines…
            </div>
          ) : runningEngines.length === 0 ? (
            <div className="bg-slate-800 rounded-xl p-8 text-center text-slate-500 text-sm">
              No engines running across all users.
            </div>
          ) : (
            runningEngines.map((eng) => {
              const key = `${eng.user_id}/${eng.engine_id}`
              return (
                <div key={key} className="bg-slate-800 rounded-xl p-4 flex items-start gap-4">
                  <div className="w-2.5 h-2.5 mt-1.5 rounded-full bg-green-400 animate-pulse flex-shrink-0" />
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className="font-semibold text-sm">{eng.engine_id}</span>
                      <span className="text-xs bg-slate-600 text-slate-300 px-1.5 py-0.5 rounded">
                        uid:{eng.user_id}
                      </span>
                      {eng.mode === 'paper' ? (
                        <span className="text-xs bg-blue-900/50 text-blue-300 px-1.5 py-0.5 rounded">paper</span>
                      ) : (
                        <span className="text-xs bg-green-900/50 text-green-300 px-1.5 py-0.5 rounded">live</span>
                      )}
                      {eng.leverage && eng.leverage > 1 && (
                        <span className="text-xs bg-orange-900/50 text-orange-300 px-1.5 py-0.5 rounded font-mono">
                          {eng.leverage}x
                        </span>
                      )}
                    </div>
                    <div className="grid grid-cols-2 md:grid-cols-4 gap-2 mt-2 text-xs text-slate-400">
                      <div><span className="block text-slate-500">Strategy</span>{eng.strategy_id}</div>
                      <div><span className="block text-slate-500">Symbol</span>{eng.symbol}</div>
                      <div><span className="block text-slate-500">Interval</span>{eng.interval}</div>
                      <div><span className="block text-slate-500">Started</span>{new Date(eng.started_at).toLocaleString()}</div>
                    </div>
                  </div>
                  <button
                    onClick={() => handleForceStop(eng.user_id, eng.engine_id)}
                    disabled={stoppingKey === key}
                    className="px-3 py-1.5 bg-red-600 hover:bg-red-700 disabled:opacity-50 rounded text-xs font-semibold flex-shrink-0"
                  >
                    {stoppingKey === key ? 'Stopping…' : '⏹ Force Stop'}
                  </button>
                </div>
              )
            })
          )}
        </div>
      )}
    </div>
  )
}
