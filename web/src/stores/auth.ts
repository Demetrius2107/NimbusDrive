import { create } from 'zustand'

interface AuthState {
  token: string | null
  username: string | null
  setAuth: (token: string, username: string) => void
  logout: () => void
  isAuthenticated: () => boolean
}

export const useAuthStore = create<AuthState>((set, get) => ({
  token: localStorage.getItem('nimbus_token'),
  username: localStorage.getItem('nimbus_username'),
  setAuth: (token, username) => {
    localStorage.setItem('nimbus_token', token)
    localStorage.setItem('nimbus_username', username)
    set({ token, username })
  },
  logout: () => {
    localStorage.removeItem('nimbus_token')
    localStorage.removeItem('nimbus_username')
    set({ token: null, username: null })
  },
  isAuthenticated: () => get().token !== null,
}))
