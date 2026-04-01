import { useEffect, useRef } from 'react'
import { useAuthStore } from '../store/authStore'

/**
 * useTradeSocket connects to the Quantix WebSocket endpoint and delivers
 * real-time messages (fills, equity updates) to the caller.
 *
 * Auth: passes the JWT as ?token= query param because browsers cannot set
 * custom headers during a WebSocket handshake.
 *
 * Reconnects automatically with 3-second back-off on close/error.
 */
export function useTradeSocket(onMessage: (msg: unknown) => void) {
  const { token } = useAuthStore()
  // Keep the latest onMessage callback in a ref to avoid restarting the
  // socket when the parent re-renders and passes a new callback identity.
  const onMessageRef = useRef(onMessage)
  onMessageRef.current = onMessage

  useEffect(() => {
    if (!token) return

    let ws: WebSocket
    let retryTimer: ReturnType<typeof setTimeout>
    let closed = false

    const connect = () => {
      if (closed) return

      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
      const url = `${protocol}//${window.location.host}/api/ws?token=${encodeURIComponent(token)}`
      ws = new WebSocket(url)

      ws.onmessage = (e) => {
        try {
          onMessageRef.current(JSON.parse(e.data))
        } catch {
          // ignore malformed messages
        }
      }

      ws.onclose = () => {
        if (!closed) {
          retryTimer = setTimeout(connect, 3000)
        }
      }

      ws.onerror = () => {
        ws.close()
      }
    }

    connect()

    return () => {
      closed = true
      clearTimeout(retryTimer)
      ws?.close()
    }
  }, [token])
}
