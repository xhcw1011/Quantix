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

// 市场类型的中文显示
function marketTypeLabel(mt: string): string {
  switch (mt) {
    case 'spot': return '现货'
    case 'swap': return '永续合约'
    case 'futures': return '交割合约'
    default: return mt
  }
}

type HealthState = 'unknown' | 'testing' | 'ok' | 'fail'

interface HealthInfo {
  state: HealthState
  message?: string  // shown as tooltip / under label
  usdt?: number     // balance from successful test
}

// 账户类型：正式实盘(真钱) vs 测试/模拟盘。提交时映射回后端的 testnet/demo 字段。
type AccountType = 'live' | 'sim'

export default function Credentials() {
  const [creds, setCreds] = useState<Credential[]>([])
  const [form, setForm] = useState(initialForm)
  const [accountType, setAccountType] = useState<AccountType>('live')
  const [loading, setLoading] = useState(false)
  const [health, setHealth] = useState<Record<number, HealthInfo>>({})
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')

  const runTest = async (id: number) => {
    setHealth((prev) => ({ ...prev, [id]: { state: 'testing' } }))
    try {
      const r = await testCredential(id)
      if (r.data.ok) {
        setHealth((prev) => ({
          ...prev,
          [id]: { state: 'ok', usdt: r.data.usdt_balance, message: `USDT: ${r.data.usdt_balance?.toFixed(2) ?? '?'}` },
        }))
      } else {
        setHealth((prev) => ({ ...prev, [id]: { state: 'fail', message: r.data.error || 'Connection failed' } }))
      }
    } catch (e: any) {
      setHealth((prev) => ({ ...prev, [id]: { state: 'fail', message: e?.response?.data?.error || 'Request failed' } }))
    }
  }

  const load = () =>
    listCredentials().then((r) => {
      const list: Credential[] = r.data || []
      setCreds(list)
      // Auto-test active credentials on load so the badges populate without a click.
      list.filter((c) => c.is_active).forEach((c) => { runTest(c.id) })
    })

  useEffect(() => { load() }, [])

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault()
    setError(''); setSuccess('')
    setLoading(true)
    try {
      // 把"账户类型"映射回后端字段：
      //  正式实盘        → testnet=false, demo=false
      //  测试/模拟盘 + binance/bybit → testnet=true,  demo=false
      //  测试/模拟盘 + okx           → demo=true,     testnet=false
      const isSim = accountType === 'sim'
      const demo = isSim && form.exchange === 'okx'
      const testnet = isSim && !demo
      await createCredential({ ...form, testnet, demo })
      setSuccess('账户添加成功')
      setForm(initialForm)
      setAccountType('live')
      load()
    } catch (err: any) {
      setError(err.response?.data?.error || '添加账户失败')
    } finally {
      setLoading(false)
    }
  }

  const handleDelete = async (id: number) => {
    if (!confirm('确定要删除这个账户吗?')) return
    await deleteCredential(id)
    load()
  }

  // handleTest is now just a thin alias to runTest (kept for clarity at the call site).
  const handleTest = runTest

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-bold">交易所账户</h1>

      {/* Existing credentials */}
      <div className="bg-slate-800 rounded-xl p-4 space-y-3">
        <h2 className="text-sm font-semibold text-slate-300">已添加的账户</h2>
        {creds.length === 0 ? (
          <p className="text-slate-500 text-sm">还没有添加任何账户。</p>
        ) : (
          creds.map((c) => {
            const h = health[c.id] || { state: 'unknown' as HealthState }
            const dot =
              h.state === 'ok' ? 'bg-green-400'
              : h.state === 'fail' ? 'bg-red-500'
              : h.state === 'testing' ? 'bg-amber-400 animate-pulse'
              : 'bg-slate-500'
            const statusText =
              h.state === 'ok' ? `✅ 已连接 — ${h.message ?? ''}`
              : h.state === 'fail' ? `❌ ${h.message ?? '连接失败'}`
              : h.state === 'testing' ? '连接中…'
              : ''
            return (
            <div key={c.id} className="flex items-center gap-3 p-3 bg-slate-700/50 rounded-lg">
              <span
                className={`w-2.5 h-2.5 rounded-full flex-shrink-0 ${dot}`}
                title={statusText || 'Not tested yet'}
              />
              <div className="flex-1">
                <div className="flex items-center gap-2">
                  <span className="font-medium text-sm">{c.label}</span>
                  <span className="text-xs bg-slate-600 px-1.5 py-0.5 rounded">{c.exchange}</span>
                  {c.testnet && <span className="text-xs bg-yellow-900/50 text-yellow-300 px-1.5 py-0.5 rounded">测试</span>}
                  {c.demo && <span className="text-xs bg-blue-900/50 text-blue-300 px-1.5 py-0.5 rounded">模拟</span>}
                  {!c.testnet && !c.demo && <span className="text-xs bg-green-900/50 text-green-300 px-1.5 py-0.5 rounded">正式</span>}
                  <span className="text-xs text-slate-400">{marketTypeLabel(c.market_type)}</span>
                </div>
                <p className="text-xs text-slate-400 mt-0.5">密钥: {c.api_key_mask}</p>
                {statusText && <p className={`text-xs mt-1 ${h.state === 'fail' ? 'text-red-400' : 'text-slate-300'}`}>{statusText}</p>}
              </div>
              <div className="flex gap-2">
                <button
                  onClick={() => handleTest(c.id)}
                  disabled={h.state === 'testing'}
                  className="text-xs px-2 py-1 bg-blue-600 hover:bg-blue-700 disabled:opacity-50 rounded"
                >
                  {h.state === 'testing' ? '连接中…' : '测试连接'}
                </button>
                <button
                  onClick={() => handleDelete(c.id)}
                  className="text-xs px-2 py-1 bg-red-600/70 hover:bg-red-600 rounded"
                >
                  删除
                </button>
              </div>
            </div>
          )})
        )}
      </div>

      {/* Add credential form */}
      <div className="bg-slate-800 rounded-xl p-4">
        <h2 className="text-sm font-semibold text-slate-300 mb-4">添加账户</h2>
        <form onSubmit={handleCreate} className="space-y-3">
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs text-slate-400 mb-1">交易所</label>
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
              <label className="block text-xs text-slate-400 mb-1">市场类型</label>
              <select
                value={form.market_type}
                onChange={(e) => setForm({ ...form, market_type: e.target.value })}
                className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
              >
                <option value="spot">现货</option>
                <option value="swap">永续合约</option>
                <option value="futures">交割合约</option>
              </select>
            </div>
          </div>

          <div>
            <label className="block text-xs text-slate-400 mb-1">账户类型</label>
            <select
              value={accountType}
              onChange={(e) => setAccountType(e.target.value as AccountType)}
              className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
            >
              <option value="live">正式实盘</option>
              <option value="sim">测试/模拟盘</option>
            </select>
            <p className="text-xs text-slate-500 mt-1">
              正式实盘 = 你的真实交易所账户;测试/模拟盘 = 交易所提供的模拟环境(不涉及真钱)
            </p>
          </div>

          <div>
            <label className="block text-xs text-slate-400 mb-1">备注名</label>
            <input
              value={form.label}
              onChange={(e) => setForm({ ...form, label: e.target.value })}
              className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
              placeholder="例如:我的币安账户"
              required
            />
          </div>
          <div>
            <label className="block text-xs text-slate-400 mb-1">
              API Key <span className="text-slate-500">从交易所后台复制</span>
            </label>
            <input
              value={form.api_key}
              onChange={(e) => setForm({ ...form, api_key: e.target.value })}
              className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm font-mono"
              placeholder="API Key"
              required
            />
          </div>
          <div>
            <label className="block text-xs text-slate-400 mb-1">
              API Secret <span className="text-slate-500">从交易所后台复制</span>
            </label>
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
              <label className="block text-xs text-slate-400 mb-1">Passphrase(OKX 口令)</label>
              <input
                type="password"
                value={form.passphrase}
                onChange={(e) => setForm({ ...form, passphrase: e.target.value })}
                className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm"
                placeholder="OKX Passphrase"
              />
            </div>
          )}

          {error && <p className="text-red-400 text-sm">{error}</p>}
          {success && <p className="text-green-400 text-sm">{success}</p>}

          <button
            type="submit"
            disabled={loading}
            className="px-4 py-2 bg-blue-600 hover:bg-blue-700 disabled:opacity-50 rounded text-sm font-medium"
          >
            {loading ? '保存中...' : '添加账户'}
          </button>
        </form>
      </div>
    </div>
  )
}
