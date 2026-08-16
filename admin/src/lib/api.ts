import axios, { AxiosError } from 'axios'

export interface ApiResult<T = unknown> {
  code: string
  message: string
  data?: T
}

export const http = axios.create({
  baseURL: '/api/v1/admin',
  timeout: 30_000,
})

http.interceptors.request.use((config) => {
  const token = localStorage.getItem('nimbus_admin_token')
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

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
      localStorage.removeItem('nimbus_admin_token')
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
