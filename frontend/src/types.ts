// Domain types mirroring the Go API models. Keep this file in sync with
// internal/models/models.go and internal/db/positions.go — it is the contract
// between the two halves of the system.
//
// Every monetary field (`price`, `fee`, `net_amount`, `last_price`,
// `cost_basis`, `realized_pl`, `market_value`, `unrealized_pl`) is integer
// cents; use the helpers in `money.ts` to convert at the UI edge.

export type Role = 'user' | 'admin'
export const ROLES: Role[] = ['user', 'admin']

// Quote currencies. Amounts in different currencies are never added together or
// converted — there is no FX rate in this system — so every total is per currency.
export type Currency = 'TWD' | 'USD'
export const CURRENCIES: Currency[] = ['TWD', 'USD']

// Canonical market codes; mirrors models.Markets on the backend.
export const MARKETS = ['TWSE', 'TPEX', 'NYSE', 'NASDAQ', 'OTHER'] as const

export interface AuthUser {
  id: string
  username: string
  role: Role
  created_at?: string
  updated_at?: string
}

// AuthResponse is returned by /auth/login, /auth/register and /auth/password.
export interface AuthResponse {
  token: string
  user: AuthUser
}

export interface Instrument {
  id: string
  symbol: string
  name: string
  market: string
  // Fixed once trades exist against the instrument: changing it would
  // reinterpret every cost basis already recorded under it.
  currency: Currency
  // The quote, fetched or maintained by hand. null means no quote has been set,
  // which the UI must show as unknown rather than as zero.
  last_price: number | null
  // When the price was good for — the market timestamp, so displayed staleness
  // is the price's own age. This is the one to show a user.
  price_updated_at: string | null
  // When the provider was last asked. Distinct from the above: a Friday close
  // fetched on Monday is a day old to a reader and a moment old to the fetcher.
  // The server uses this to decide whether refetching is worth the call.
  quote_checked_at: string | null
  created_at: string
  updated_at: string
}

export interface InstrumentInput {
  symbol: string
  name: string
  market: string
  // Optional; the server defaults it from the market when omitted.
  currency?: Currency
}

export type TransactionSide = 'buy' | 'sell'
export const SIDES: TransactionSide[] = ['buy', 'sell']

// Transaction is one entry in the ledger. `symbol` is a snapshot taken when the
// entry was made, so renaming an instrument never rewrites history.
export interface Transaction {
  id: string
  user_id: string
  instrument_id: string
  symbol: string
  side: TransactionSide
  quantity: number
  price: number
  fee: number
  // The cash movement, always computed by the server.
  net_amount: number
  traded_at: string
  note: string
  created_at: string
  updated_at: string
}

export interface TransactionInput {
  instrument_id: string
  side: TransactionSide
  quantity: number
  price: number
  fee: number
  traded_at: string
  note: string
}

// TransactionEdit is the editable subset. Side and instrument are absent on
// purpose: changing either would move the entry to a different position, which
// the API expects as a delete plus a fresh entry.
export interface TransactionEdit {
  quantity: number
  price: number
  fee: number
  traded_at: string
  note: string
}

// Position is a holding joined with its instrument.
//
// `cost_basis` is the TOTAL remaining cost, not a per-share average — the
// backend deliberately never stores an average, because rounding one to whole
// cents would drift on every trade. Derive the average with averageCost() in
// positionMath.ts.
//
// `market_value` and `unrealized_pl` are null exactly when `last_price` is,
// meaning the holding cannot currently be valued.
export interface Position {
  id: string
  instrument_id: string
  symbol: string
  name: string
  market: string
  currency: Currency
  quantity: number
  cost_basis: number
  realized_pl: number
  last_price: number | null
  price_updated_at: string | null
  market_value: number | null
  unrealized_pl: number | null
  updated_at: string
}

// CurrencySummary totals the part of the book held in one currency. The API
// returns one per currency the user holds — never a single grand total, which
// would mean adding TWD to USD.
//
// `total_market_value` and `total_unrealized_pl` cover only the holdings that
// have a quote, so `priced_cost_basis` is reported alongside them and the
// identity total_unrealized_pl === total_market_value - priced_cost_basis
// always holds. Compare `priced_positions` with `open_positions` to tell the
// user how much of the book those totals actually account for.
export interface CurrencySummary {
  currency: Currency
  open_positions: number
  priced_positions: number
  total_cost_basis: number
  priced_cost_basis: number
  total_market_value: number
  total_unrealized_pl: number
  total_realized_pl: number
}

// The outcome of one instrument in a quote refresh. `error` carries the
// provider's own wording, which is the only actionable diagnostic for a symbol
// that cannot be fetched.
export interface RefreshResult {
  instrument_id: string
  symbol: string
  // The symbol the provider was actually asked for, derived from the market.
  // A mis-filed market shows up here: 2330 on TPEX is looked up as 2330.TWO.
  ticker?: string
  status: 'updated' | 'skipped' | 'failed'
  last_price?: number
  error?: string
}

export interface RefreshReport {
  updated: number
  failed: number
  // Instruments whose quote was already current and so left untouched. They are
  // counted but deliberately absent from `results`: on a repeat refresh they
  // would be the whole list, and none of them is actionable.
  fresh: number
  results: RefreshResult[]
}
