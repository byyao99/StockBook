// Package models holds the domain types shared by the db and handler layers.
// They carry GORM tags for persistence and JSON tags for the API, but no
// database or HTTP dependencies of their own.
package models

import "time"

// Role is an account's permission level.
type Role string

const (
	// RoleUser can manage only their own transactions and positions.
	RoleUser Role = "user"
	// RoleAdmin additionally manages the instrument master data and accounts.
	RoleAdmin Role = "admin"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	switch r {
	case RoleUser, RoleAdmin:
		return true
	default:
		return false
	}
}

// User is an account. PasswordHash is never serialized. TokenVersion is
// compared against each bearer token's claim, so bumping it revokes every
// outstanding token for the account.
type User struct {
	ID           string    `gorm:"primaryKey" json:"id"`
	Username     string    `gorm:"uniqueIndex" json:"username"`
	PasswordHash string    `json:"-"`
	Role         Role      `json:"role"`
	TokenVersion int       `gorm:"not null;default:0" json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Currency is the unit an instrument's prices are quoted in. Amounts in
// different currencies are never added together or converted — there is no FX
// rate in this system — so every total is reported per currency.
type Currency string

const (
	CurrencyTWD Currency = "TWD"
	CurrencyUSD Currency = "USD"
)

// Currencies are the supported quote currencies.
var Currencies = []Currency{CurrencyTWD, CurrencyUSD}

// Valid reports whether c is a supported currency.
func (c Currency) Valid() bool {
	return c == CurrencyTWD || c == CurrencyUSD
}

// CanonicalCurrency matches c against Currencies case-insensitively and returns
// the canonical spelling. The second result reports whether a match was found.
func CanonicalCurrency(c string) (Currency, bool) {
	for _, known := range Currencies {
		if equalFold(c, string(known)) {
			return known, true
		}
	}
	return "", false
}

// Markets are the canonical market codes an instrument may belong to.
var Markets = []string{"TWSE", "TPEX", "NYSE", "NASDAQ", "OTHER"}

// DefaultCurrencyForMarket returns the currency instruments on a market are
// normally quoted in, used when a caller does not specify one. OTHER has no
// obvious default and falls back to TWD; callers who care should be explicit.
func DefaultCurrencyForMarket(market string) Currency {
	switch market {
	case "NYSE", "NASDAQ":
		return CurrencyUSD
	default:
		return CurrencyTWD
	}
}

// CanonicalMarket matches m against Markets case-insensitively and returns the
// canonical spelling. The second result reports whether a match was found.
func CanonicalMarket(m string) (string, bool) {
	for _, known := range Markets {
		if equalFold(m, known) {
			return known, true
		}
	}
	return "", false
}

// Instrument is a tradable security in the master data. LastPrice is either
// entered by hand or fetched from a quote provider; nil means no quote has been
// set, which is reported honestly as an unknown market value rather than as zero.
//
// LastPrice is int64 minor units (cents for USD, and the same hundredths scale
// for TWD), like every other monetary field in this system.
//
// Currency is fixed once trades exist against the instrument: changing it would
// silently reinterpret every historical cost basis recorded under it.
//
// The two quote timestamps answer different questions and must not be merged.
// PriceUpdatedAt is when the price was good for — the market timestamp a
// provider reported — and drives the staleness a user sees. QuoteCheckedAt is
// when the provider was last asked, and drives whether asking again is worth
// the outbound call. A Friday closing price fetched on Monday is a day old to
// the reader and a moment old to the fetcher, and both readings are correct.
type Instrument struct {
	ID             string     `gorm:"primaryKey" json:"id"`
	Symbol         string     `gorm:"uniqueIndex" json:"symbol"`
	Name           string     `json:"name"`
	Market         string     `json:"market"`
	Currency       Currency   `gorm:"not null;default:TWD" json:"currency"`
	LastPrice      *int64     `json:"last_price"`
	PriceUpdatedAt *time.Time `json:"price_updated_at"`
	QuoteCheckedAt *time.Time `json:"quote_checked_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// TransactionSide is the direction of a trade.
type TransactionSide string

const (
	// SideBuy adds shares and cost to a position.
	SideBuy TransactionSide = "buy"
	// SideSell removes shares, realizes profit or loss, and releases cost.
	SideSell TransactionSide = "sell"
)

// Valid reports whether s is a known side.
func (s TransactionSide) Valid() bool {
	return s == SideBuy || s == SideSell
}

// Transaction is one entry in a user's ledger: a buy or sell that already
// happened. The ledger is the system of record — positions are derived from it
// and can always be rebuilt by replaying these rows in order.
//
// Symbol is a snapshot taken at entry time so that renaming an instrument never
// rewrites history. Price and Fee are int64 cents; Quantity is whole shares.
// NetAmount is computed by the server (never accepted from the client): the
// cash that left the account on a buy, or arrived on a sell, fees included.
type Transaction struct {
	ID           string          `gorm:"primaryKey" json:"id"`
	UserID       string          `gorm:"index:idx_tx_user_instrument_time,priority:1" json:"user_id"`
	InstrumentID string          `gorm:"index:idx_tx_user_instrument_time,priority:2" json:"instrument_id"`
	Symbol       string          `json:"symbol"`
	Side         TransactionSide `json:"side"`
	Quantity     int             `json:"quantity"`
	Price        int64           `json:"price"`
	Fee          int64           `json:"fee"`
	NetAmount    int64           `json:"net_amount"`
	TradedAt     time.Time       `gorm:"index:idx_tx_user_instrument_time,priority:3" json:"traded_at"`
	Note         string          `json:"note"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// Position is a user's materialized holding of one instrument. It is a cache of
// what replaying the ledger would produce; ReplayPosition in the db layer is the
// authoritative definition.
//
// CostBasis is the *total* remaining cost in cents, not a per-share average —
// storing an average would accumulate rounding error on every trade. The average
// cost is derived for display at the UI edge as CostBasis/Quantity.
//
// The unique index on (user_id, instrument_id) is what makes concurrent writes
// safe: it guarantees at most one row per holding, so a compare-and-swap on
// (quantity, cost_basis) is a real mutual exclusion and not a lost update.
type Position struct {
	ID           string    `gorm:"primaryKey" json:"id"`
	UserID       string    `gorm:"uniqueIndex:idx_position_user_instrument,priority:1" json:"user_id"`
	InstrumentID string    `gorm:"uniqueIndex:idx_position_user_instrument,priority:2" json:"instrument_id"`
	Quantity     int       `gorm:"not null;default:0" json:"quantity"`
	CostBasis    int64     `gorm:"not null;default:0" json:"cost_basis"`
	RealizedPL   int64     `gorm:"not null;default:0" json:"realized_pl"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// equalFold reports whether a and b are equal ignoring ASCII case. Kept local so
// this package stays dependency-free.
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
