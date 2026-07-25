import { beforeEach, describe, expect, it } from 'vitest'
import {
  clearSession,
  currentUser,
  getToken,
  isAdmin,
  isAuthenticated,
  setSession,
} from './session'
import type { AuthUser } from './types'

function user(role: AuthUser['role']): AuthUser {
  return { id: 'u1', username: 'alice', role }
}

beforeEach(() => {
  clearSession()
  localStorage.clear()
})

describe('setSession', () => {
  it('exposes the token and user reactively', () => {
    setSession('tok-123', user('user'))
    expect(getToken()).toBe('tok-123')
    expect(isAuthenticated.value).toBe(true)
    expect(currentUser.value?.username).toBe('alice')
  })

  it('mirrors the session to localStorage so a reload stays signed in', () => {
    setSession('tok-123', user('user'))
    expect(localStorage.getItem('sb_token')).toBe('tok-123')
    expect(JSON.parse(localStorage.getItem('sb_user')!)).toEqual(user('user'))
  })
})

describe('clearSession', () => {
  it('drops the token, user, and localStorage mirror', () => {
    setSession('tok-123', user('admin'))
    clearSession()
    expect(getToken()).toBeNull()
    expect(isAuthenticated.value).toBe(false)
    expect(currentUser.value).toBeNull()
    expect(localStorage.getItem('sb_token')).toBeNull()
    expect(localStorage.getItem('sb_user')).toBeNull()
  })
})

describe('role helpers', () => {
  it('does not treat a plain user as an admin', () => {
    setSession('t', user('user'))
    expect(isAdmin.value).toBe(false)
  })

  it('recognizes an admin', () => {
    setSession('t', user('admin'))
    expect(isAdmin.value).toBe(true)
  })

  it('is falsy across the board when signed out', () => {
    expect(isAdmin.value).toBe(false)
    expect(isAuthenticated.value).toBe(false)
  })
})
