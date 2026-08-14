import { useEffect, useRef, useState, type ChangeEvent } from 'react'

// Fixes the 2026-08 "只能选上下,不能输入" bug: a controlled numeric <input>
// whose displayed value is re-derived (parsed/rounded/unit-converted) from
// state on every keystroke fights the user mid-type, because Number("0.") /
// Number("1.") / +'' all evaluate to a plain 0/1 and the redisplayed value
// snaps back and strips whatever partial decimal was just typed. The fix:
// keep the DISPLAYED text as a local echo of exactly what was typed:
// parsing/rounding/clamping/unit-conversion only happens on blur, never
// inside onChange.
export interface UseRawNumberFieldOptions {
  value: number | ''
  onCommit: (v: number | '') => void
  min?: number
  max?: number
  // format/parse let a caller display the value in a different unit (e.g.
  // the guardian U/%-toggle card) while keeping the same raw-echo behavior.
  format?: (v: number) => string
  parse?: (s: string) => number
}

export interface RawNumberField {
  text: string
  onChange: (e: ChangeEvent<HTMLInputElement>) => void
  onBlur: () => void
}

const defaultFormat = (v: number) => String(v)
const defaultParse = (s: string) => Number(s)

export function useRawNumberField({
  value,
  onCommit,
  min,
  max,
  format = defaultFormat,
  parse = defaultParse,
}: UseRawNumberFieldOptions): RawNumberField {
  const [text, setText] = useState<string>(value === '' ? '' : format(value))
  // While the user is actively typing, external value changes (including the
  // ones THIS keystroke just triggered via onCommit) must not reformat the
  // field out from under their cursor.
  const editing = useRef(false)

  useEffect(() => {
    if (editing.current) return
    setText(value === '' ? '' : format(value))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value])

  return {
    text,
    onChange: (e) => {
      editing.current = true
      const raw = e.target.value
      setText(raw)
      if (raw.trim() === '') return
      const n = parse(raw)
      // Only commit once the string already parses (e.g. not mid-typing "-"
      // or "0.") -- min/max are deliberately NOT enforced here so the user
      // can type through an intermediate value below min (e.g. "1" then
      // "12" on the way to "123" when min=50) without being clamped early.
      if (Number.isFinite(n)) onCommit(n)
    },
    onBlur: () => {
      editing.current = false
      const raw = text.trim()
      if (raw === '') {
        onCommit('')
        return
      }
      let n = parse(raw)
      if (!Number.isFinite(n)) n = 0
      if (min != null && n < min) n = min
      if (max != null && n > max) n = max
      onCommit(n)
      setText(format(n))
    },
  }
}
