import { http, ApiResult } from './api'

// 与后端 GET /auth/me 返回的 data 字段对齐（仅取配额相关字段）。
export interface MeQuota {
  id: number
  username: string
  storage_quota: number
  used_storage: number
  is_admin: boolean
}

// SSE snapshot 事件的 payload（当月传输配额用量）。
export interface QuotaSnapshot {
  storage_quota?: number
  used_storage?: number
  upload_bytes?: number
  upload_quota?: number
  download_bytes?: number
  download_quota?: number
  period?: string
}

// SSE quota 变更事件（reset_all / user_quota_updated / user_status_updated）。
export interface QuotaChangeEvent {
  version: number
  target_user_id: number
  type: string
  payload: Record<string, unknown>
  timestamp: string
}

// getMe 拉取当前用户信息（含存储配额）。配额页首屏 + SSE 断线轮询共用。
export async function getMe(): Promise<MeQuota> {
  const resp = await http.get<ApiResult<MeQuota>>('/auth/me')
  return resp.data.data!
}
