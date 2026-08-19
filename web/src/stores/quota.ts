import { create } from 'zustand'
import { getMe, type QuotaSnapshot, type QuotaChangeEvent } from '../lib/quota'
import { ApiError } from '../lib/api'

// 配额推送连接状态。
export type QuotaConnState = 'idle' | 'connected' | 'reconnecting' | 'polling'

interface QuotaState {
  // 存储配额（持久累计）
  storageQuota: number
  usedStorage: number
  // 月度传输配额（每月重置）
  uploadBytes: number
  uploadQuota: number
  downloadBytes: number
  downloadQuota: number
  period: string
  // 推送连接状态 + 最近版本号（用于 Last-Event-ID）
  connState: QuotaConnState
  lastVersion: number
  // 标记是否已加载过首屏数据
  loaded: boolean

  setSnapshot: (s: QuotaSnapshot) => void
  applyChange: (evt: QuotaChangeEvent) => void
  fetchMe: () => Promise<void>
  setConnState: (s: QuotaConnState) => void
  reset: () => void
}

export const useQuotaStore = create<QuotaState>((set, get) => ({
  storageQuota: 0,
  usedStorage: 0,
  uploadBytes: 0,
  uploadQuota: 0,
  downloadBytes: 0,
  downloadQuota: 0,
  period: '',
  connState: 'idle',
  lastVersion: 0,
  loaded: false,

  // 应用 SSE snapshot 事件（首屏 + 重连后服务端推送当前状态）。
  setSnapshot: (s) =>
    set({
      storageQuota: s.storage_quota ?? get().storageQuota,
      usedStorage: s.used_storage ?? get().usedStorage,
      uploadBytes: s.upload_bytes ?? get().uploadBytes,
      uploadQuota: s.upload_quota ?? get().uploadQuota,
      downloadBytes: s.download_bytes ?? get().downloadBytes,
      downloadQuota: s.download_quota ?? get().downloadQuota,
      period: s.period ?? get().period,
      loaded: true,
    }),

  // 应用 SSE quota 变更事件：reset_all / user_quota_updated 等类型触发重新拉取。
  // 变更事件只携带"什么变了"，不带全量数据，因此收到后重新拉 /auth/me 拿最新值。
  applyChange: (evt) => {
    set({ lastVersion: evt.version })
    const t = evt.type
    if (t === 'reset_all' || t === 'user_quota_updated' || t === 'user_status_updated') {
      // 拉最新配额；失败不阻塞（下次 snapshot/轮询会兜底）。
      void get().fetchMe()
    }
  },

  // 从 /auth/me 拉取存储配额（SSE 首屏前 + 降级轮询 + 变更后刷新共用）。
  fetchMe: async () => {
    try {
      const me = await getMe()
      set({
        storageQuota: me.storage_quota,
        usedStorage: me.used_storage,
        loaded: true,
      })
    } catch (err) {
      // 401 由 axios 拦截器处理 token 清理，这里不重复处理。
      if (err instanceof ApiError && err.httpStatus === 401) return
      // 其他错误静默：配额展示非关键路径。
    }
  },

  setConnState: (s) => set({ connState: s }),

  reset: () =>
    set({
      storageQuota: 0,
      usedStorage: 0,
      uploadBytes: 0,
      uploadQuota: 0,
      downloadBytes: 0,
      downloadQuota: 0,
      period: '',
      connState: 'idle',
      lastVersion: 0,
      loaded: false,
    }),
}))
