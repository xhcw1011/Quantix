import { useEffect, useRef, useState } from 'react'

interface SymbolPickerProps {
  value: string
  onChange: (v: string) => void
  options: string[]
  placeholder?: string
}

// A searchable combobox for the trading pair. Shows the current value in a text
// input; focusing/typing opens a dropdown filtered by case-insensitive substring.
// Clicking an option selects it. Typing a symbol not in the list is allowed too —
// onChange fires with the raw uppercased text, so any pair works. Closes on
// outside-click and on Escape.
export default function SymbolPicker({ value, onChange, options, placeholder }: SymbolPickerProps) {
  const [open, setOpen] = useState(false)
  const containerRef = useRef<HTMLDivElement>(null)

  // Close the dropdown when clicking anywhere outside this component.
  useEffect(() => {
    const onMouseDown = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', onMouseDown)
    return () => document.removeEventListener('mousedown', onMouseDown)
  }, [])

  const query = value.trim().toUpperCase()
  const filtered = query === ''
    ? options
    : options.filter((o) => o.includes(query))

  return (
    <div ref={containerRef} className="relative">
      <input
        value={value}
        onChange={(e) => {
          onChange(e.target.value.toUpperCase())
          setOpen(true)
        }}
        onFocus={() => setOpen(true)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            setOpen(false)
          } else if (e.key === 'Escape') {
            setOpen(false)
          }
        }}
        className="w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-slate-500"
        placeholder={placeholder}
      />
      {open && filtered.length > 0 && (
        <ul className="absolute z-20 mt-1 w-full max-h-60 overflow-y-auto bg-slate-700 border border-slate-600 rounded shadow-lg text-sm">
          {filtered.map((o) => (
            <li
              key={o}
              onMouseDown={(e) => {
                // onMouseDown (not onClick) so it fires before the input blur.
                e.preventDefault()
                onChange(o)
                setOpen(false)
              }}
              className={`px-2 py-1.5 cursor-pointer hover:bg-slate-600 ${
                o === value ? 'bg-slate-600 text-white' : 'text-slate-300'
              }`}
            >
              {o}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
