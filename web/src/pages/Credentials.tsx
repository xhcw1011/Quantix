import { useEffect, useState } from 'react'
import {
  listCredentials, createCredential, deleteCredential, testCredential
} from '../api/trading'

interface Credential {
  id: number
  exchange: string
  label: string
  api_key_mask: string
  testnet: boolean
  demo: boolean
  market_type: string
  is_active: boolean
  created_at: string
}

const initialForm = {
  exchange: 'binance',
  label: '',
  api_key: '',
  api_secret: '',
  passphrase: '',
  testnet: false,
  demo: false,
  market_type: 'spot',
}

export default function Credentials() {
  const [creds, setCreds] = useState<Credential[]>([])
  const [form, setForm] = useState(initialForm)
  const [loading, setLoading] = useState(false)
  const [testResults, setTestResults] = useState<Record<number, string>>({})
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')

  const load = () => listCredentials().then((r) => setCreds(r.data || []))
  useEffect(() => { load() }, [])

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(''); setSuccess('')
    setLoading(true)
    try {
      await createCredential(form)
      setSuccess('Credential added successfully')
      setForm(initialForm)
      load()
    } catch (err: any) {
      setError(err.response?.data?.error || 'Failed to create credential')
    } finally {
      setLoading(false)
    }
  }

  const handleDelete = async (id: number) => {
    if (!confirm('Delete this credential?')) return
    await deleteCredential(id)
    load()
  }

  const handleTest = async (id: number) => {
    setTestResults((prev) => ({ ...prev, [id]: 'Testing...' }))
    try {
      const r = await testCredential(id)
      setTestResults((prev) => ({
        ...prev,
        [id]: r.data.ok
          ? `✅ Connected — USDT: ${r.data.usdt_balance?.toFixed(2) || '?'}`
          : `❌ ${r.data.error}`,
      }))
    } catch {
      setTestResults((prev) => ({ ...prev, [id]: '❌ Request failed' }))
    }
  }

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-bold">Exchange Credentials</h1>

      {/* Existing credentials */}
      <div className="bg-slate-800 rounded-xl p-4 space-y-3">
        <h2 className="text-sm font-semibold text-slate-300">Saved Credentials</h2>
        {creds.length === 0 ? (
          <p className="text-slate-500 text-sm">No credentials added yet.</p>
        ) : (
          creds.map((c) => (
            <div key={c.id} className="flex items-center gap-3 p-3 bg-slate-700/50 rounded-lg">
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <span className="font-medium text-sm">{c.label}</span>
                  <span className="text-xs bg-slate-600 px-1.5 py-0.5 rounded">{c.exchange}</span>
                  {c.testnet && <span className="text-xs bg-yellow-900/50 text-yellow-300 px-1.5 py-0.5 rounded">testnet</span>}
                  {c.demo && <span className="text-xs bg-blue-900/50 text-blue-300 px-1.5 py-0.5 rounded">demo</span>}
                  <span className="text-xs text-slate-400">{c.market_type}</span>
                </div>
                <p className="text-xs text-slate-400 mt-0.5">Key: {c.api_key_mask}</p>
                {testResults[c.id] && (
                  <p className="text-xs mt-1">{testResults[c.id]}</p>
                )}
              </div>
              <div className="flex gap-2">
                <button
                  onClick={() => handleTest(c.id)}
                  className="text-xs px-2 py-1 bg-blue-600 hover:bg-blue-700 rounded"
                >
                  Test
                </button>
                <button
                  onClick={() => handleDelete(c.id)}
                  className="text-xs px-2 py-1 bg-red-600/70 hover:bg-red-600 rounded"
                >
                  Delete
                </button>
              </div>
            </div>
          ))
        )}
      </div>

      {/* Add credential form */}
      <div className="bg-slate-800 rounded-xl p-4">
        <h2 className="text-sm font-semibold text-slate-300 mb-4">Add New Credential</h2>
        <form onSubmit={handleCreate} className="space-y-3">
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs text-slate-400 mb-1">Exchange</label>
              <select
                value={form.exchange}
                onChange={(e) => setForm({ ...form, exchange: e.target.value })}
                className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
              >
                <option value="binance">Binance</option>
                <option value="okx">OKX</option>
                <option value="bybit">Bybit</option>
              </select>
            </div>
            <div>
              <label className="block text-xs text-slate-400 mb-1">Market Type</label>
              <select
                value={form.market_type}
                onChange={(e) => setForm({ ...form, market_type: e.target.value })}
                className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
              >
                <option value="spot">Spot</option>
                <option value="swap">Swap (Perpetual)</option>
              </select>
            </div>
          </div>

          <div>
            <label className="block text-xs text-slate-400 mb-1">Label</label>
            <input
              value={form.label}
              onChange={(e) => setForm({ ...form, label: e.target.value })}
              className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
              placeholder="e.g. Binance Testnet"
              required
            />
          </div>
          <div>
            <label className="block text-xs text-slate-400 mb-1">API Key</label>
            <input
              value={form.api_key}
              onChange={(e) => setForm({ ...form, api_key: e.target.value })}
              className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm font-mono"
              placeholder="API Key"
              required
            />
          </div>
          <div>
            <label className="block text-xs text-slate-400 mb-1">API Secret</label>
            <input
              type="password"
              value={form.api_secret}
              onChange={(e) => setForm({ ...form, api_secret: e.target.value })}
              className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm font-mono"
              placeholder="API Secret"
              required
            />
          </div>
          {form.exchange === 'okx' && (
            <div>
              <label className="block text-xs text-slate-400 mb-1">Passphrase (OKX)</label>
              <input
                type="password"
                value={form.passphrase}
                onChange={(e) => setForm({ ...form, passphrase: e.target.value })}
                className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
                placeholder="OKX Passphrase"
              />
            </div>
          )}
          <div className="flex gap-4">
            <label className="flex items-center gap-2 text-sm cursor-pointer">
              <input
                type="checkbox"
                checked={form.testnet}
                onChange={(e) => setForm({ ...form, testnet: e.target.checked })}
                className="rounded"
              />
              Testnet (Binance)
            </label>
            <label className="flex items-center gap-2 text-sm cursor-pointer">
              <input
                type="checkbox"
                checked={form.demo}
                onChange={(e) => setForm({ ...form, demo: e.target.checked })}
                className="rounded"
              />
              Demo (OKX)
            </label>
          </div>

          {error && <p className="text-red-400 text-sm">{error}</p>}
          {success && <p className="text-green-400 text-sm">{success}</p>}

          <button
            type="submit"
            disabled={loading}
            className="px-4 py-2 bg-blue-600 hover:bg-blue-700 disabled:opacity-50 rounded text-sm font-medium"
          >
            {loading ? 'Saving...' : 'Add Credential'}
          </button>
        </form>
      </div>
    </div>
  )
}
