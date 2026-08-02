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
// The period is always since inception. A return over a *window* needs the
// portfolio's market value at the opening of that window, and this system keeps
// only the current quote for each instrument — no price history — so the value
// of the book on any past date is not recoverable. Accepting from/to here would
// mean answering a different question than the one asked.
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
	OpenPositions   int `json:"open_positions"`
	PricedPositions int `json:"priced_positions"`
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
		a.summary.EndingValue += int64(h.Quantity) * *h.LastPrice
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
