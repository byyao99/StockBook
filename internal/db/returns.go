package db

import (
	"math"
	"sort"
	"time"

	"stockbook/internal/models"
)

// ReturnsSummary is one currency's money-weighted rate of return, measured over
// the whole ledger, with the figures it was computed from.
//
// Like every other total in this system it is per currency: there is no FX rate
// here, so a TWD book and a USD book are two separate questions.
//
// It answers one of two questions, and which one depends on whether a period
// was asked for.
//
// **Since inception** is the default: every flow the ledger has ever carried,
// closed by the market value of what is still held. It needs no price history,
// only a current quote for each open holding.
//
// **Over a window** needs the book's market value at both ends, which is a
// different and heavier requirement: the opening value is a payment *out* — the
// position the period was entered holding — and the closing value a payment in.
// This was refused for a long time and the reason is worth recording, because it
// has since expired: the system stored only each instrument's current quote, so
// the value of the book on a past date was not recoverable. models.DailyClose
// changed that, and priceLedger is the same valuation the equity curve runs on.
// A windowed report therefore inherits the curve's rule rather than the live
// quote one — an instrument whose stored history does not reach the day it was
// first traded is dropped whole and counted in WithoutHistory.
//
// XIRRBps is nil when no rate could be computed, with Unavailable carrying the
// reason. It is not zero: a book that cannot be measured has not broken even.
type ReturnsSummary struct {
	Currency models.Currency `json:"currency"`

	// XIRRBps is the annualized money-weighted rate in basis points: 1234 means
	// 12.34% a year. Basis points rather than a float for the reason every
	// amount here is minor units — an exact integer over the wire, with the
	// rounding decided once, on the server, instead of drifting through
	// whatever each client's formatter does with 0.12340000000000001.
	XIRRBps *int64 `json:"xirr_bps"`
	// Unavailable explains an absent rate in the user's own terms, and is empty
	// whenever XIRRBps is set.
	Unavailable string `json:"unavailable,omitempty"`

	Invested    int64 `json:"invested"`     // total paid out on buys, fees included
	Returned    int64 `json:"returned"`     // total received from sales and dividends
	EndingValue int64 `json:"ending_value"` // market value of the open holdings counted here
	NetGain     int64 `json:"net_gain"`     // Returned + EndingValue - Invested

	// FirstFlowAt is the date of the earliest entry counted, so a caller can say
	// what the rate is an average *over*. Nil when nothing was counted.
	FirstFlowAt *time.Time `json:"first_flow_at"`
	// AsOf is the moment the ending value was taken at, which is the far end of
	// the period the rate covers.
	AsOf time.Time `json:"as_of"`

	// OpenPositions and PricedPositions report how much of the book the rate
	// accounts for, on the same terms as CurrencySummary. An open holding with
	// no quote is left out of the calculation *entirely* — see ReturnsReport.
	// Both are zero on a windowed report, where a live quote decides nothing.
	OpenPositions   int `json:"open_positions"`
	PricedPositions int `json:"priced_positions"`

	// From and To bound a windowed report, and are empty on a since-inception
	// one. A caller cannot tell the two apart from the figures alone — both
	// report a rate and an ending value — and they answer different questions,
	// so the response says which it is rather than leaving it to be inferred
	// from what was requested.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`

	// OpeningValue is what the book was worth entering a windowed period, which
	// is the flow that makes the rate a period rate rather than a lifetime one.
	// Zero on a since-inception report, where there is no position to open with.
	OpeningValue int64 `json:"opening_value"`

	// WithoutHistory counts instruments dropped from a windowed report for want
	// of stored prices reaching back to when they were first traded — the same
	// rule, and the same count, the equity curve reports.
	WithoutHistory int `json:"without_history"`
}

// returnsFlowRow is the flat scan target for the ledger side of the report.
type returnsFlowRow struct {
	InstrumentID string
	Currency     models.Currency
	Side         models.TransactionSide
	NetAmount    int64
	TradedAt     time.Time
}

