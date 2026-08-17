// 分享模块 API 封装：创建/列表/取消/公开访问/校验密码。
// 对接 APIServer (Gin) 的 /files/:id/share、/shares、/s 接口。

import { http } from './api'

export interface Share {
  id: string
  user_id?: number
  file_id: number
  has_password: boolean
  expires_at?: string | null
  max_access?: number | null
  access_count: number
  status: string
  created_at?: string
}

export interface ShareListResponse {
  shares: Share[]
  page: number
  page_size: number
  total: number
}

export interface ValidateResult {
  access_allowed: boolean
  access_count_remaining: number | null
  expires_in: number | null
  file_id: number
}

export interface CreateShareOptions {
  password?: string
  expire_time?: string
  max_access_count?: number
}

interface ApiData<T> {
  code: string
  message: string
  data: T
}

// createShare 对指定文件创建分享。
export async function createShare(fileId: number, opts: CreateShareOptions): Promise<Share> {
  const resp = await http.post<ApiData<Share>>(`/files/${fileId}/share`, opts)
  return resp.data.data
}

// listShares 列出我的分享。
export async function listShares(page = 1, pageSize = 50): Promise<ShareListResponse> {
  const resp = await http.get<ApiData<ShareListResponse>>('/shares', {
    params: { page, page_size: pageSize },
  })
  return resp.data.data
}

// cancelShare 取消分享。
export async function cancelShare(id: string): Promise<void> {
  await http.delete<ApiData<null>>(`/shares/${id}`)
}

// getShare 公开查看分享详情。
export async function getShare(id: string): Promise<Share> {
  const resp = await http.get<ApiData<Share>>(`/s/${id}`)
  return resp.data.data
}

// validateShare 校验密码并自增访问计数。
export async function validateShare(id: string, password: string): Promise<ValidateResult> {
  const resp = await http.post<ApiData<ValidateResult>>(`/s/${id}/validate`, { password })
  return resp.data.data
}
