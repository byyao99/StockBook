// The only place in the app that talks HTTP. Views call the exported *Api
// objects and never touch fetch directly.
import { clearSession, getToken } from '../session'
import type {
  AuthResponse,
  AuthUser,
  CurrencyCurve,
  Instrument,
  InstrumentCandidate,
  InstrumentInput,
  CurrencySummary,
  HindsightSummary,
  Position,
  RealizedSummary,
  RefreshReport,
  ReturnsSummary,
  Role,
  SyncReport,
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
  // Looks a symbol or company name up with the market-data provider. Candidates
  // come back with the symbol, market and currency already resolved, so adding
  // one is a click rather than four fields of typing.
  search: (q: string) =>
    request<InstrumentCandidate[]>(`/instruments/search?q=${encodeURIComponent(q)}`),
  create: (input: InstrumentInput) =>
    request<Instrument>('/instruments', { method: 'POST', body: JSON.stringify(input) }),
  // Only the display name is editable: symbol and market decide how the
  // instrument is priced, and currency belongs to the provider.
  rename: (id: string, name: string) =>
    request<Instrument>(`/instruments/${id}`, { method: 'PUT', body: JSON.stringify({ name }) }),
  // Quotes are a separate endpoint from the rest of the record: keeping prices
  // current is routine daily work, editing the master data is not. Passing null
  // clears the quote (and its timestamp) rather than setting it to zero.
  setPrice: (id: string, lastPrice: number | null) =>
    request<Instrument>(`/instruments/${id}/price`, {
      method: 'PATCH',
      body: JSON.stringify({ last_price: lastPrice }),
    }),
  remove: (id: string) => request<void>(`/instruments/${id}`, { method: 'DELETE' }),
  // Fetches quotes for every instrument the provider can address. A partial
  // failure is reported per symbol rather than failing the whole call, so the
  // caller sees exactly which tickers are wrong and what the provider said.
  refreshQuotes: () =>
    request<RefreshReport>('/instruments/refresh-quotes', { method: 'POST' }),
  // Downloads the daily closes the equity curve is drawn from. Only traded
  // instruments are fetched, and only from the day they were first traded, so a
  // run is proportional to the book rather than to the master data. A first run
  // downloads years and is slow; later ones top up a few sessions.
  syncHistory: () => request<SyncReport>('/instruments/sync-history', { method: 'POST' }),
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
  summary: () => request<CurrencySummary[]>('/positions/summary'),
}

// The `?from=&to=` shared by the reports that take a period. Both bounds are
// YYYY-MM-DD and inclusive: the server stretches a bare `to` to the end of that
// day, so a year ends on 12-31 rather than the day before it. Either may be
// omitted, which leaves that end open.
function dateRangeQuery(from?: string, to?: string): string {
  const params = new URLSearchParams()
  if (from) params.set('from', from)
  if (to) params.set('to', to)
  const query = params.toString()
  return query ? `?${query}` : ''
}

export const reportApi = {
  // What the caller banked over the period, one entry per currency.
  realized: (from?: string, to?: string) =>
    request<RealizedSummary[]>(`/reports/realized${dateRangeQuery(from, to)}`),

  // The annualized money-weighted return, one entry per currency. It takes no
  // date range on purpose: a windowed rate needs the book's market value on the
  // opening day, which the server cannot recover from current quotes alone.
  returns: () => request<ReturnsSummary[]>('/reports/returns'),

  // What the sales in a period would be worth had the shares never been sold.
  // The bounds behave exactly as realized's do — this one takes a period
  // happily, since the comparison is always against today's quote.
  hindsight: (from?: string, to?: string) =>
    request<HindsightSummary[]>(`/reports/hindsight${dateRangeQuery(from, to)}`),

  // The book's own daily history, one entry per currency. Its bounds select
  // SESSIONS, not trades: the whole ledger is always folded and the window only
  // decides which days are drawn, so narrowing to last month still shows
  // holdings bought years ago.
  curve: (from?: string, to?: string) =>
    request<CurrencyCurve[]>(`/reports/curve${dateRangeQuery(from, to)}`),
}
