import { useToastStore } from '../store/toastStore'

const STYLES: Record<string, string> = {
  success: 'bg-green-900/80 border-green-600/60 text-green-100',
  error:   'bg-red-900/80 border-red-600/60 text-red-100',
  info:    'bg-slate-800/90 border-slate-600/60 text-slate-100',
  warning: 'bg-amber-900/80 border-amber-600/60 text-amber-100',
}

const ICONS: Record<string, string> = {
  success: '✓',
  error:   '✕',
  info:    'ℹ',
  warning: '⚠',
}

export default function Toasts() {
  const toasts = useToastStore((s) => s.toasts)
  const dismiss = useToastStore((s) => s.dismiss)

  if (toasts.length === 0) return null

  return (
    <div className="fixed top-4 right-4 z-50 flex flex-col gap-2 max-w-md pointer-events-none">
      {toasts.map((t) => (
        <div
          key={t.id}
          onClick={() => dismiss(t.id)}
          className={`pointer-events-auto cursor-pointer border rounded-lg px-3 py-2 shadow-lg backdrop-blur-sm text-sm flex items-start gap-2 ${STYLES[t.type]}`}
          role="status"
        >
          <span className="font-bold">{ICONS[t.type]}</span>
          <span className="flex-1 break-words">{t.message}</span>
          <button onClick={(e) => { e.stopPropagation(); dismiss(t.id) }} className="text-xs opacity-60 hover:opacity-100">✕</button>
        </div>
      ))}
    </div>
  )
}
