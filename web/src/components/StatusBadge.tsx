// Dedupes the identical status→color map + badge markup that was copy-pasted
// verbatim in Orders.tsx and EngineDetail.tsx.
const STATUS_COLOR: Record<string, string> = {
  FILLED: 'bg-green-900/50 text-green-300',
  CANCELLED: 'bg-slate-600 text-slate-300',
  REJECTED: 'bg-red-900/50 text-red-300',
  PENDING: 'bg-yellow-900/50 text-yellow-300',
  OPEN: 'bg-blue-900/50 text-blue-300',
}

interface StatusBadgeProps {
  status: string
  // Optional display text (e.g. a localized label); colored by the raw
  // `status` regardless, since callers like Orders.tsx show Chinese labels
  // ("已成交") while EngineDetail.tsx shows the raw status verbatim.
  label?: string
}

export default function StatusBadge({ status, label }: StatusBadgeProps) {
  return (
    <span className={`text-xs px-1.5 py-0.5 rounded ${STATUS_COLOR[status] || 'bg-slate-600 text-slate-300'}`}>
      {label ?? status}
    </span>
  )
}
