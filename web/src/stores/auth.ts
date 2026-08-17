import { create } from 'zustand'

interface AuthState {
  token: string | null
  username: string | null
  isAdmin: boolean
  setAuth: (token: string, username: string, isAdmin: boolean) => void
  logout: () => void
  isAuthenticated: () => boolean
}

export const useAuthStore = create<AuthState>((set, get) => ({
  token: localStorage.getItem('nimbus_token'),
  username: localStorage.getItem('nimbus_username'),
  isAdmin: localStorage.getItem('nimbus_is_admin') === 'true',
  setAuth: (token, username, isAdmin) => {
    localStorage.setItem('nimbus_token', token)
    localStorage.setItem('nimbus_username', username)
    localStorage.setItem('nimbus_is_admin', String(isAdmin))
    set({ token, username, isAdmin })
  },
  logout: () => {
    localStorage.removeItem('nimbus_token')
    localStorage.removeItem('nimbus_username')
    localStorage.removeItem('nimbus_is_admin')
    set({ token: null, username: null, isAdmin: false })
  },
  isAuthenticated: () => get().token !== null,
}))
