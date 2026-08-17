// 文件管理 API 封装：列表/文件夹/移动/重命名/删除/回收站。
// 对接 APIServer (Gin) 的 /files 和 /trash 接口。

import { http } from './api'

export interface FileNode {
  id: number
  user_id: number
  parent_id: number | null
  name: string
  size: number
  mime_type: string
  is_folder: boolean
  hash_sha256?: string
  chunk_count: number
  status: string
  storage_path?: string
  deleted_at?: string
  created_at: string
  updated_at: string
}

export interface FileListResponse {
  files: FileNode[]
  page: number
  page_size: number
  total: number
}

interface ApiData<T> {
  code: string
  message: string
  data: T
}

// 列出文件
export async function listFiles(
  parentId?: number | null,
  page = 1,
  pageSize = 50,
): Promise<FileListResponse> {
  const params: Record<string, string | number> = { page, page_size: pageSize }
  if (parentId != null) params.parent_id = parentId
  const resp = await http.get<ApiData<FileListResponse>>('/files', { params })
  return resp.data.data
}

// 创建文件夹
export async function createFolder(
  name: string,
  parentId?: number | null,
): Promise<{ id: number; name: string }> {
  const resp = await http.post<ApiData<{ id: number; name: string }>>('/files/folder', {
    name,
    parent_id: parentId ?? null,
  })
  return resp.data.data
}

// 移动文件
export async function moveFile(id: number, parentId?: number | null): Promise<void> {
  await http.post(`/files/${id}/move`, { parent_id: parentId ?? null })
}

// 重命名
export async function renameFile(id: number, name: string): Promise<void> {
  await http.post(`/files/${id}/rename`, { name })
}

// 软删除（移入回收站）
export async function deleteFile(id: number): Promise<void> {
  await http.delete(`/files/${id}`)
}

// 回收站列表
export async function listTrash(page = 1, pageSize = 50): Promise<FileListResponse> {
  const resp = await http.get<ApiData<FileListResponse>>('/trash', {
    params: { page, page_size: pageSize },
  })
  return resp.data.data
}

// 恢复
export async function restoreFile(id: number): Promise<void> {
  await http.post(`/trash/${id}/restore`)
}

// 彻底删除
export async function permanentDelete(id: number): Promise<void> {
  await http.delete(`/trash/${id}`)
}
