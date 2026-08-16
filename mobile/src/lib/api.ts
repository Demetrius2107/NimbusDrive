import * as SecureStore from 'expo-secure-store'
import { Platform } from 'react-native'
import AsyncStorage from '@react-native-async-storage/async-storage'

const API_BASE = 'http://localhost:8080/api/v1'

export interface ApiResult<T = unknown> {
  code: string
  message: string
  data?: T
}

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

// Token 存储：iOS/Android 用 SecureStore，Web 用 AsyncStorage 降级。
async function getToken(): Promise<string | null> {
  if (Platform.OS === 'web') {
    return await AsyncStorage.getItem('nimbus_token')
  }
  return await SecureStore.getItemAsync('nimbus_token')
}

async function setToken(token: string | null): Promise<void> {
  if (Platform.OS === 'web') {
    if (token) await AsyncStorage.setItem('nimbus_token', token)
    else await AsyncStorage.removeItem('nimbus_token')
    return
  }
  if (token) await SecureStore.setItemAsync('nimbus_token', token)
  else await SecureStore.deleteItemAsync('nimbus_token')
}

export async function request<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const token = await getToken()
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(options.headers as Record<string, string>),
  }
  if (token) headers.Authorization = `Bearer ${token}`

  const resp = await fetch(`${API_BASE}${path}`, { ...options, headers })
  const body = (await resp.json().catch(() => ({}))) as ApiResult<T>

  if (!resp.ok || (body.code && body.code !== '0')) {
    if (resp.status === 401) await setToken(null)
    throw new ApiError(body.code ?? '50001', body.message ?? '请求失败', resp.status)
  }
  return body.data as T
}

export const auth = { setToken, getToken }
