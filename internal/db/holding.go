package db

import (
	"math"
	"time"

	"stockbook/internal/models"
)

// trailingDividendWindow is how far back "the last twelve months" reaches.
//
// A year of actual days rather than a calendar year, so the figure does not
// jump on 1 January and does not depend on which month a reader happens to
// open the page in. 365 rather than 366 matches models.XIRR's day count, which
// is the other annualized figure on the same screen.
const trailingDividendWindow = 365 * 24 * time.Hour

// minAnnualizedSpan is the shortest holding period an annual rate is reported
// for. Annualizing compounds the period's return over the rest of the year, so
// 20% made in a month prints as roughly 800% a year — arithmetically right, and
// a forecast nobody should read off a single month. Below a year the figure is
// withheld rather than shown, and Unavailable says why. Measured on the same
// actual-365 days XIRR annualizes over, so exactly a year qualifies.
const minAnnualizedSpan = 365 * 24 * time.Hour

// HoldingDetail is everything one holding's own page needs: the position, what
// it has banked over its whole life, and how fast the money in it has grown.
//
// It is deliberately **lifetime**, with no period bounds, which is the one place
// this differs from the reports on /reports. Those answer "how did this period
// go?" across a book; this answers "how has this holding done?", and a question
// about one position is not improved by hiding the half of its history that
// falls outside a window — the average cost every figure here is built on was
// formed by trades the window would have excluded.
//
// Invested, Proceeds and Dividends are cash as it moved, fees included, so
// Proceeds + Dividends - Invested is what the holding has returned so far in
// cash. TradingPL is what the sales banked against the average cost they
// released, which is a different question and the one the realized report
// answers; the two differ by whatever is still held.
//
// RealizedPL is the position's own running total rather than a sum of stamps.
// By invariant 6 those agree, and UnstampedEntries is what says whether the
// decomposition beside it is complete.
type HoldingDetail struct {
	Position PositionView `json:"position"`
	AsOf     time.Time    `json:"as_of"`

	FirstTradedAt *time.Time `json:"first_traded_at"`
	LastTradedAt  *time.Time `json:"last_traded_at"`

	Buys             int `json:"buys"`
	Sells            int `json:"sells"`
	DividendCount    int `json:"dividend_count"`
	UnstampedEntries int `json:"unstamped_entries"`

	Invested  int64 `json:"invested"`
	Proceeds  int64 `json:"proceeds"`
	Dividends int64 `json:"dividends"`

	RealizedPL int64 `json:"realized_pl"`
	TradingPL  int64 `json:"trading_pl"`

	// TrailingDividends is what this holding paid out over the last year, and
	// YieldOnCostBps is that against what the shares still held cost.
	//
	// Against **cost**, not against market value, because that is the question a
	// long-term holder is actually asking: what the money they committed is now
	// paying them, which rises as a position matures and is the whole point of
	// buying an income holding early. The market-value yield is the one every
	// quote page already shows, and it says something about the price rather
	// than about this book.
	//
	// A trailing figure is used rather than a forecast because this system does
	// not predict distributions; what it has is what was banked. That makes the
	// number understate a holding bought part-way through the year, which is
	// honest in the direction that matters and is why it is labelled by its
	// window.
	//
	// The yield is nil, never zero, once the shares are gone: there is no cost
	// left to divide by, and 0% would read as an income holding that stopped
	// paying. A trailing total of zero against a real cost basis is a genuine
	// zero and reported as one.
	TrailingDividends int64  `json:"trailing_dividends"`
	YieldOnCostBps    *int64 `json:"yield_on_cost_bps"`

	// XIRRBps is the money-weighted return on this holding alone, measured the
	// same way and with the same convention as the book-wide figure on
	// /reports/returns: the ledger's own flows, closed by the market value of
	// whatever is still held. It is nil for a holding held less than
	// minAnnualizedSpan. Unavailable says in words why there is none.
	XIRRBps     *int64 `json:"xirr_bps"`
	Unavailable string `json:"unavailable,omitempty"`
}

// HoldingDetail assembles one of userID's holdings, or ErrNotFound when they
// have never traded the instrument. asOf is when open shares are valued —
// normally now, and a parameter so the arithmetic is testable against fixed
// dates, exactly as ReturnsReport takes one.
func (d *DB) HoldingDetail(userID, instrumentID string, asOf time.Time) (HoldingDetail, error) {
	position, err := d.GetPosition(userID, instrumentID)
	if err != nil {
		return HoldingDetail{}, err
	}

	entries := []models.Transaction{}
	err = d.db.Where("user_id = ? AND instrument_id = ?", userID, instrumentID).
		Order(ledgerOrder).Find(&entries).Error
	if err != nil {
		return HoldingDetail{}, err
	}

	detail := HoldingDetail{
		Position:   position,
		AsOf:       asOf,
		RealizedPL: position.RealizedPL,
	}
	if len(entries) == 0 {
		// A position with no ledger behind it should not exist, since the rows
		// are a cache of the fold. Reporting the position alone is truthful and
		// leaves the derived figures unset rather than inventing them.
		detail.Unavailable = "this holding has no ledger entries"
		return detail, nil
	}

	first, last := entries[0].TradedAt, entries[len(entries)-1].TradedAt
	detail.FirstTradedAt, detail.LastTradedAt = &first, &last

	flows := make([]models.CashFlow, 0, len(entries)+1)
	since := asOf.Add(-trailingDividendWindow)
	for _, e := range entries {
		amount := e.NetAmount
		switch e.Side {
		case models.SideBuy:
			detail.Buys++
			detail.Invested += e.NetAmount
			amount = -amount
		case models.SideSell:
			detail.Sells++
			detail.Proceeds += e.NetAmount
		case models.SideDividend:
			detail.DividendCount++
			if e.TradedAt.After(since) {
				detail.TrailingDividends += e.NetAmount
			}
		}
		flows = append(flows, models.CashFlow{At: e.TradedAt, Amount: amount})

		if !e.Side.Realizes() {
			continue
		}
		if e.RealizedPL == nil {
			detail.UnstampedEntries++
			continue
		}
		if e.Side == models.SideDividend {
			detail.Dividends += *e.RealizedPL
		} else {
			detail.TradingPL += *e.RealizedPL
		}
	}

	if position.Quantity > 0 && position.CostBasis > 0 {
		yield := int64(math.Round(float64(detail.TrailingDividends) / float64(position.CostBasis) * 10000))
		detail.YieldOnCostBps = &yield
	}

	// The holding period runs to asOf while shares are still held, and to the
	// last entry once they are gone: a closed holding stopped having money at
	// work then.
	end := asOf
	if position.Quantity == 0 {
		end = last
	}
	if end.Sub(first) < minAnnualizedSpan {
		detail.Unavailable = "held for less than a year, so an annual rate would only extrapolate a short run"
		return detail, nil
	}

	// What is still held is a payment in at asOf, for the reason the book-wide
	// report adds one: without it every unsold share measures as money that
	// never came back. An open holding with no quote takes the whole figure out
	// rather than being valued at zero — the same rule, for the same reason.
	switch {
	case position.Quantity == 0:
		// Closed: the flows are complete and the ending value is genuinely zero.
	case position.MarketValue == nil:
		detail.Unavailable = "this holding has no quote, so the shares still held cannot be valued"
		return detail, nil
	default:
		flows = append(flows, models.CashFlow{At: asOf, Amount: *position.MarketValue})
	}

	if rate, err := models.XIRR(flows); err != nil {
		detail.Unavailable = err.Error()
	} else {
		bps := int64(math.Round(rate * 10000))
		detail.XIRRBps = &bps
	}
	return detail, nil
}
