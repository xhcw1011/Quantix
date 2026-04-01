import { useState } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { useAuthStore } from '../store/authStore'

const navItems = [
  { path: '/', label: 'Dashboard', icon: '📊' },
  { path: '/fills', label: 'Fills', icon: '✅' },
  { path: '/orders', label: 'Orders', icon: '📋' },
  { path: '/credentials', label: 'Credentials', icon: '🔑' },
  { path: '/engine', label: 'Engine', icon: '⚙️' },
  { path: '/backtest', label: 'Backtest', icon: '🔬' },
  { path: '/positions', label: 'Positions', icon: '📈' },
  { path: '/settings', label: 'Settings', icon: '🔧' },
]

export default function Layout({ children }: { children: React.ReactNode }) {
  const location = useLocation()
  const navigate = useNavigate()
  const { username, role, logout } = useAuthStore()
  const [menuOpen, setMenuOpen] = useState(false)

  const handleLogout = () => {
    logout()
    navigate('/login')
  }

  const allNavItems = role === 'admin'
    ? [...navItems, { path: '/admin', label: 'Admin', icon: '🛡️' }]
    : navItems

  const sidebarContent = (
    <>
      <div className="flex items-center justify-between mb-6 px-2">
        <span className="text-xl font-bold text-blue-400">⚡ Quantix</span>
        {/* Close button — mobile only */}
        <button
          className="md:hidden text-slate-400 hover:text-white"
          onClick={() => setMenuOpen(false)}
          aria-label="Close menu"
        >
          ✕
        </button>
      </div>
      {allNavItems.map((item) => (
        <Link
          key={item.path}
          to={item.path}
          onClick={() => setMenuOpen(false)}
          className={`flex items-center gap-3 px-3 py-2 rounded-lg text-sm transition-colors ${
            location.pathname === item.path
              ? 'bg-blue-600 text-white'
              : 'text-slate-300 hover:bg-slate-700'
          }`}
        >
          <span>{item.icon}</span>
          {item.label}
        </Link>
      ))}
      <div className="mt-auto pt-4 border-t border-slate-700">
        <div className="text-xs text-slate-400 px-2 mb-1">{username}</div>
        {role === 'admin' && (
          <div className="text-xs text-yellow-500 px-2 mb-2">admin</div>
        )}
        <button
          onClick={handleLogout}
          className="w-full text-left px-3 py-2 text-sm text-slate-400 hover:text-white hover:bg-slate-700 rounded-lg transition-colors"
        >
          🚪 Logout
        </button>
      </div>
    </>
  )

  return (
    <div className="flex min-h-screen bg-slate-900 text-slate-100">
      {/* Desktop sidebar */}
      <aside className="hidden md:flex w-56 bg-slate-800 flex-col py-6 px-4 gap-2 shrink-0">
        {sidebarContent}
      </aside>

      {/* Mobile: top bar + slide-in drawer */}
      <div className="md:hidden fixed top-0 left-0 right-0 z-30 bg-slate-800 flex items-center px-4 py-3 gap-3 border-b border-slate-700">
        <button
          onClick={() => setMenuOpen(true)}
          className="text-slate-300 hover:text-white text-xl leading-none"
          aria-label="Open menu"
        >
          ☰
        </button>
        <span className="text-base font-bold text-blue-400">⚡ Quantix</span>
      </div>

      {/* Mobile drawer backdrop */}
      {menuOpen && (
        <div
          className="md:hidden fixed inset-0 z-40 bg-black/50"
          onClick={() => setMenuOpen(false)}
        />
      )}

      {/* Mobile drawer */}
      <aside className={`md:hidden fixed top-0 left-0 bottom-0 z-50 w-56 bg-slate-800 flex flex-col py-6 px-4 gap-2 transition-transform duration-200 ${
        menuOpen ? 'translate-x-0' : '-translate-x-full'
      }`}>
        {sidebarContent}
      </aside>

      {/* Main content — add top padding on mobile to clear the fixed header */}
      <main className="flex-1 overflow-auto p-6 pt-16 md:pt-6">{children}</main>
    </div>
  )
}
