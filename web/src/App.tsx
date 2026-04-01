import React, { useEffect } from 'react'
import { BrowserRouter, Routes, Route, Navigate, useNavigate } from 'react-router-dom'
import { useAuthStore } from './store/authStore'
import Layout from './components/Layout'
import Login from './pages/Login'
import Dashboard from './pages/Dashboard'
import Fills from './pages/Fills'
import Orders from './pages/Orders'
import Credentials from './pages/Credentials'
import Engine from './pages/Engine'
import Backtest from './pages/Backtest'
import Admin from './pages/Admin'
import Positions from './pages/Positions'
import Settings from './pages/Settings'

// ─── Error Boundary ───────────────────────────────────────────────────────────

interface ErrorBoundaryState {
  hasError: boolean
  error: Error | null
}

class ErrorBoundary extends React.Component<
  { children: React.ReactNode },
  ErrorBoundaryState
> {
  state: ErrorBoundaryState = { hasError: false, error: null }

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { hasError: true, error }
  }

  render() {
    if (this.state.hasError) {
      return (
        <div className="min-h-screen bg-slate-900 flex items-center justify-center p-8">
          <div className="max-w-lg w-full bg-slate-800 rounded-xl p-8 text-center">
            <div className="text-4xl mb-4">⚠️</div>
            <h1 className="text-xl font-bold text-red-400 mb-2">Something went wrong</h1>
            <p className="text-slate-400 text-sm mb-6">
              {this.state.error?.message ?? 'An unexpected error occurred.'}
            </p>
            <button
              onClick={() => this.setState({ hasError: false, error: null })}
              className="bg-blue-600 hover:bg-blue-700 text-white font-medium py-2 px-6 rounded-lg text-sm transition-colors"
            >
              Try again
            </button>
          </div>
        </div>
      )
    }
    return this.props.children
  }
}

// ─── Route guards ─────────────────────────────────────────────────────────────

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const { isAuthenticated } = useAuthStore()
  return isAuthenticated ? <>{children}</> : <Navigate to="/login" replace />
}

function AdminRoute({ children }: { children: React.ReactNode }) {
  const { role } = useAuthStore()
  if (role !== 'admin') return <Navigate to="/" replace />
  return <>{children}</>
}

// AuthLogoutListener handles `auth:logout` events dispatched by the API interceptor.
// Must be inside BrowserRouter to access useNavigate.
function AuthLogoutListener() {
  const navigate = useNavigate()
  useEffect(() => {
    const handler = () => navigate('/login', { replace: true })
    window.addEventListener('auth:logout', handler)
    return () => window.removeEventListener('auth:logout', handler)
  }, [navigate])
  return null
}

// ─── App ──────────────────────────────────────────────────────────────────────

export default function App() {
  return (
    <ErrorBoundary>
      <BrowserRouter>
        <AuthLogoutListener />
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route
            path="/*"
            element={
              <ProtectedRoute>
                <Layout>
                  <ErrorBoundary>
                    <Routes>
                      <Route path="/" element={<Dashboard />} />
                      <Route path="/fills" element={<Fills />} />
                      <Route path="/orders" element={<Orders />} />
                      <Route path="/credentials" element={<Credentials />} />
                      <Route path="/engine" element={<Engine />} />
                      <Route path="/backtest" element={<Backtest />} />
                      <Route path="/positions" element={<Positions />} />
                      <Route path="/admin" element={<AdminRoute><Admin /></AdminRoute>} />
                      <Route path="/settings" element={<Settings />} />
                    </Routes>
                  </ErrorBoundary>
                </Layout>
              </ProtectedRoute>
            }
          />
        </Routes>
      </BrowserRouter>
    </ErrorBoundary>
  )
}