// ReturnsReport computes userID's money-weighted return, one entry per currency,
// ordered by currency so the output is stable. asOf is when the open holdings
// are valued — normally now, and a parameter so the arithmetic is testable.
//
// The cash flows are the ledger itself, seen from the user's pocket: a buy is
// money out, a sale or a dividend is money in, and what is still held is a final
// payment in at asOf, because a return has to account for the shares not yet
// sold or the whole book would look like a total loss.
//
// **An open holding with no quote takes its entire history out of the report.**
// Its market value is unknown, and the two alternatives are both worse than
// omitting it: valuing it at zero would show a holding as a wipeout, and keeping
// its purchases while dropping only its ending value would report exactly the
// same wipeout with no sign that anything was missing. Leaving the instrument
// out answers a smaller question truthfully — the return on the part of the book
// that can be valued — and PricedPositions against OpenPositions is what tells
// the caller how much that is. This is the same rule PortfolioSummary follows
// for its valuation totals, applied to a figure that spans history rather than a
// moment. Closed holdings need no quote and are always counted: their cash flows
// are complete and their ending value is genuinely zero.
func (d *DB) ReturnsReport(userID string, asOf time.Time) ([]ReturnsSummary, error) {
	holdings := []positionRow{}
	if err := d.positionQuery(userID, true).Select(positionSelect).Scan(&holdings).Error; err != nil {
		return nil, err
	}

	flows := []returnsFlowRow{}
	err := d.db.Model(&models.Transaction{}).
		Joins("JOIN instruments ON instruments.id = transactions.instrument_id").
		Where("transactions.user_id = ?", userID).
		Select(`transactions.instrument_id AS instrument_id,
			transactions.side AS side,
			transactions.net_amount AS net_amount,
			transactions.traded_at AS traded_at,
			instruments.currency AS currency`).
		Scan(&flows).Error
	if err != nil {
		return nil, err
	}

	type accumulator struct {
		summary *ReturnsSummary
		flows   []models.CashFlow
	}
	byCurrency := map[models.Currency]*accumulator{}
	acc := func(currency models.Currency) *accumulator {
		a, ok := byCurrency[currency]
		if !ok {
			a = &accumulator{summary: &ReturnsSummary{Currency: currency, AsOf: asOf}}
			byCurrency[currency] = a
		}
		return a
	}

	// Which instruments cannot be valued, and so are dropped whole. The bucket
	// is still created for their currency, so a book that is entirely unpriced
	// reports why rather than vanishing from the response.
	unvalued := map[string]bool{}
	for _, h := range holdings {
		a := acc(h.Currency)
		if h.Quantity == 0 {
			continue
		}
		a.summary.OpenPositions++
		if h.LastPrice == nil {
			unvalued[h.InstrumentID] = true
			continue
		}
		a.summary.PricedPositions++
		a.summary.EndingValue += models.Gross(h.Quantity, *h.LastPrice)
	}

	for _, f := range flows {
		if unvalued[f.InstrumentID] {
			continue
		}
		a := acc(f.Currency)
		amount := f.NetAmount
		if f.Side == models.SideBuy {
			a.summary.Invested += amount
			amount = -amount
		} else {
			a.summary.Returned += amount
		}
		a.flows = append(a.flows, models.CashFlow{At: f.TradedAt, Amount: amount})
		if a.summary.FirstFlowAt == nil || f.TradedAt.Before(*a.summary.FirstFlowAt) {
			tradedAt := f.TradedAt
			a.summary.FirstFlowAt = &tradedAt
		}
	}

	summaries := make([]ReturnsSummary, 0, len(byCurrency))
	for _, a := range byCurrency {
		s := a.summary
		s.NetGain = s.Returned + s.EndingValue - s.Invested

		// What is still held is a payment in, dated at the moment it was valued.
		// Without it every unsold share would read as money that never came back.
		if s.EndingValue > 0 {
			a.flows = append(a.flows, models.CashFlow{At: asOf, Amount: s.EndingValue})
		}

		if len(a.flows) == 0 {
			// Only reachable when every holding in the currency was dropped for
			// want of a quote: a bucket exists only where there is a holding.
			s.Unavailable = "no holding in this currency has a quote, so there is nothing to measure a return over"
		} else if rate, err := models.XIRR(a.flows); err != nil {
			s.Unavailable = err.Error()
		} else {
			bps := int64(math.Round(rate * 10000))
			s.XIRRBps = &bps
		}
		summaries = append(summaries, *s)
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Currency < summaries[j].Currency
	})
	return summaries, nil
}

