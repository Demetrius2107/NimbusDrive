// 文件下载工具
// MVP 走单连接流式：fetch GET /download/:fileId → blob → <a download> 触发保存。
// 后续可扩展为分块并发下载（复用 uploader 的并发模式 + Range 请求）。

import { http, ApiError } from './api'

export interface DownloadOptions {
  fileId: number
  fileName: string
  signal?: AbortSignal
}

// 触发浏览器下载一个文件。
// 通过 axios 请求字节流，拿到 Blob 后用 <a> 标签触发保存。
export async function downloadFile(opts: DownloadOptions): Promise<void> {
  const { fileId, fileName, signal } = opts

  const resp = await http.get(`/download/${fileId}`, {
    responseType: 'blob',
    timeout: 0, // 下载不限超时
    signal,
  })

  const blob = resp.data as Blob
  triggerBrowserDownload(blob, fileName)
}

// 预检文件大小（HEAD 请求），用于决定下载策略。
export async function getFileSize(fileId: number, signal?: AbortSignal): Promise<number> {
  const resp = await http.head(`/download/${fileId}`, { signal })
  const len = resp.headers['content-length']
  if (typeof len === 'string') {
    return parseInt(len, 10) || 0
  }
  return 0
}

// 用 Blob + <a download> 触发浏览器保存文件。
function triggerBrowserDownload(blob: Blob, fileName: string): void {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = fileName
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  // 延迟释放，避免下载尚未开始就 revoke。
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

// 下载失败时将 Blob 错误体转为可读消息。
// 后端统一响应体是 JSON {code, message}，axios responseType=blob 时 error.response.data 也是 Blob。
export async function extractDownloadError(err: unknown): Promise<string> {
  if (err instanceof ApiError) {
    return err.message
  }
  // 尝试从 Blob 响应中解析业务错误。
  const blobData = (err as { response?: { data?: Blob } })?.response?.data
  if (blobData instanceof Blob) {
    try {
      const text = await blobData.text()
      const parsed = JSON.parse(text) as { message?: string }
      if (parsed.message) return parsed.message
    } catch {
      // 解析失败走通用错误
    }
  }
  if (err instanceof Error) return err.message
  return '下载失败'
}
