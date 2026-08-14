interface LeverageSliderProps {
  value: number
  onChange: (v: number) => void
  // Extra explanatory text next to the "杠杆" label (only one of the two
  // call sites in Engine.tsx uses this).
  hint?: string
}

// Dedupes the identical leverage slider that was hand-built twice in
// Engine.tsx (guardian's "顺便帮我开仓" card and the generic leverage card).
export default function LeverageSlider({ value, onChange, hint }: LeverageSliderProps) {
  return (
    <div>
      <div className="flex items-center justify-between mb-1">
        <label className="text-xs text-slate-400">
          杠杆{hint && <span className="text-slate-500 font-normal">{hint}</span>}
        </label>
        <span className="text-sm font-bold text-orange-300">{value}x</span>
      </div>
      <input
        type="range"
        min={1}
        max={20}
        step={1}
        value={value}
        onChange={(e) => onChange(+e.target.value)}
        className="w-full accent-orange-500"
      />
      <div className="flex justify-between text-xs text-slate-500 mt-0.5">
        <span>1x</span>
        <span>10x</span>
        <span>20x</span>
      </div>
    </div>
  )
}
