interface ToggleProps {
  checked: boolean
  onChange: (checked: boolean) => void
  // Tailwind bg-* class for the track when checked=true; different call sites
  // use different accent colors (guardian's emerald vs hedge mode's purple).
  activeColor?: string
}

// Dedupes the hand-rolled switch that was independently built twice in
// Engine.tsx (顺便帮我开仓 / Hedge Mode) with identical markup.
export default function Toggle({ checked, onChange, activeColor = 'bg-emerald-600' }: ToggleProps) {
  return (
    <button
      type="button"
      onClick={() => onChange(!checked)}
      className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors ${
        checked ? activeColor : 'bg-slate-600'
      }`}
    >
      <span
        className={`inline-block h-4 w-4 transform rounded-full bg-white transition-transform ${
          checked ? 'translate-x-6' : 'translate-x-1'
        }`}
      />
    </button>
  )
}