// ReturnsBetween computes userID's money-weighted return over [from, to], one
// entry per currency. Both bounds are YYYY-MM-DD; an empty `to` means today.
//
// The window is what makes this a different question from ReturnsReport, not a
// filtered version of it. A period rate has to account for the position the
// period was *entered* holding, so the book's market value at `from` enters the
// flows as money out — as though the whole portfolio had been bought that
// morning — and its value at `to` as money in. Between them sit only the trades
// actually made in the window. Without the opening flow, a book held unchanged
// through a flat year would report no flows at all and no rate; with it, it
// correctly reports roughly zero.
//
// Both ends are valued from stored closes rather than from live quotes, even
// when `to` is today. Mixing the two would measure the closing end against a
// price the opening end had no equivalent of, and the curve already answers
// "what was it worth then" this way.
//
// A trade dated exactly on `from` is inside the opening value, not a flow: it is
// folded by valueOn like any earlier entry. Counting it as both would charge the
// period twice for the same purchase.
func (d *DB) ReturnsBetween(userID, from, to string) ([]ReturnsSummary, error) {
	txs := []curveTx{}
	err := d.db.Model(&models.Transaction{}).
		Joins("JOIN instruments ON instruments.id = transactions.instrument_id").
		Where("transactions.user_id = ?", userID).
		Select(`transactions.instrument_id AS instrument_id,
			transactions.side AS side,
			transactions.quantity AS quantity,
			transactions.price AS price,
			transactions.fee AS fee,
			transactions.net_amount AS net_amount,
			transactions.traded_at AS traded_at,
			instruments.currency AS currency`).
		Order(qualifiedLedgerOrder).Scan(&txs).Error
	if err != nil {
		return nil, err
	}
	if len(txs) == 0 {
		return []ReturnsSummary{}, nil
	}
	if to == "" {
		to = time.Now().UTC().Format(time.DateOnly)
	}

	byCurrency := map[models.Currency][]curveTx{}
	for _, tx := range txs {
		byCurrency[tx.Currency] = append(byCurrency[tx.Currency], tx)
	}

	summaries := make([]ReturnsSummary, 0, len(byCurrency))
	for currency, ledger := range byCurrency {
		summary, err := d.currencyReturnsBetween(currency, ledger, from, to)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Currency < summaries[j].Currency
	})
	return summaries, nil
}

// currencyReturnsBetween measures one currency's book over the window.
func (d *DB) currencyReturnsBetween(currency models.Currency, ledger []curveTx, from, to string) (ReturnsSummary, error) {
	summary := ReturnsSummary{Currency: currency, From: from, To: to}

	priced, err := d.priceLedger(ledger, to)
	if err != nil {
		return summary, err
	}
	summary.WithoutHistory = priced.withoutHistory
	if len(priced.series) == 0 {
		summary.Unavailable = "no holding in this currency has stored price history covering when it was held; sync prices first"
		return summary, nil
	}

	// An open `from` means "since the book began", which needs no opening
	// position: the first purchase is the first flow, exactly as it is for the
	// since-inception report.
	flows := []models.CashFlow{}
	if from != "" {
		opening, err := priced.valueOn(from)
		if err != nil {
			return summary, err
		}
		summary.OpeningValue = opening
		if opening > 0 {
			flows = append(flows, models.CashFlow{At: parseDay(from), Amount: -opening})
		}
	}

	for _, tx := range ledger {
		day := tx.TradedAt.UTC().Format(time.DateOnly)
		// Entries at or before the opening are already inside OpeningValue, and
		// entries past the close belong to a period this report does not cover.
		if (from != "" && day <= from) || day > to {
			continue
		}
		if _, ok := priced.series[tx.InstrumentID]; !ok {
			continue // an excluded instrument's trades leave with it
		}
		amount := tx.NetAmount
		if tx.Side == models.SideBuy {
			summary.Invested += amount
			amount = -amount
		} else {
			summary.Returned += amount
		}
		flows = append(flows, models.CashFlow{At: tx.TradedAt, Amount: amount})
		if summary.FirstFlowAt == nil || tx.TradedAt.Before(*summary.FirstFlowAt) {
			tradedAt := tx.TradedAt
			summary.FirstFlowAt = &tradedAt
		}
	}

	// The closing figure is dated at the last session it could actually be
	// struck on, not at the far bound that was asked for. Asking for "this year"
	// in September must not price the book in December: the same gain spread
	// over a longer assumed period reports a lower annual rate, and nothing on
	// screen would say why.
	closeOn := to
	if last, ok := priced.lastSessionOn(to); ok {
		closeOn = last
	}
	closing, err := priced.valueOn(closeOn)
	if err != nil {
		return summary, err
	}
	summary.EndingValue = closing
	summary.AsOf = parseDay(closeOn)
	summary.NetGain = summary.Returned + closing - summary.Invested - summary.OpeningValue
	if closing > 0 {
		flows = append(flows, models.CashFlow{At: summary.AsOf, Amount: closing})
	}

	if len(flows) == 0 {
		summary.Unavailable = "nothing was held or traded in this period, so there is no return to measure"
		return summary, nil
	}
	if rate, err := models.XIRR(flows); err != nil {
		summary.Unavailable = err.Error()
	} else {
		bps := int64(math.Round(rate * 10000))
		summary.XIRRBps = &bps
	}
	return summary, nil
}

// parseDay reads a YYYY-MM-DD bound as an instant for the flow list. The hour is
// arbitrary and shared by both ends, so it cancels out of an actual/365 rate;
// what matters is that a bound and a trade on the same day do not sort apart.
func parseDay(date string) time.Time {
	parsed, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}
