import axios from 'axios'

const api = axios.create({
  baseURL: '/api',
  headers: { 'Content-Type': 'application/json' },
})

// Inject JWT token from localStorage on every request
api.interceptors.request.use((config) => {
  const token = localStorage.getItem('token')
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

// Redirect to login on 401 (dispatches custom event so React Router handles navigation,
// avoiding hard page refresh which breaks SPA routing state).
api.interceptors.response.use(
  (res) => res,
  (err) => {
    if (err.response?.status === 401) {
      // Clear auth state via store instead of direct localStorage manipulation,
      // so Zustand-derived state stays in sync.
      import('../store/authStore').then(({ useAuthStore }) => {
        useAuthStore.getState().logout()
      })
      window.dispatchEvent(new Event('auth:logout'))
    }
    return Promise.reject(err)
  }
)

export default api
