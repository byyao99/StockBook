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

// Adding an instrument names a listing; everything else about it — the name,
// the currency, the opening price — comes from the price provider, which is the
// authority on what an instrument is called and what it trades in.
export interface InstrumentInput {
  symbol: string
  market: string
}

// What an entry does to a position. A dividend banks a cash payout without
// moving shares or cost: it is income, not a refund of what the shares cost.
export type TransactionSide = 'buy' | 'sell' | 'dividend'
export const SIDES: TransactionSide[] = ['buy', 'sell', 'dividend']

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
  // What this one entry banked, as the ledger fold computed it. null on a buy,
  // which realizes nothing — not the same as banking zero, so it renders as an
  // em dash rather than as $0.00. A sell is null only when the server could not
  // replay its holding, a case the realized report counts and shows.
  realized_pl: number | null
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

// RealizedRow is one instrument's realized result over a reporting period.
//
// The two sources are kept apart: `trading_pl` is what the sales banked and
// `dividends` is what was paid out for holding the shares. `realized_pl` is
// their sum. `proceeds` and `cost` describe the sales alone, so
// trading_pl === proceeds - cost always holds — the identity is against
// trading, never against the combined total.
export interface RealizedRow {
  instrument_id: string
  symbol: string
  name: string
  market: string
  currency: Currency
  realized_pl: number
  trading_pl: number
  dividends: number
  proceeds: number
  cost: number
  sells: number
  dividend_count: number
}

// RealizedSummary totals one currency's realized result over the period. Like
// CurrencySummary the API returns one per currency and never a grand total.
//
// `unstamped_entries` counts sales and dividends whose result the server could
// not compute. They are excluded from the totals rather than counted as zero, so
// a non-zero value means the period is not fully accounted for and the UI must
// say so. It is normally 0.
export interface RealizedSummary {
  currency: Currency
  realized_pl: number
  trading_pl: number
  dividends: number
  proceeds: number
  cost: number
  sells: number
  dividend_count: number
  unstamped_entries: number
  instruments: RealizedRow[]
}

// HindsightRow is one instrument's answer to "what did selling cost me?" —
// what the sales brought in, against what those same shares would be worth at
// today's quote.
//
// `selling_gain` === `proceeds` - `value_if_held` always holds. Negative means
// the shares would be worth more now than the sales fetched.
//
// `shares_held` is the position today, and is there to make a re-entry visible:
// "sold 100, holds 100" means the loss this row reports was not actually taken.
// The report cannot net that out — following the cash from a sale into whatever
// it bought next would need a cash balance, which this system does not have.
export interface HindsightRow {
  instrument_id: string
  symbol: string
  name: string
  market: string
  currency: Currency
  sells: number
  shares_sold: number
  shares_held: number
  proceeds: number
  value_if_held: number
  selling_gain: number
  // The quote the whole comparison rests on.
  last_price: number
}

// HindsightSummary totals one currency's sell decisions over the period. Rows
// come back worst decision first — the regret end is what the report is read for.
//
// `unpriced_sales` counts sales whose instrument has no quote and which are
// therefore left out: with nothing to compare against, valuing those shares at
// zero would report every one of those sales as a masterstroke.
export interface HindsightSummary {
  currency: Currency
  sells: number
  shares_sold: number
  proceeds: number
  value_if_held: number
  selling_gain: number
  unpriced_sales: number
  instruments: HindsightRow[]
}

// ReturnsSummary is one currency's annualized money-weighted rate of return
// (XIRR) over the whole ledger, with the figures behind it.
//
// The period is always since the first entry — never a window. A windowed rate
// needs the market value of the book on the day the window opened, and the
// server keeps only each instrument's current quote, so that value is not
// recoverable.
//
// An open holding with no quote is left out of the calculation ENTIRELY, its
// purchases along with its unknown value: counting the money that went in
// without the value it turned into would report the holding as a wipeout.
// Compare `priced_positions` with `open_positions` to see how much of the book
// the rate covers.
export interface ReturnsSummary {
  currency: Currency
  // Basis points: 1234 means 12.34% a year. null when no rate could be
  // computed, with `unavailable` carrying the reason in words — a book that
  // cannot be measured has not broken even, so it must never render as 0%.
  xirr_bps: number | null
  unavailable?: string
  invested: number
  returned: number
  ending_value: number
  // returned + ending_value - invested.
  net_gain: number
  // The earliest entry counted, so the UI can say what the rate averages over.
  first_flow_at: string | null
  // When the open holdings were valued: the far end of the period.
  as_of: string
  open_positions: number
  priced_positions: number
}

// CurvePoint is one trading session in the book's own history.
//
// `market_value` and `net_invested` are both money and share a scale; the gap
// between them is the gain. Neither is a return — a book that doubles its
// contributions doubles its market value without having earned anything, so
// `index` is the figure that answers performance.
export interface CurvePoint {
  date: string
  market_value: number
  net_invested: number
  // A notional 100 (as 10000 minor units, like every other amount) chained from
  // the daily returns with contributions divided out. Saving harder must not
  // look like skill.
  index: number
}

// CurrencyCurve is one currency's daily history plus the figures derived from
// it. One per currency, like every other total here.
export interface CurrencyCurve {
  currency: Currency
  points: CurvePoint[]
  // Time-weighted return over the window: what a single unit of money left
  // alone in this book would have earned. Deliberately not the same question as
  // ReturnsSummary.xirr_bps, which weights every dollar by how long it was in.
  twr_bps: number | null
  annualized_bps: number | null
  // The deepest peak-to-trough fall of the index, as a POSITIVE number: 2500
  // means the book was once 25% below its high-water mark. It is a depth, not a
  // movement, so it must never render with a leading "+".
  max_drawdown_bps: number | null
  instruments: number
  // Instruments left out entirely for want of stored prices covering the days
  // they were held. Counting one short would understate the book for every day
  // before its prices begin, which looks exactly like a real drawdown.
  without_history: number
  // Why the curve is empty, in words. Empty when there are points. A book with
  // no stored history reads as "sync prices", never as a flat line at zero.
  unavailable?: string
}

// The outcome of one instrument in a price-history sync.
export interface SyncResult {
  instrument_id: string
  symbol: string
  ticker?: string
  status: 'synced' | 'skipped' | 'failed'
  // The window actually requested, which tells an incremental top-up from a
  // first full download.
  from?: string
  to?: string
  // Sessions written by this call, against how many the instrument holds now. A
  // run that adds nothing but already holds years is healthy, and only the
  // second number says so.
  added: number
  sessions: number
  error?: string
}

export interface SyncReport {
  synced: number
  failed: number
  // Instruments with nothing to fetch — never traded, so no history is needed.
  skipped: number
  results: SyncResult[]
}

// A candidate returned by the instrument search, already carrying the exact
// values that would be stored — so adding one types nothing and can mistype
// nothing. `exists` flags a symbol already in the master data.
export interface InstrumentCandidate {
  symbol: string
  name: string
  market: string
  currency: Currency
  // The provider's own full ticker, shown so it is obvious which listing a
  // result refers to when several exchanges carry the same number.
  ticker: string
  exists: boolean
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
