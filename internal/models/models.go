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
// AssetType is the provider's own word for what this is ("EQUITY", "ETF"),
// recorded so the system can tell an ETF from an ordinary share after the fact.
// It is empty for an instrument created before the column existed and for one
// the provider did not classify; refreshOne fills those in opportunistically
// rather than a startup backfill reaching for the network. Nothing depends on
// it being right — it only picks which fee profile a trade form suggests, which
// the user can always override.
type Instrument struct {
	ID             string     `gorm:"primaryKey" json:"id"`
	Symbol         string     `gorm:"uniqueIndex" json:"symbol"`
	Name           string     `json:"name"`
	Market         string     `json:"market"`
	Currency       Currency   `gorm:"not null;default:TWD" json:"currency"`
	AssetType      string     `json:"asset_type"`
	LastPrice      *int64     `json:"last_price"`
	PriceUpdatedAt *time.Time `json:"price_updated_at"`
	QuoteCheckedAt *time.Time `json:"quote_checked_at"`
	// NewsCheckedAt and FundamentalsCheckedAt are when each was last asked for,
	// and exist for the same reason QuoteCheckedAt does: to skip a source that
	// was consulted a moment ago rather than hammering it on every press. They
	// are separate because the two answer on wildly different cadences — a
	// headline is stale in half an hour, a quarterly result in three months.
	//
	// Both are nil until the first successful collection, and a failed one
	// leaves them alone, so a source that is down is retried immediately rather
	// than suppressed.
	NewsCheckedAt         *time.Time `json:"news_checked_at"`
	FundamentalsCheckedAt *time.Time `json:"fundamentals_checked_at"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

// DailyClose is one instrument's closing price on one trading day.
//
// This is the only history in the system. Instrument.LastPrice answers "what is
// it worth now?"; these rows answer "what was it worth then?", which is what any
// figure spanning time — an equity curve, a drawdown, a benchmark comparison —
// has to consult. The two are kept apart rather than merged because they are
// maintained differently: the quote is refreshed against the provider's latest,
// while a close, once recorded, describes a session that is over and does not
// change.
//
// Date is the calendar day as YYYY-MM-DD, not a timestamp. A daily bar belongs
// to a session rather than to an instant, so carrying a time would invite
// comparisons that depend on the reader's zone; as a string it also sorts
// lexicographically into chronological order and compares exactly. It is the
// same way the ledger already names a day.
//
// Close is int64 minor units like every other price. The composite primary key
// is what makes a re-fetch idempotent: the same session fetched twice updates
// one row instead of accumulating duplicates that would each be counted.
type DailyClose struct {
	InstrumentID string    `gorm:"primaryKey" json:"instrument_id"`
	Date         string    `gorm:"primaryKey" json:"date"`
	Close        int64     `json:"close"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// DividendEvent is a distribution the provider says an instrument paid.
//
// It is **not** a ledger entry and must never become one on its own. The ledger
// holds what the user says happened to their money; this holds what the market
// says happened to the security, which is a different kind of fact with a
// different owner. Turning one into the other automatically would invent
// entries — the same thing the hindsight report refuses to do when it declines
// to model the dividends unsold shares would have received.
//
// What it is for is the prompt: the system can see that shares were held on an
// ex-date and that no dividend entry followed, and say so. Recording it stays
// the user's act.
//
// ExDate is the day the shares began trading without the right to the payout,
// which is what decides who is owed it — whoever held on that date is paid,
// typically weeks later. Matching against a holding therefore uses the ex-date
// while the ledger entry the user eventually writes is dated when the cash
// actually arrived, and the two are deliberately not the same day.
//
// Amount is per share in minor units. The composite key makes a refetch
// idempotent, exactly as DailyClose's does.
type DividendEvent struct {
	InstrumentID string    `gorm:"primaryKey" json:"instrument_id"`
	ExDate       string    `gorm:"primaryKey" json:"ex_date"`
	Amount       int64     `json:"amount"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// NewsItem is one headline about a listed company.
//
// Unlike everything else stored here it is not a fact about anybody's book: it
// is a pointer to somebody else's writing, kept only so a feed can be assembled
// without asking the provider again. It is shared master data on the same
// footing as an instrument's price — a headline about 2330 is objective, and
// scoping it per user would store the same article once per holder.
//
// ID is the provider's own identifier prefixed with the source
// ("cnyes:6591330"), because the sources number their articles independently and
// nothing promises those numbers will not collide.
//
// Language is stored rather than derived from the source, so that a reader can
// see which language a link leads to before opening it and so a second source in
// either language can be added without the field changing meaning.
type NewsItem struct {
	ID          string    `gorm:"primaryKey" json:"id"`
	Source      string    `json:"source"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary"`
	URL         string    `json:"url"`
	Publisher   string    `json:"publisher"`
	Language    string    `json:"language"`
	PublishedAt time.Time `gorm:"index" json:"published_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// NewsMention ties one article to one instrument it was tagged with.
//
// This is a join table rather than a copy of the article per instrument, which
// is a deliberate departure from how DailyClose keys its rows. Two things force
// it. An article genuinely names several companies — a single cnyes piece tagged
// 3374, 2330 and 6789 — so duplication would store the same title, summary and
// URL three times. And the feed has to render one row per article carrying all
// its tickers, which duplicated rows cannot do without grouping them back
// together on every read.
//
// A row exists only where the provider tagged the article with a code the book
// actually holds; see internal/news for why that tag is the only thing trusted
// to create one.
type NewsMention struct {
	NewsID       string    `gorm:"primaryKey" json:"news_id"`
	InstrumentID string    `gorm:"primaryKey;index" json:"instrument_id"`
	CreatedAt    time.Time `json:"created_at"`
}

// The metrics a FinancialFact can carry.
//
// Three, deliberately. Revenue says how much business the company did, net
// income how much of it it kept, and diluted EPS how much of that belonged to
// one share — which is the only one of the three directly comparable with the
// price already on file. Ratios built on them (margins, ROE, P/E) are derived at
// the UI edge if they are ever wanted, for the same reason average cost is: a
// stored ratio freezes a rounding decision into the database.
//
// They live here rather than in the provider package because they are what the
// rows are keyed by. The provider's names for the same things ("TotalRevenue")
// are its own vocabulary and are translated at that boundary.
const (
	MetricRevenue    = "revenue"
	MetricNetIncome  = "net_income"
	MetricDilutedEPS = "diluted_eps"
)

// The reporting cadences, as the provider stamps them. They are kept in the
// provider's own spelling because that is what arrives on every figure and what
// the rows are keyed by; renaming them here would mean two vocabularies for one
// column.
const (
	PeriodQuarter = "3M"
	PeriodYear    = "12M"
)

// FinancialFact is one number a company reported for one closed period.
//
// It is DailyClose's shape generalized, and for the same reason: a fiscal period
// that has ended does not change, so a refetch upserts onto the composite key
// instead of accumulating rows that would each be counted. Metric and PeriodType
// join the key because one instrument reports several figures on two cadences,
// and AsOfDate is the period end as YYYY-MM-DD for the reasons DailyClose.Date
// is a string.
//
// Currency is the currency the company *reported* in, which is not necessarily
// the one its shares trade in — an ADR is the ordinary case. This is the one
// place a currency differing from the instrument's is not a failure: unlike a
// quote or a cost basis, these figures are never added to anything, so the only
// thing owed them is a label. Storing what the provider said is what makes the
// label truthful.
type FinancialFact struct {
	InstrumentID string    `gorm:"primaryKey" json:"instrument_id"`
	Metric       string    `gorm:"primaryKey" json:"metric"`
	PeriodType   string    `gorm:"primaryKey" json:"period_type"`
	AsOfDate     string    `gorm:"primaryKey" json:"as_of_date"`
	Value        int64     `json:"value"`
	Currency     Currency  `json:"currency"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// TransactionSide is what an entry does to a position.
type TransactionSide string

const (
	// SideBuy adds shares and cost to a position.
	SideBuy TransactionSide = "buy"
	// SideSell removes shares, realizes profit or loss, and releases cost.
	SideSell TransactionSide = "sell"
	// SideDividend banks a cash payout without moving shares or cost.
	//
	// It is income, not a refund of what the shares cost: the payout is
	// realized in full and the cost basis is left alone. The alternative
	// convention — deducting a dividend from the cost basis — is the one US tax
	// treatment of a return of capital uses, and adopting it here would make an
	// ordinary Taiwanese cash dividend quietly inflate the paper gain on shares
	// still held instead of showing up as the income it is.
	//
	// Quantity is the shares the payout was made on and Price is the amount per
	// share, matching how a dividend is announced, so the total is arrived at
	// the same way as a trade's.
	SideDividend TransactionSide = "dividend"
)

// Sides are the entry kinds a ledger accepts.
var Sides = []TransactionSide{SideBuy, SideSell, SideDividend}

// Valid reports whether s is a known side.
func (s TransactionSide) Valid() bool {
	return s == SideBuy || s == SideSell || s == SideDividend
}

// Realizes reports whether an entry of this kind banks a result of its own, and
// therefore carries a RealizedPL stamp. A buy does not: it only moves cash into
// a cost basis it may sit in for years.
func (s TransactionSide) Realizes() bool {
	return s == SideSell || s == SideDividend
}

// Transaction is one entry in a user's ledger: a buy or sell that already
// happened. The ledger is the system of record — positions are derived from it
// and can always be rebuilt by replaying these rows in order.
//
// Symbol is a snapshot taken at entry time so that renaming an instrument never
// rewrites history. Price and Fee are int64 cents; Quantity is whole shares.
// NetAmount is computed by the server (never accepted from the client): the
// cash that left the account on a buy, or arrived on a sell, fees included.
//
// RealizedPL is what this one entry banked: the step the fold took when it was
// applied, which is proceeds minus the cost the sale released. It is a cache of
// the ledger like Position is, restamped whenever the position is replayed, and
// the whole point of storing it is that a fold's intermediate steps are
// otherwise lost — Position.RealizedPL keeps only the running total, which
// cannot answer "how did this year go?".
//
// It is a pointer because there are two distinct non-numbers here. A buy
// realizes nothing, which is not the same as banking zero, and a row predating
// the column has simply not been stamped yet. Both read as nil, and the report
// counts unstamped sells rather than summing them in as zero.
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
	RealizedPL   *int64          `json:"realized_pl"`
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

// FeeProfileKey names one brokerage arrangement. The set is a small fixed
// matrix rather than a free-form list because each key has to be derivable from
// a trade without asking: the market comes from the instrument, stock-vs-ETF
// from Instrument.AssetType, and only the recurring-purchase plan needs the user
// to say so on the form.
type FeeProfileKey string

const (
	// FeeTWStock is an ordinary Taiwanese share bought outright.
	FeeTWStock FeeProfileKey = "tw_stock"
	// FeeTWETF is a Taiwanese ETF, which pays a lower transaction tax on sale.
	FeeTWETF FeeProfileKey = "tw_etf"
	// FeeTWRecurring is a Taiwanese regular savings plan, usually charged at a
	// lower rate with a much lower floor.
	FeeTWRecurring FeeProfileKey = "tw_recurring"
	// FeeUSStock is a US share, typically bought through a sub-brokerage.
	FeeUSStock FeeProfileKey = "us_stock"
	// FeeUSETF is a US ETF.
	FeeUSETF FeeProfileKey = "us_etf"
	// FeeUSRecurring is a US regular savings plan.
	FeeUSRecurring FeeProfileKey = "us_recurring"
)

// FeeProfileKeys lists every key in the order settings are displayed.
var FeeProfileKeys = []FeeProfileKey{
	FeeTWStock, FeeTWETF, FeeTWRecurring,
	FeeUSStock, FeeUSETF, FeeUSRecurring,
}

// Valid reports whether k is a known fee profile.
func (k FeeProfileKey) Valid() bool {
	for _, known := range FeeProfileKeys {
		if k == known {
			return true
		}
	}
	return false
}

// MaxFeeRatePpm caps a configured rate at 10%.
//
// The ceiling catches a grossly misplaced decimal — 142500 where 1425 was
// meant — because that failure is otherwise silent: nothing downstream looks
// wrong, the cost basis simply drifts, and the book reports a worse year than
// it had. It deliberately does not try to catch a factor-of-ten slip. A sub-
// brokerage really can charge over 1%, so no ceiling can tell 1.425% from a
// mistyped 0.1425% without also refusing rates people genuinely pay. What it
// can do is refuse a number no brokerage on earth charges.
const MaxFeeRatePpm int64 = 100_000

// FeeProfile is one user's fee arrangement for one kind of trade.
//
// Rates are parts per million, not basis points. Every other rate in this
// system crosses the wire as integer basis points and for the same reasons —
// integers do not drift, and the rounding is decided in one place — but a basis
// point cannot express 0.1425%, which is the standard Taiwanese commission and
// the single most common rate this book will ever hold. Parts per million is
// the next scale down that keeps every rate in use an exact integer: 0.1425% is
// 1425, the 0.3% transaction tax is 3000.
//
// The discount multiplies the commission and nothing else. MinFee is a floor on
// what is actually paid, so it applies after the discount; SellTaxPpm is levied
// by the government and no broker can discount it.
//
// MinFee is int64 minor units, in the currency the profile's market trades in —
// which the key implies, since TWD and USD amounts are never added anywhere in
// this system. SellTaxPpm is charged on sales only; it is Taiwan's securities
// transaction tax, which is levied on the proceeds rather than by the broker,
// but lands in the same Transaction.Fee field because that field records what
// the trade cost in total.
//
// The composite primary key is what makes saving idempotent, the same way
// DailyClose's is: settings are written as a whole set and must update in place
// rather than accumulate a row per save.
//
// These rows are deliberately absent for a user who has never opened the
// settings page. DefaultFeeProfiles supplies the values in that case, merged at
// read time rather than written at registration — writing would freeze one
// day's defaults into every account created before they were revised, and it
// would also destroy the only signal that distinguishes a user who genuinely
// trades commission-free (a stored RatePpm of 0) from one who has never said.
type FeeProfile struct {
	UserID     string        `gorm:"primaryKey" json:"-"`
	Key        FeeProfileKey `gorm:"primaryKey" json:"key"`
	RatePpm    int64         `json:"rate_ppm"`
	MinFee     int64         `json:"min_fee"`
	SellTaxPpm int64         `json:"sell_tax_ppm"`
	// DiscountBps is the fraction of the list commission actually paid, in
	// basis points: 10000 is full price and 2800 is the Taiwanese "2.8 折".
	//
	// It is a separate field rather than folded into RatePpm because that is how
	// the charge is actually quoted — a broker advertises a discount off the
	// standard 0.1425%, not a rate of its own — and because the product often
	// is not expressible as a rate at all: 0.1425% at 3.3 折 is 0.047025%, which
	// is 470.25 parts per million. Keeping the two apart lets the multiplication
	// happen at full precision and the rounding happen once, on the money.
	//
	// Zero means no discount, not a free trade. A column added by AutoMigrate
	// leaves every existing row at zero, and reading that as "multiply by nought"
	// would silently make every past profile commission-free; a user who really
	// pays nothing sets RatePpm to 0 instead, which says so directly.
	DiscountBps int64 `json:"discount_bps"`
	// The timestamps stay out of the JSON. A profile the user has never saved is
	// synthesized from the defaults at read time and has no timestamps to report;
	// serializing the zero value would date it to the year 1.
	CreatedAt time.Time `json:"-"`
	UpdatedAt time.Time `json:"-"`
}

// DefaultFeeProfiles returns the starting rates for a user who has not set
// their own, in FeeProfileKeys order.
//
// They are ordinary Taiwanese retail terms: 0.1425% commission with a NT$20
// floor, plus a 0.3% securities transaction tax on sale which drops to 0.1% for
// an ETF. The US figures are sub-brokerage terms, where the tax does not apply.
// Every one of them is negotiable in practice, which is exactly why they are a
// per-user setting and not a constant.
func DefaultFeeProfiles() []FeeProfile {
	return []FeeProfile{
		{Key: FeeTWStock, RatePpm: 1425, MinFee: 2000, SellTaxPpm: 3000},
		{Key: FeeTWETF, RatePpm: 1425, MinFee: 2000, SellTaxPpm: 1000},
		{Key: FeeTWRecurring, RatePpm: 1000, MinFee: 100, SellTaxPpm: 3000},
		{Key: FeeUSStock, RatePpm: 1500, MinFee: 1500, SellTaxPpm: 0},
		{Key: FeeUSETF, RatePpm: 1500, MinFee: 1500, SellTaxPpm: 0},
		{Key: FeeUSRecurring, RatePpm: 1500, MinFee: 100, SellTaxPpm: 0},
	}
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
