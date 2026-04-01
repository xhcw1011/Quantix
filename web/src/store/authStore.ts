import { create } from 'zustand'

interface AuthState {
  token: string | null
  userID: number | null
  username: string | null
  role: string | null
  isAuthenticated: boolean
  login: (token: string, userID: number, username: string, role: string) => void
  logout: () => void
}

export const useAuthStore = create<AuthState>((set) => ({
  token: localStorage.getItem('token'),
  userID: Number(localStorage.getItem('userID')) || null,
  username: localStorage.getItem('username'),
  role: localStorage.getItem('role'),
  isAuthenticated: !!localStorage.getItem('token'),

  login: (token, userID, username, role) => {
    localStorage.setItem('token', token)
    localStorage.setItem('userID', String(userID))
    localStorage.setItem('username', username)
    localStorage.setItem('role', role)
    set({ token, userID, username, role, isAuthenticated: true })
  },

  logout: () => {
    localStorage.removeItem('token')
    localStorage.removeItem('userID')
    localStorage.removeItem('username')
    localStorage.removeItem('role')
    set({ token: null, userID: null, username: null, role: null, isAuthenticated: false })
  },
}))
