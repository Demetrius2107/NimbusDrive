// 自研分块上传控制器
// 设计要点（详见《设计文档》7.1 上传时序）：
//  - Web Worker 内用 spark-md5 计算 sha256/md5（此处用主线程 spark-md5 占位，后续抽到 worker）
//  - 4MB 分块、最大并发 5
//  - 秒传：先 POST /files/check-hash 命中则跳过上传
//  - 断点续传：GET /upload/{sessionId} 取缺失块再补传
//  - 合并：POST /upload/{sessionId}/complete
//
// 本文件是骨架：接口与状态机已定义，实际网络调用在后端接口就绪后填充。

import SparkMD5 from 'spark-md5'

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

// 计算文件哈希。MVP 用 MD5 占位（spark-md5），生产应换 SHA-256（Web Crypto subtle.digest）
// 或在 Worker 内分块流式计算，避免阻塞主线程。
export async function computeHash(file: File, onProgress?: (percent: number) => void): Promise<string> {
  return new Promise((resolve, reject) => {
    const spark = new SparkMD5.ArrayBuffer()
    const reader = new FileReader()
    const totalChunks = Math.ceil(file.size / CHUNK_SIZE)
    let current = 0

    const loadNext = () => {
      const start = current * CHUNK_SIZE
      const end = Math.min(start + CHUNK_SIZE, file.size)
      reader.readAsArrayBuffer(file.slice(start, end))
    }

    reader.onload = (e) => {
      if (!e.target?.result) return
      spark.append(e.target.result as ArrayBuffer)
      current++
      onProgress?.((current / totalChunks) * 100)
      if (current < totalChunks) {
        loadNext()
      } else {
        resolve(spark.end())
      }
    }
    reader.onerror = () => reject(reader.error)
    loadNext()
  })
}

// 上传单个分块到 TransferServer。后端接口就绪后填充实现。
async function uploadChunk(
  _sessionId: string,
  _index: number,
  _blob: Blob,
  _signal?: AbortSignal,
): Promise<void> {
  // TODO: PUT /upload/{sessionId}/chunks/{index}（流式 body）
  await new Promise((r) => setTimeout(r, 10)) // 占位
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
export async function uploadFile(opts: UploadOptions): Promise<{ fileId: number; instant: boolean }> {
  const { file, onProgress, signal } = opts
  const totalChunks = Math.max(1, Math.ceil(file.size / CHUNK_SIZE))

  // 1. 哈希
  onProgress?.({ phase: 'hashing', totalChunks, uploadedChunks: 0, percent: 0 })
  const hash = await computeHash(file, (p) =>
    onProgress?.({ phase: 'hashing', totalChunks, uploadedChunks: 0, percent: p * 0.3 }),
  )

  // 2. 秒传判定
  onProgress?.({ phase: 'checking', totalChunks, uploadedChunks: 0, percent: 30 })
  // TODO: POST /files/check-hash { hash, size, name, parent_id }
  //   命中 → return { fileId, instant: true }
  //   未命中 → 返回 sessionId / fileId / totalChunks
  const checkResult = await mockCheckHash(hash, file)
  if (checkResult.instant) {
    onProgress?.({ phase: 'completed', totalChunks, uploadedChunks: totalChunks, percent: 100 })
    return { fileId: checkResult.fileId, instant: true }
  }

  // 3. 断点续传：取缺失块
  // TODO: GET /upload/{sessionId} → { missing: [...] }
  const missing = Array.from({ length: totalChunks }, (_, i) => i)

  // 4. 并发上传
  onProgress?.({ phase: 'uploading', totalChunks, uploadedChunks: 0, percent: 30 })
  await uploadChunksConcurrent(
    checkResult.sessionId!,
    file,
    missing,
    (uploaded) =>
      onProgress?.({
        phase: 'uploading',
        totalChunks,
        uploadedChunks: uploaded,
        percent: 30 + (uploaded / totalChunks) * 60,
      }),
    signal,
  )

  // 5. 合并
  onProgress?.({ phase: 'merging', totalChunks, uploadedChunks: totalChunks, percent: 90 })
  // TODO: POST /upload/{sessionId}/complete
  await new Promise((r) => setTimeout(r, 50))

  onProgress?.({ phase: 'completed', totalChunks, uploadedChunks: totalChunks, percent: 100 })
  return { fileId: checkResult.fileId, instant: false }
}

// 临时 mock：后端接口就绪后替换为真实 http 调用。
async function mockCheckHash(
  _hash: string,
  _file: File,
): Promise<{ instant: boolean; fileId: number; sessionId?: string }> {
  return { instant: false, fileId: 0, sessionId: crypto.randomUUID() }
}
