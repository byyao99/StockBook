// The only place in the app that talks HTTP. Views call the exported *Api
// objects and never touch fetch directly.
import { clearSession, getToken } from '../session'
import type {
  AuthResponse,
  AuthUser,
  Instrument,
  InstrumentInput,
  PortfolioSummary,
  Position,
  Role,
  Transaction,
  TransactionEdit,
  TransactionInput,
  TransactionSide,
} from '../types'

const BASE = '/api/v1'

interface Envelope<T> {
  data: T
}
interface ErrorBody {
  error?: string
}
export interface PageMeta {
  limit: number
  offset: number
  total: number
}
export interface Page<T> {
  items: T[]
  pagination: PageMeta
}
interface PageResponse<T> {
  data: T[]
  pagination: PageMeta
}

async function parseError(res: Response): Promise<never> {
  const body = (await res.json().catch(() => null)) as ErrorBody | null
  throw new Error(body?.error ?? `Request failed (${res.status})`)
}

function authHeaders(): Record<string, string> {
  const token = getToken()
  return token ? { Authorization: `Bearer ${token}` } : {}
}

// A 401 means the token is gone or revoked, so drop the stale session and let
// the route guard send the user to the login page.
function handleStatus(res: Response): void {
  if (res.status === 401) clearSession()
}

// request unwraps the `{ data }` envelope and throws on non-2xx responses.
async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...authHeaders(), ...init?.headers },
  })
  if (res.status === 204) return undefined as T
  if (!res.ok) {
    handleStatus(res)
    return parseError(res)
  }
  const body = (await res.json()) as Envelope<T>
  return body.data
}

// requestPage returns both the list and its pagination metadata.
async function requestPage<T>(path: string): Promise<Page<T>> {
  const res = await fetch(`${BASE}${path}`, { headers: authHeaders() })
  if (!res.ok) {
    handleStatus(res)
    return parseError(res)
  }
  const body = (await res.json()) as PageResponse<T>
  return { items: body.data, pagination: body.pagination }
}

function pageQuery(limit: number, offset: number): string {
  return `?limit=${limit}&offset=${offset}`
}

export const authApi = {
  login: (username: string, password: string) =>
    request<AuthResponse>('/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  register: (username: string, password: string) =>
    request<AuthResponse>('/auth/register', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  // Changing the password revokes all outstanding tokens; the response carries
  // a fresh session the caller must adopt to stay signed in.
  changePassword: (oldPassword: string, newPassword: string) =>
    request<AuthResponse>('/auth/password', {
      method: 'PUT',
      body: JSON.stringify({ old_password: oldPassword, new_password: newPassword }),
    }),
}

export const userApi = {
  list: (limit = 20, offset = 0) => requestPage<AuthUser>(`/users${pageQuery(limit, offset)}`),
  create: (input: { username: string; password: string; role: Role }) =>
    request<AuthUser>('/users', { method: 'POST', body: JSON.stringify(input) }),
  updateRole: (id: string, role: Role) =>
    request<AuthUser>(`/users/${id}/role`, { method: 'PUT', body: JSON.stringify({ role }) }),
  resetPassword: (id: string, password: string) =>
    request<void>(`/users/${id}/password`, { method: 'PUT', body: JSON.stringify({ password }) }),
  remove: (id: string) => request<void>(`/users/${id}`, { method: 'DELETE' }),
}

export interface InstrumentFilter {
  market?: string
  q?: string
}

export const instrumentApi = {
  list: (limit = 20, offset = 0, filter: InstrumentFilter = {}) => {
    let query = pageQuery(limit, offset)
    if (filter.market) query += `&market=${encodeURIComponent(filter.market)}`
    if (filter.q) query += `&q=${encodeURIComponent(filter.q)}`
    return requestPage<Instrument>(`/instruments${query}`)
  },
  get: (id: string) => request<Instrument>(`/instruments/${id}`),
  create: (input: InstrumentInput) =>
    request<Instrument>('/instruments', { method: 'POST', body: JSON.stringify(input) }),
  update: (id: string, input: InstrumentInput) =>
    request<Instrument>(`/instruments/${id}`, { method: 'PUT', body: JSON.stringify(input) }),
  // Quotes are a separate endpoint from the rest of the record: keeping prices
  // current is routine daily work, editing the master data is not. Passing null
  // clears the quote (and its timestamp) rather than setting it to zero.
  setPrice: (id: string, lastPrice: number | null) =>
    request<Instrument>(`/instruments/${id}/price`, {
      method: 'PATCH',
      body: JSON.stringify({ last_price: lastPrice }),
    }),
  remove: (id: string) => request<void>(`/instruments/${id}`, { method: 'DELETE' }),
}

export interface TransactionFilter {
  instrumentId?: string
  side?: TransactionSide
  from?: string
  to?: string
}

export const transactionApi = {
  list: (limit = 20, offset = 0, filter: TransactionFilter = {}) => {
    let query = pageQuery(limit, offset)
    if (filter.instrumentId) query += `&instrument_id=${encodeURIComponent(filter.instrumentId)}`
    if (filter.side) query += `&side=${filter.side}`
    if (filter.from) query += `&from=${encodeURIComponent(filter.from)}`
    if (filter.to) query += `&to=${encodeURIComponent(filter.to)}`
    return requestPage<Transaction>(`/transactions${query}`)
  },
  get: (id: string) => request<Transaction>(`/transactions/${id}`),
  create: (input: TransactionInput) =>
    request<Transaction>('/transactions', { method: 'POST', body: JSON.stringify(input) }),
  update: (id: string, input: TransactionEdit) =>
    request<Transaction>(`/transactions/${id}`, { method: 'PUT', body: JSON.stringify(input) }),
  remove: (id: string) => request<void>(`/transactions/${id}`, { method: 'DELETE' }),
}

export const positionApi = {
  list: (limit = 20, offset = 0, includeClosed = false) =>
    requestPage<Position>(
      `/positions${pageQuery(limit, offset)}${includeClosed ? '&include_closed=true' : ''}`,
    ),
  summary: () => request<PortfolioSummary>('/positions/summary'),
}
