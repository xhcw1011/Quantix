import { useState } from 'react'
import { updateEngineParams } from '../api/trading'
import { useTradeSocket } from '../hooks/useTradeSocket'
import { fieldsForStrategy } from '../constants/strategyFields'
import NumberInput from './NumberInput'

// LiveStatus subscribes to WS "status" messages and renders the latest snapshot
// for the given engine_id. Server pushes once per minute from printStatus.
export default function LiveStatus({ engineID, strategyId }: { engineID: string; strategyId?: string }) {
  const [data, setData] = useState<Record<string, any> | null>(null)
  const [lastTs, setLastTs] = useState<number>(0)
  // 守仓风控档 live 编辑
  const [editing, setEditing] = useState(false)
  const [ev, setEv] = useState<Record<string, number | ''>>({})
  const [saving, setSaving] = useState(false)
  const [saveMsg, setSaveMsg] = useState('')

  useTradeSocket((msg: any) => {
    if (msg?.type === 'status' && msg?.data?.engine_id === engineID) {
      setData(msg.data)
      setLastTs(Date.now())
    }
  })

  const guardianFields = fieldsForStrategy('guardian')
  const openEdit = () => {
    const cur = (data?.strat_edit ?? {}) as Record<string, number>
    setEv(Object.fromEntries(guardianFields.map((f) => [f.key, cur[f.key] ?? f.default])))
    setSaveMsg('')
    setEditing(true)
  }
  const saveEdit = async () => {
    setSaving(true)
    setSaveMsg('')
    try {
      const params: Record<string, number> = {}
      for (const f of guardianFields) params[f.key] = (Number(ev[f.key]) || 0) / 100 // % → fraction
      await updateEngineParams(engineID, params)
      setSaveMsg('已保存,立即生效')
      setEditing(false)
    } catch (e: any) {
      setSaveMsg(e.response?.data?.error || '保存失败')
    } finally {
      setSaving(false)
    }
  }

  if (!data) {
    return (
      <p className="text-xs text-slate-600 mt-2">
        实时状态:等待下一次快照(约 60 秒一次)…
      </p>
    )
  }
  const ageS = Math.round((Date.now() - lastTs) / 1000)
  const num = (v: any, d = 2) => typeof v === 'number' ? v.toFixed(d) : String(v ?? '—')
  const stratFields = Object.entries(data).filter(([k]) => k.startsWith('strat_'))
  // 自动守仓 has its own plain-language panel. Detect it by strategy id, or by the
  // guardian-only `strat_state` key when the id isn't available.
  const isGuardian = strategyId === 'guardian' || data.strat_state !== undefined
  return (
    <div className="mt-3 border-t border-slate-700 pt-2 text-xs">
      <div className="flex items-center justify-between text-slate-500 mb-1.5">
        <span>实时状态</span>
        <span>{ageS}秒前更新</span>
      </div>
      <div className="grid grid-cols-2 md:grid-cols-4 gap-x-3 gap-y-1 text-slate-300">
        <div><span className="text-slate-500">权益</span> <span className="font-mono">${num(data.equity)}</span></div>
        <div><span className="text-slate-500">可用资金</span> <span className="font-mono">${num(data.cash)}</span></div>
        <div><span className="text-slate-500">已实现</span> <span className="font-mono">${num(data.realized_pnl)}</span></div>
        <div><span className="text-slate-500">收益率</span> <span className="font-mono">{num(data.total_return_pct)}%</span></div>
        {data.strat_regime !== undefined && (
          <div><span className="text-slate-500">Regime</span> <span className="font-mono">{data.strat_regime}</span></div>
        )}
        {data.strat_has_long !== undefined && (
          <div><span className="text-slate-500">LONG</span> <span className={`font-mono ${data.strat_has_long ? 'text-green-400' : 'text-slate-500'}`}>{data.strat_has_long ? 'open' : '—'}</span></div>
        )}
        {data.strat_has_short !== undefined && (
          <div><span className="text-slate-500">SHORT</span> <span className={`font-mono ${data.strat_has_short ? 'text-red-400' : 'text-slate-500'}`}>{data.strat_has_short ? 'open' : '—'}</span></div>
        )}
        {data.strat_hedge_cooldown_remaining !== undefined && data.strat_hedge_cooldown_remaining !== '0s' && (
          <div><span className="text-slate-500">Hedge CD</span> <span className="font-mono">{data.strat_hedge_cooldown_remaining}</span></div>
        )}
      </div>

      {/* 自动守仓 — 明白话面板 */}
      {isGuardian && data.strat_state !== undefined && (() => {
        const stateMap: Record<string, string> = {
          watching: '守护中', closing: '平仓中', closed: '已结束', arming: '准备中',
        }
        const stateLabel = stateMap[data.strat_state] ?? String(data.strat_state)
        const armed = data.strat_state !== 'arming'
        const sideLabel = data.strat_side === 'long' ? '做多' : data.strat_side === 'short' ? '做空' : '—'
        const pnlR = data.strat_pnl_r
        const pnlText = typeof pnlR === 'number' ? `${pnlR >= 0 ? '+' : ''}${pnlR.toFixed(1)}R` : '—'
        const pnlColor = typeof pnlR === 'number' ? (pnlR >= 0 ? 'text-green-400' : 'text-red-400') : 'text-slate-300'
        return (
          <div className="mt-2 border border-slate-700 rounded-lg bg-slate-900/40 p-3">
            <div className="flex items-center justify-between mb-2">
              <span className="text-xs font-semibold text-slate-300">自动守仓 · {data.strat_symbol ?? ''}</span>
              <div className="flex items-center gap-2">
                {armed && !editing && (
                  <button type="button" onClick={openEdit} className="text-xs px-2 py-0.5 rounded bg-slate-700 hover:bg-slate-600 text-slate-200">✎ 改风控档</button>
                )}
                <span className="text-xs px-1.5 py-0.5 rounded bg-slate-700 text-slate-200">{stateLabel}</span>
              </div>
            </div>
            {armed ? (
              <div className="grid grid-cols-2 md:grid-cols-4 gap-x-3 gap-y-1.5 text-slate-300">
                <div><span className="block text-slate-500">方向</span>{sideLabel}</div>
                <div><span className="block text-slate-500">成本价</span><span className="font-mono">{num(data.strat_entry)}</span></div>
                <div><span className="block text-slate-500">数量</span><span className="font-mono">{num(data.strat_qty, 4)}</span></div>
                <div><span className="block text-slate-500">当前止损价</span><span className="font-mono">{num(data.strat_stop)}</span></div>
                <div><span className="block text-slate-500">浮盈</span><span className={`font-mono ${pnlColor}`}>{pnlText}</span></div>
                <div><span className="block text-slate-500">止损保护</span>{data.strat_trail_active ? '已上移锁利' : '未激活'}</div>
                {typeof data.strat_tp === 'number' && data.strat_tp > 0 && (
                  <div><span className="block text-slate-500">止盈价</span><span className="font-mono">{num(data.strat_tp)}</span></div>
                )}
              </div>
            ) : (
              <p className="text-slate-400">正在准备守护你的仓位…</p>
            )}
            {editing && (
              <div className="mt-3 border-t border-slate-700 pt-2">
                <p className="text-[10px] text-slate-500 mb-2">改完立即生效,不停引擎、不动仓位。止损在已开始移动后只能收紧、不能放宽。</p>
                <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
                  {guardianFields.map((f) => (
                    <div key={f.key}>
                      <label className="block text-[11px] text-slate-400 mb-0.5">{f.label} (%)</label>
                      <NumberInput
                        min={0}
                        value={ev[f.key] ?? ''}
                        onChange={(v) => setEv((p) => ({ ...p, [f.key]: v }))}
                        className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1 text-sm"
                      />
                    </div>
                  ))}
                </div>
                <div className="flex items-center gap-2 mt-2">
                  <button type="button" onClick={saveEdit} disabled={saving} className="px-3 py-1 bg-emerald-600 hover:bg-emerald-700 disabled:opacity-50 rounded text-xs font-semibold">{saving ? '保存中…' : '保存'}</button>
                  <button type="button" onClick={() => setEditing(false)} className="px-3 py-1 bg-slate-700 hover:bg-slate-600 rounded text-xs">取消</button>
                  {saveMsg && <span className="text-[11px] text-slate-400">{saveMsg}</span>}
                </div>
              </div>
            )}
            {!editing && saveMsg && <p className="text-[11px] text-green-400 mt-1.5">{saveMsg}</p>}
          </div>
        )
      })()}

      {stratFields.length > 0 && (
        <details className="mt-2">
          <summary className="text-slate-500 cursor-pointer">策略详情 ({stratFields.length} 项)</summary>
          <pre className="text-[10px] text-slate-400 mt-1 overflow-x-auto">{JSON.stringify(Object.fromEntries(stratFields), null, 2)}</pre>
        </details>
      )}
    </div>
  )
}
