import React, { useEffect, Suspense, lazy } from 'react'
import { BrowserRouter, Routes, Route, Navigate, useNavigate, useLocation } from 'react-router-dom'
import { useAuthStore } from './store/authStore'
import Layout from './components/Layout'
import { ConfirmProvider } from './components/ConfirmDialog'
import Toasts from './components/Toasts'

const Login = lazy(() => import('./pages/Login'))
const Dashboard = lazy(() => import('./pages/Dashboard'))
const Fills = lazy(() => import('./pages/Fills'))
const Orders = lazy(() => import('./pages/Orders'))
const Credentials = lazy(() => import('./pages/Credentials'))
const Engine = lazy(() => import('./pages/Engine'))
const EngineDetail = lazy(() => import('./pages/EngineDetail'))
const Admin = lazy(() => import('./pages/Admin'))
const Settings = lazy(() => import('./pages/Settings'))

function PageFallback() {
  return (
    <div className="flex items-center justify-center py-24 text-slate-500 text-sm">
      加载中…
    </div>
  )
}

// ─── Error Boundary ───────────────────────────────────────────────────────────

interface ErrorBoundaryState {
  hasError: boolean
  error: Error | null
}

// Vite content-hashes each lazy chunk's filename; a deploy changes those
// hashes and deletes the old files. A tab that was already open (or loaded
// index.html from cache) before the deploy still tries to fetch the old,
// now-404 chunk on its next route navigation — showing this boundary's
// generic error instead of just picking up the new build. One automatic
// reload fetches the current index.html (and its now-valid chunk hashes)
// instead of leaving the user stuck; guarded to once per tab session so a
// genuinely persistent failure (offline, real server outage) doesn't loop.
const CHUNK_ERROR_RE = /dynamically imported module|Failed to fetch|Loading chunk|error loading dynamically/i
const CHUNK_RELOAD_KEY = 'chunk-reload-attempted'

class ErrorBoundary extends React.Component<
  { children: React.ReactNode },
  ErrorBoundaryState
> {
  state: ErrorBoundaryState = { hasError: false, error: null }

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { hasError: true, error }
  }

  componentDidCatch(error: Error) {
    if (CHUNK_ERROR_RE.test(error?.message ?? '') && !sessionStorage.getItem(CHUNK_RELOAD_KEY)) {
      sessionStorage.setItem(CHUNK_RELOAD_KEY, '1')
      window.location.reload()
    }
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

// The viewport <meta> is document-level, not per-route, but only these routes
// have actually been adapted for narrow screens so far — the one exception is
// /engine's "+ 新建策略" form (~480 lines, no test coverage, real-money order
// risk), which stays un-adapted even though its route is in the mobile set.
// So: real mobile viewport (reflow) on the adapted routes; pinned
// desktop-width viewport (zoom-to-fit, no reflow) everywhere else. Must be
// inside BrowserRouter to access useLocation.
function isMobileAdaptedPath(pathname: string): boolean {
  return (
    pathname === '/' ||
    pathname === '/login' ||
    pathname === '/engine' ||
    pathname.startsWith('/engine/') ||
    pathname === '/orders' ||
    pathname === '/fills' ||
    pathname === '/credentials' ||
    pathname === '/settings' ||
    pathname === '/admin'
  )
}

function ViewportManager() {
  const location = useLocation()
  useEffect(() => {
    const meta = document.querySelector('meta[name="viewport"]')
    if (!meta) return
    meta.setAttribute(
      'content',
      isMobileAdaptedPath(location.pathname) ? 'width=device-width, initial-scale=1' : 'width=1024'
    )
  }, [location.pathname])
  return null
}

// ─── App ──────────────────────────────────────────────────────────────────────

export default function App() {
  // A successful mount means this load's chunks are all valid — clear the
  // guard so a chunk failure from a LATER deploy (later in this same tab
  // session) can still trigger its own one-time auto-reload.
  useEffect(() => { sessionStorage.removeItem(CHUNK_RELOAD_KEY) }, [])
  return (
    <ErrorBoundary>
      <ConfirmProvider>
        <BrowserRouter>
          <Toasts />
          <AuthLogoutListener />
          <ViewportManager />
          <Suspense fallback={<PageFallback />}>
            <Routes>
              <Route path="/login" element={<Login />} />
              <Route
                path="/*"
                element={
                  <ProtectedRoute>
                    <Layout>
                      <ErrorBoundary>
                        <Suspense fallback={<PageFallback />}>
                          <Routes>
                            <Route path="/" element={<Dashboard />} />
                            <Route path="/fills" element={<Fills />} />
                            <Route path="/orders" element={<Orders />} />
                            <Route path="/credentials" element={<Credentials />} />
                            <Route path="/engine" element={<Engine />} />
                            <Route path="/engine/:engineId" element={<EngineDetail />} />
                            <Route path="/admin" element={<AdminRoute><Admin /></AdminRoute>} />
                            <Route path="/settings" element={<Settings />} />
                          </Routes>
                        </Suspense>
                      </ErrorBoundary>
                    </Layout>
                  </ProtectedRoute>
                }
              />
            </Routes>
          </Suspense>
        </BrowserRouter>
      </ConfirmProvider>
    </ErrorBoundary>
  )
}
