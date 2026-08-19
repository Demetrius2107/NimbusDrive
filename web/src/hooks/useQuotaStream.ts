import { useEffect, useRef } from 'react'
import { useQuotaStore } from '../stores/quota'
import type { QuotaChangeEvent, QuotaSnapshot } from '../lib/quota'

// 轮询降级间隔：SSE 连续失败后以此频率拉 /auth/me 兜底。
const POLL_INTERVAL_MS = 30_000
// 连续失败几次后切到轮询降级。
const FALLBACK_THRESHOLD = 2

/**
 * useQuotaStream 在组件挂载后建立配额推送通道：
 *  1. 先 fetchMe 拿首屏存储配额（SSE 建连前的即时数据）
 *  2. 用浏览器原生 EventSource 连 /api/v1/quota/stream（自动带 Last-Event-ID 重连）
 *  3. snapshot 事件 → setSnapshot；quota 事件 → applyChange
 *  4. EventSource 自带重连，但连续失败超过阈值后退化为 30s 轮询 /auth/me
 *
 * EventSource 不支持自定义 header，但 SSE 端点用 query 传 token：
 * 后端 GinJWTAuth 读取 Authorization 头或 ?token= query（见 middleware）。
 */
export function useQuotaStream() {
  const { fetchMe, setSnapshot, applyChange, setConnState, lastVersion } = useQuotaStore()
  const failCount = useRef(0)
  const pollTimer = useRef<ReturnType<typeof setInterval> | null>(null)
  const esRef = useRef<EventSource | null>(null)

  useEffect(() => {
    let cancelled = false

    const startPolling = () => {
      if (pollTimer.current) return
      setConnState('polling')
      // 立即拉一次，再定时拉。
      void fetchMe()
      pollTimer.current = setInterval(() => {
        if (!cancelled) void fetchMe()
      }, POLL_INTERVAL_MS)
    }

    const stopPolling = () => {
      if (pollTimer.current) {
        clearInterval(pollTimer.current)
        pollTimer.current = null
      }
    }

    const connect = () => {
      if (cancelled) return
      const token = localStorage.getItem('nimbus_token')
      if (!token) return // 未登录，不建连。

      // EventSource 不支持自定义 header，通过 query 传 token。
      const url = `/api/v1/quota/stream?token=${encodeURIComponent(token)}&last=${lastVersion}`
      const es = new EventSource(url)
      esRef.current = es

      es.onopen = () => {
        failCount.current = 0
        stopPolling()
        setConnState('connected')
      }

      es.addEventListener('snapshot', (e) => {
        try {
          const snap = JSON.parse((e as MessageEvent).data) as QuotaSnapshot
          setSnapshot(snap)
        } catch {
          /* 忽略格式错误 */
        }
      })

      es.addEventListener('quota', (e) => {
        try {
          const evt = JSON.parse((e as MessageEvent).data) as QuotaChangeEvent
          applyChange(evt)
        } catch {
          /* 忽略格式错误 */
        }
      })

      // heartbeat 事件无需处理，收到即说明连接存活。

      es.onerror = () => {
        es.close()
        esRef.current = null
        failCount.current += 1
        if (failCount.current >= FALLBACK_THRESHOLD) {
          // EventSource 自带重连，但若服务端不可达（503/网络断），
          // 自动重连会无限失败；切到轮询降级，更省资源且对用户更可预期。
          setConnState('polling')
          startPolling()
        } else {
          setConnState('reconnecting')
          // 短延迟后让 EventSource 重新建连。
          setTimeout(() => {
            if (!cancelled && !esRef.current) connect()
          }, 1500)
        }
      }
    }

    // 1. 首屏数据（不阻塞 SSE 建连）。
    void fetchMe()
    // 2. 建立推送通道。
    connect()

    return () => {
      cancelled = true
      if (esRef.current) {
        esRef.current.close()
        esRef.current = null
      }
      stopPolling()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
}
