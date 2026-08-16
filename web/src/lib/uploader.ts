// 自研分块上传控制器
// 设计要点（详见《设计文档》7.1 上传时序）：
//  - Web Crypto subtle.digest 计算 SHA-256（后端要求 64 位 hex）
//  - 4MB 分块、最大并发 5
//  - 秒传：POST /upload/check-hash 命中则跳过上传
//  - 断点续传：GET /upload/{sessionId} 取缺失块再补传
//  - 合并：POST /upload/{sessionId}/complete

import { http } from './api'

export const CHUNK_SIZE = 4 * 1024 * 1024 // 4MB
export const MAX_CONCURRENCY = 5

export type UploadPhase = 'hashing' | 'checking' | 'uploading' | 'merging' | 'completed' | 'error'

export interface UploadProgress {
  phase: UploadPhase
  totalChunks: number
  uploadedChunks: number
  percent: number
  error?: string
}

export interface UploadOptions {
  file: File
  parentId?: number | null
  onProgress?: (p: UploadProgress) => void
  signal?: AbortSignal
}

export interface UploadResult {
  fileId: number
  instant: boolean
}

// 后端 check-hash 响应体
interface CheckHashResponse {
  instant: boolean
  file_id: number
  session_id?: string
  chunk_size?: number
  total_chunks?: number
}

// 后端 GET /upload/{sessionId} 响应体
interface UploadStatusResponse {
  session_id: string
  total_chunks: number
  missing: number[]
  status: string
}

// 后端 complete 响应体
interface CompleteResponse {
  file_id: number
  storage_path: string
  hash_sha256: string
  size: number
}

// 计算文件 SHA-256 哈希（64 位 hex）。
// MVP 用 file.arrayBuffer() 全量读取 + Web Crypto subtle.digest。
// 局限：超大文件（GB 级）会占用内存；后续可换 Worker 内流式 SHA-256。
export async function computeHash(file: File, onProgress?: (percent: number) => void): Promise<string> {
  // 分块读取以报告进度，最终合并给 subtle.digest。
  const chunks: ArrayBuffer[] = []
  const totalChunks = Math.max(1, Math.ceil(file.size / CHUNK_SIZE))
  let current = 0

  for (let i = 0; i < totalChunks; i++) {
    const start = i * CHUNK_SIZE
    const end = Math.min(start + CHUNK_SIZE, file.size)
    const buf = await file.slice(start, end).arrayBuffer()
    chunks.push(buf)
    current++
    onProgress?.((current / totalChunks) * 100)
  }

  // 合并所有分块为一个 ArrayBuffer。
  const merged = new Uint8Array(file.size)
  let offset = 0
  for (const chunk of chunks) {
    merged.set(new Uint8Array(chunk), offset)
    offset += chunk.byteLength
  }

  const hashBuffer = await crypto.subtle.digest('SHA-256', merged)
  return bufferToHex(hashBuffer)
}

function bufferToHex(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer)
  const hex: string[] = []
  for (let i = 0; i < bytes.length; i++) {
    hex.push(bytes[i].toString(16).padStart(2, '0'))
  }
  return hex.join('')
}

// 秒传判定 / 创建上传会话。
async function checkHash(
  hash: string,
  file: File,
  parentId?: number | null,
): Promise<CheckHashResponse> {
  const resp = await http.post<{ code: string; message: string; data: CheckHashResponse }>(
    '/upload/check-hash',
    {
      hash_sha256: hash,
      size: file.size,
      name: file.name,
      parent_id: parentId ?? null,
    },
  )
  return resp.data.data
}

// 上传单个分块到 TransferServer（PUT 原始字节流）。
async function uploadChunk(
  sessionId: string,
  index: number,
  blob: Blob,
  signal?: AbortSignal,
): Promise<void> {
  await http.put(`/upload/${sessionId}/chunks/${index}`, blob, {
    headers: { 'Content-Type': 'application/octet-stream' },
    timeout: 120_000, // 单块超时放宽到 2 分钟
    signal,
  })
}

// 查询缺失分块（断点续传）。
async function getMissingChunks(sessionId: string, signal?: AbortSignal): Promise<number[]> {
  const resp = await http.get<{ code: string; message: string; data: UploadStatusResponse }>(
    `/upload/${sessionId}`,
    { signal },
  )
  return resp.data.data.missing
}

// 合并分块，完成上传。
async function completeUpload(sessionId: string, signal?: AbortSignal): Promise<CompleteResponse> {
  const resp = await http.post<{ code: string; message: string; data: CompleteResponse }>(
    `/upload/${sessionId}/complete`,
    null,
    { timeout: 300_000, signal }, // 合并大对象可能较慢
  )
  return resp.data.data
}

