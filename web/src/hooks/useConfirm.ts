import { createContext, useContext } from 'react'

export interface ConfirmOptions {
  title: string
  message: string // rendered with line breaks preserved (use \n / \n\n)
  confirmLabel?: string
  cancelLabel?: string
  // Destructive actions (real, irreversible exchange orders) get the red
  // treatment; everything else gets the neutral one. Defaults to true since
  // every current call site (stop engine, close position, delete account)
  // is destructive.
  danger?: boolean
}

export type ConfirmFn = (options: ConfirmOptions) => Promise<boolean>

export const ConfirmContext = createContext<ConfirmFn | null>(null)

// useConfirm replaces window.confirm() with a styled in-app dialog that
// matches the rest of the UI instead of the browser's native alert box.
// Usage mirrors window.confirm: `if (!(await confirm({...}))) return`.
export function useConfirm(): ConfirmFn {
  const ctx = useContext(ConfirmContext)
  if (!ctx) throw new Error('useConfirm must be used within <ConfirmProvider>')
  return ctx
}
