// 管理后台 API 封装：用户管理/文件审计/操作日志。
// 对接 APIServer (Gin) 的 /admin 接口（需管理员 JWT）。

import { http } from './api'

export interface AdminUser {
  id: number
  username: string
  email: string
  storage_quota: number
  used_storage: number
  status: number // 1=正常 2=封禁
  is_admin: boolean
  created_at: string
  updated_at: string
}

export interface AdminFile {
  id: number
  user_id: number
  name: string
  size: number
  mime_type: string
  is_folder: boolean
  status: string
  created_at: string
  updated_at: string
}

export interface OperationLog {
  id: number
  actor_id?: number
  actor_type: string
  action: string
  target_type?: string
  target_id?: string
  ip?: string
  detail?: Record<string, unknown>
  created_at: string
}

interface ApiData<T> {
  code: string
  message: string
  data: T
}

export async function listUsers(params: {
  page?: number
  page_size?: number
  search?: string
  status?: number
}): Promise<{ users: AdminUser[]; page: number; page_size: number; total: number }> {
  const resp = await http.get<ApiData<{ users: AdminUser[]; page: number; page_size: number; total: number }>>(
    '/admin/users',
    { params },
  )
  return resp.data.data
}

export async function updateUserStatus(id: number, status: number): Promise<void> {
  await http.patch<ApiData<null>>(`/admin/users/${id}/status`, { status })
}

export async function updateUserQuota(id: number, quota: number): Promise<void> {
  await http.patch<ApiData<null>>(`/admin/users/${id}/quota`, { quota })
}

export async function listFiles(params: {
  page?: number
  page_size?: number
  user_id?: number
}): Promise<{ files: AdminFile[]; page: number; page_size: number; total: number }> {
  const resp = await http.get<ApiData<{ files: AdminFile[]; page: number; page_size: number; total: number }>>(
    '/admin/files',
    { params },
  )
  return resp.data.data
}

export async function listLogs(params: {
  page?: number
  page_size?: number
  action?: string
  actor_id?: number
}): Promise<{ logs: OperationLog[]; page: number; page_size: number; total: number }> {
  const resp = await http.get<ApiData<{ logs: OperationLog[]; page: number; page_size: number; total: number }>>(
    '/admin/logs',
    { params },
  )
  return resp.data.data
}
