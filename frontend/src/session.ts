// Client-side auth session: the bearer token and the current user, mirrored to
// localStorage so a reload stays signed in. This module has no dependency on the
// API client, so both the client and the views can import it without a cycle.
import { computed, ref } from 'vue'
import type { AuthUser } from './types'

const TOKEN_KEY = 'sb_token'
const USER_KEY = 'sb_user'

const token = ref<string | null>(localStorage.getItem(TOKEN_KEY))
const user = ref<AuthUser | null>(loadUser())

function loadUser(): AuthUser | null {
  const raw = localStorage.getItem(USER_KEY)
  if (!raw) return null
  try {
    return JSON.parse(raw) as AuthUser
  } catch {
    return null
  }
}

export const isAuthenticated = computed(() => token.value !== null)
export const currentUser = computed(() => user.value)
export const isAdmin = computed(() => user.value?.role === 'admin')

// getToken returns the raw bearer token for the API client to attach.
export function getToken(): string | null {
  return token.value
}

export function setSession(newToken: string, newUser: AuthUser): void {
  token.value = newToken
  user.value = newUser
  localStorage.setItem(TOKEN_KEY, newToken)
  localStorage.setItem(USER_KEY, JSON.stringify(newUser))
}

export function clearSession(): void {
  token.value = null
  user.value = null
  localStorage.removeItem(TOKEN_KEY)
  localStorage.removeItem(USER_KEY)
}
