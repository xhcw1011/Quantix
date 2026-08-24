import { create } from 'zustand'

export type ToastType = 'success' | 'error' | 'info' | 'warning'

export interface Toast {
  id: number
  type: ToastType
  message: string
}

interface ToastState {
  toasts: Toast[]
  push: (type: ToastType, message: string, ttlMs?: number) => void
  dismiss: (id: number) => void
}

let counter = 0

export const useToastStore = create<ToastState>((set, get) => ({
  toasts: [],
  push: (type, message, ttlMs = 4000) => {
    const id = ++counter
    set((s) => ({ toasts: [...s.toasts, { id, type, message }] }))
    if (ttlMs > 0) {
      window.setTimeout(() => get().dismiss(id), ttlMs)
    }
  },
  dismiss: (id) => set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })),
}))

// Convenience helpers callable outside React components (e.g. axios interceptors).
export const toast = {
  success: (msg: string, ttl?: number) => useToastStore.getState().push('success', msg, ttl),
  error:   (msg: string, ttl?: number) => useToastStore.getState().push('error', msg, ttl ?? 6000),
  info:    (msg: string, ttl?: number) => useToastStore.getState().push('info', msg, ttl),
  warning: (msg: string, ttl?: number) => useToastStore.getState().push('warning', msg, ttl),
}
