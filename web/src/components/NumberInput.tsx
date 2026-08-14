import { useRawNumberField } from '../hooks/useRawNumberField'
import { INPUT_CLASS } from '../lib/inputStyles'

interface NumberInputProps {
  value: number | ''
  onChange: (v: number | '') => void
  min?: number
  max?: number
  placeholder?: string
  className?: string
  disabled?: boolean
}

// See hooks/useRawNumberField.ts for why this exists: fixes the "只能选上下,
// 不能输入" bug by never reformatting the displayed text while the user is
// typing -- only on blur.
export default function NumberInput({
  value,
  onChange,
  min,
  max,
  placeholder,
  className,
  disabled,
}: NumberInputProps) {
  const field = useRawNumberField({ value, onCommit: onChange, min, max })
  return (
    <input
      type="text"
      inputMode="decimal"
      value={field.text}
      onChange={field.onChange}
      onBlur={field.onBlur}
      placeholder={placeholder}
      disabled={disabled}
      className={className ?? INPUT_CLASS}
    />
  )
}