// 取消上传（用户中止时清理服务端会话）。
export async function cancelUpload(sessionId: string): Promise<void> {
  try {
    await http.delete(`/upload/${sessionId}`)
  } catch {
    // 取消失败不阻塞前端流程
  }
}

// 并发上传控制器：最大 MAX_CONCURRENCY 个分块在飞。
async function uploadChunksConcurrent(
  sessionId: string,
  file: File,
  missing: number[],
  onProgress: (uploaded: number) => void,
  signal?: AbortSignal,
): Promise<void> {
  let uploaded = 0
  const queue = [...missing]
  const inFlight: Promise<void>[] = []

  const startNext = async (): Promise<void> => {
    if (signal?.aborted) throw new Error('aborted')
    const idx = queue.shift()
    if (idx === undefined) return
    const start = idx * CHUNK_SIZE
    const end = Math.min(start + CHUNK_SIZE, file.size)
    await uploadChunk(sessionId, idx, file.slice(start, end), signal)
    uploaded++
    onProgress(uploaded)
    return startNext() // 递归保持并发槽位
  }

  for (let i = 0; i < Math.min(MAX_CONCURRENCY, queue.length); i++) {
    inFlight.push(startNext())
  }
  await Promise.all(inFlight)
}

// 完整上传流程：hash → 秒传判定 → 分块上传 → 合并。
export async function uploadFile(opts: UploadOptions): Promise<UploadResult> {
  const { file, parentId, onProgress, signal } = opts
  const totalChunks = Math.max(1, Math.ceil(file.size / CHUNK_SIZE))

  // 清理回调集合：无论成功/失败/中止，都在 finally 中执行。
  const cleanups: (() => void)[] = []
  const runCleanups = () => {
    for (const fn of cleanups.splice(0)) {
      try {
        fn()
      } catch {
        // 清理失败不影响主流程
      }
    }
  }

  try {
    // 1. 哈希
    onProgress?.({ phase: 'hashing', totalChunks, uploadedChunks: 0, percent: 0 })
    const hash = await computeHash(file, (p) =>
      onProgress?.({ phase: 'hashing', totalChunks, uploadedChunks: 0, percent: p * 0.3 }),
    )

    // 2. 秒传判定 / 创建会话
    onProgress?.({ phase: 'checking', totalChunks, uploadedChunks: 0, percent: 30 })
    const checkResult = await checkHash(hash, file, parentId)
    if (checkResult.instant) {
      onProgress?.({ phase: 'completed', totalChunks, uploadedChunks: totalChunks, percent: 100 })
      return { fileId: checkResult.file_id, instant: true }
    }

    const sessionId = checkResult.session_id!

    // 注册中止回调：用户取消时清理服务端会话，避免僵尸 multipart。
    const onAbort = () => cancelUpload(sessionId)
    signal?.addEventListener('abort', onAbort)
    cleanups.push(() => signal?.removeEventListener('abort', onAbort))

    // 3. 断点续传：取缺失块
    const missing = await getMissingChunks(sessionId, signal)
    if (missing.length === 0) {
      // 所有分块已上传（恢复中断的会话），直接合并
      onProgress?.({ phase: 'merging', totalChunks, uploadedChunks: totalChunks, percent: 90 })
      const result = await completeUpload(sessionId, signal)
      onProgress?.({ phase: 'completed', totalChunks, uploadedChunks: totalChunks, percent: 100 })
      return { fileId: result.file_id, instant: false }
    }

    // 4. 并发上传缺失分块
    onProgress?.({ phase: 'uploading', totalChunks, uploadedChunks: 0, percent: 30 })
    await uploadChunksConcurrent(
      sessionId,
      file,
      missing,
      (uploaded) =>
        onProgress?.({
          phase: 'uploading',
          totalChunks,
          uploadedChunks: uploaded,
          percent: 30 + (uploaded / missing.length) * 60,
        }),
      signal,
    )

    // 5. 合并
    onProgress?.({ phase: 'merging', totalChunks, uploadedChunks: totalChunks, percent: 90 })
    const result = await completeUpload(sessionId, signal)

    onProgress?.({ phase: 'completed', totalChunks, uploadedChunks: totalChunks, percent: 100 })
    return { fileId: result.file_id, instant: false }
  } catch (err) {
    const message = err instanceof Error ? err.message : '上传失败'
    onProgress?.({ phase: 'error', totalChunks, uploadedChunks: 0, percent: 0, error: message })
    throw err
  } finally {
    runCleanups()
  }
}
