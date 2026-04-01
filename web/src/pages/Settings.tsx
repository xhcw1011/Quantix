import { useEffect, useState } from 'react'
import api from '../api/client'
import { getNotifications, updateNotifications, testNotification } from '../api/trading'

// ─── Password Section ─────────────────────────────────────────────────────────

function PasswordSection() {
  const [currentPw, setCurrentPw] = useState('')
  const [newPw, setNewPw] = useState('')
  const [confirmPw, setConfirmPw] = useState('')
  const [loading, setLoading] = useState(false)
  const [msg, setMsg] = useState<{ text: string; ok: boolean } | null>(null)

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setMsg(null)
    if (newPw !== confirmPw) { setMsg({ text: 'New passwords do not match', ok: false }); return }
    if (newPw.length < 8) { setMsg({ text: 'New password must be at least 8 characters', ok: false }); return }

    setLoading(true)
    try {
      await api.put('/users/me/password', { current_password: currentPw, new_password: newPw })
      setMsg({ text: 'Password updated successfully', ok: true })
      setCurrentPw(''); setNewPw(''); setConfirmPw('')
    } catch (err: any) {
      setMsg({ text: err?.response?.data?.error ?? 'Failed to update password', ok: false })
    } finally {
      setLoading(false)
    }
  }

  const inputCls = 'w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-slate-100 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500'

  return (
    <div className="bg-slate-800 rounded-xl p-6">
      <h2 className="text-lg font-semibold text-slate-200 mb-4">Change Password</h2>
      <form onSubmit={handleSubmit} className="flex flex-col gap-4">
        <div>
          <label className="block text-sm text-slate-400 mb-1">Current Password</label>
          <input type="password" value={currentPw} onChange={e => setCurrentPw(e.target.value)}
            required className={inputCls} />
        </div>
        <div>
          <label className="block text-sm text-slate-400 mb-1">New Password</label>
          <input type="password" value={newPw} onChange={e => setNewPw(e.target.value)}
            required minLength={8} className={inputCls} />
        </div>
        <div>
          <label className="block text-sm text-slate-400 mb-1">Confirm New Password</label>
          <input type="password" value={confirmPw} onChange={e => setConfirmPw(e.target.value)}
            required className={inputCls} />
        </div>
        {msg && (
          <div className={`text-sm px-3 py-2 rounded-lg ${msg.ok ? 'bg-green-900/40 text-green-400' : 'bg-red-900/40 text-red-400'}`}>
            {msg.text}
          </div>
        )}
        <button type="submit" disabled={loading}
          className="bg-blue-600 hover:bg-blue-700 disabled:opacity-50 text-white font-medium py-2 px-4 rounded-lg text-sm transition-colors">
          {loading ? 'Updating…' : 'Update Password'}
        </button>
      </form>
    </div>
  )
}

// ─── Telegram Section ─────────────────────────────────────────────────────────

function TelegramSection() {
  const [botToken, setBotToken] = useState('')
  const [chatId, setChatId] = useState('')
  const [tokenSet, setTokenSet] = useState(false)
  const [loading, setLoading] = useState(false)
  const [testing, setTesting] = useState(false)
  const [msg, setMsg] = useState<{ text: string; ok: boolean } | null>(null)

  useEffect(() => {
    getNotifications()
      .then(r => {
        setTokenSet(r.data.tg_bot_token_set ?? false)
        setChatId(r.data.tg_chat_id ? String(r.data.tg_chat_id) : '')
      })
      .catch(() => {})
  }, [])

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault()
    setMsg(null)
    const id = parseInt(chatId, 10)
    if (isNaN(id)) { setMsg({ text: 'Chat ID must be a number', ok: false }); return }

    setLoading(true)
    try {
      await updateNotifications({ tg_bot_token: botToken, tg_chat_id: id })
      setMsg({ text: 'Telegram settings saved', ok: true })
      setTokenSet(botToken !== '' || tokenSet)
      setBotToken('') // clear token field after save (server won't return it)
    } catch (err: any) {
      setMsg({ text: err?.response?.data?.error ?? 'Failed to save settings', ok: false })
    } finally {
      setLoading(false)
    }
  }

  const handleTest = async () => {
    setMsg(null)
    setTesting(true)
    try {
      await testNotification()
      setMsg({ text: 'Test notification sent — check your Telegram!', ok: true })
    } catch (err: any) {
      setMsg({ text: err?.response?.data?.error ?? 'Failed to send test notification', ok: false })
    } finally {
      setTesting(false)
    }
  }

  const inputCls = 'w-full bg-slate-700 border border-slate-600 rounded-lg px-3 py-2 text-slate-100 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500'

  return (
    <div className="bg-slate-800 rounded-xl p-6">
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-lg font-semibold text-slate-200">Telegram Notifications</h2>
        {tokenSet && (
          <span className="text-xs bg-green-900/50 text-green-400 px-2 py-0.5 rounded">Configured</span>
        )}
      </div>

      <p className="text-sm text-slate-400 mb-4">
        Receive trade fills, risk alerts, and daily summaries via Telegram.
        Create a bot with{' '}
        <span className="text-blue-400">@BotFather</span> and get your Chat ID from{' '}
        <span className="text-blue-400">@userinfobot</span>.
      </p>

      <form onSubmit={handleSave} className="flex flex-col gap-4">
        <div>
          <label className="block text-sm text-slate-400 mb-1">
            Bot Token{tokenSet && <span className="ml-1 text-xs text-slate-500">(leave blank to keep existing)</span>}
          </label>
          <input type="password" value={botToken} onChange={e => setBotToken(e.target.value)}
            placeholder={tokenSet ? '••••••••••••' : 'e.g. 110201543:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw'}
            className={inputCls} />
        </div>
        <div>
          <label className="block text-sm text-slate-400 mb-1">Chat ID</label>
          <input type="text" value={chatId} onChange={e => setChatId(e.target.value)}
            placeholder="e.g. 123456789" className={inputCls} />
        </div>

        {msg && (
          <div className={`text-sm px-3 py-2 rounded-lg ${msg.ok ? 'bg-green-900/40 text-green-400' : 'bg-red-900/40 text-red-400'}`}>
            {msg.text}
          </div>
        )}

        <div className="flex gap-3">
          <button type="submit" disabled={loading}
            className="bg-blue-600 hover:bg-blue-700 disabled:opacity-50 text-white font-medium py-2 px-4 rounded-lg text-sm transition-colors">
            {loading ? 'Saving…' : 'Save'}
          </button>
          {tokenSet && (
            <button type="button" onClick={handleTest} disabled={testing}
              className="bg-slate-600 hover:bg-slate-500 disabled:opacity-50 text-white font-medium py-2 px-4 rounded-lg text-sm transition-colors">
              {testing ? 'Sending…' : 'Send Test'}
            </button>
          )}
        </div>
      </form>
    </div>
  )
}

// ─── Page ─────────────────────────────────────────────────────────────────────

export default function Settings() {
  return (
    <div className="max-w-lg space-y-6">
      <h1 className="text-2xl font-bold text-slate-100">Settings</h1>
      <PasswordSection />
      <TelegramSection />
    </div>
  )
}
