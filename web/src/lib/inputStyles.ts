// The default text/number/select input style, previously copy-pasted
// verbatim ~34 times across Engine.tsx/Backtest.tsx/Credentials.tsx/Logs.tsx/
// SymbolPicker.tsx. One source of truth so it can't drift again.
export const INPUT_CLASS = 'w-full bg-slate-700 border border-slate-600 rounded px-2 py-1.5 text-sm'

// The compact filter-input style used by Orders.tsx and Fills.tsx -- was
// independently declared as an identical local `inputCls` constant in both
// files. Deliberately NOT merged with INPUT_CLASS or Settings.tsx's own
// (visually different, more spacious) inputCls -- those are genuinely
// different styles, not drift, and unifying them would be a visual change,
// not a dedup.
export const FILTER_INPUT_CLASS =
  'bg-slate-700 border border-slate-600 rounded px-2 py-1 text-sm text-slate-100 focus:outline-none focus:ring-1 focus:ring-blue-500'
