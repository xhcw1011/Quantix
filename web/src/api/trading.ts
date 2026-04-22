import api from './client'

export const login = (username: string, password: string) =>
  api.post('/auth/login', { username, password })

export const register = (username: string, email: string, password: string) =>
  api.post('/auth/register', { username, email, password })

export const getMe = () => api.get('/users/me')

// Credentials
export const listCredentials = () => api.get('/credentials')
export const createCredential = (data: object) => api.post('/credentials', data)
export const updateCredential = (id: number, data: object) => api.put(`/credentials/${id}`, data)
export const deleteCredential = (id: number) => api.delete(`/credentials/${id}`)
export const testCredential = (id: number) => api.post(`/credentials/${id}/test`)

// Trading data
export const getOrders = (
  limit = 50, offset = 0,
  symbol = '', strategyId = '', mode = '', from = '', to = '',
) =>
  api.get('/orders', { params: { limit, offset, symbol, strategy_id: strategyId, mode, from, to } })

export const getFills = (
  limit = 50, offset = 0,
  symbol = '', strategyId = '', mode = '', from = '', to = '',
) =>
  api.get('/fills', { params: { limit, offset, symbol, strategy_id: strategyId, mode, from, to } })

export const getPositions = () => api.get('/positions')

export const getEquity = (strategyId?: string, limit = 200) =>
  api.get('/equity', { params: { strategy_id: strategyId, limit } })

export const getSummary = () => api.get('/summary')

// Strategies
export const listStrategies = () => api.get('/strategies')

// Engine — multi-engine (new)
export const listEngines = () => api.get('/engines')
export const startEngine = (data: object) => api.post('/engines', data)
export const stopEngineById = (id: string) => api.delete(`/engines/${id}`)
export const getEngineById = (id: string) => api.get(`/engines/${id}`)

// Engine — legacy (kept for internal use if needed)
export const getEngineStatus = () => api.get('/engine/status')

// Admin
export const adminListUsers = () => api.get('/admin/users')
export const adminSetUserActive = (id: number, active: boolean) =>
  api.put(`/admin/users/${id}/activate`, { active })
export const adminListEngines = () => api.get('/admin/engines')
export const adminForceStopEngine = (userID: number, engineID: string) =>
  api.delete(`/admin/engines/${userID}/${engineID}`)

// Notifications
export const getNotifications = () => api.get('/users/me/notifications')
export const updateNotifications = (data: { tg_bot_token: string; tg_chat_id: number }) =>
  api.put('/users/me/notifications', data)
export const testNotification = () => api.post('/users/me/notifications/test')

// Backtest
export const submitBacktest = (data: object) => api.post('/backtest', data)
export const listBacktests = (limit = 20, offset = 0) =>
  api.get('/backtest', { params: { limit, offset } })
export const getBacktest = (id: string) => api.get(`/backtest/${id}`)
export const deleteBacktest = (id: string) => api.delete(`/backtest/${id}`)
