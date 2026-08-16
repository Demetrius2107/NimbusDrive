import axios, { AxiosError } from 'axios'

// 统一响应体（与后端 pkg/response.Body 对齐）
export interface ApiResult<T = unknown> {
  code: string
  message: string
  data?: T
}

export const http = axios.create({
  baseURL: '/api/v1',
  timeout: 30_000,
})

// 请求拦截：注入 JWT
http.interceptors.request.use((config) => {
  const token = localStorage.getItem('nimbus_token')
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

// 响应拦截：拆包统一响应体，错误归一化
http.interceptors.response.use(
  (resp) => {
    const body = resp.data as ApiResult
    if (body && typeof body.code === 'string' && body.code !== '0') {
      return Promise.reject(new ApiError(body.code, body.message, resp.status))
    }
    return resp
  },
  (err: AxiosError<ApiResult>) => {
    if (err.response?.status === 401) {
      localStorage.removeItem('nimbus_token')
      // 跳登录由路由守卫或调用方处理
    }
    const body = err.response?.data
    if (body && typeof body.code === 'string') {
      return Promise.reject(new ApiError(body.code, body.message, err.response!.status))
    }
    return Promise.reject(new ApiError('50001', err.message || '网络错误', err.response?.status ?? 0))
  },
)

export class ApiError extends Error {
  constructor(
    public code: string,
    message: string,
    public httpStatus: number,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}
