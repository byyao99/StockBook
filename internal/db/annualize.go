package db

import (
	"math"
	"time"

	"stockbook/internal/models"
)

// minAnnualizedSpan is the shortest stretch of history an annual rate is
// reported for, everywhere one is: the book-wide and windowed money-weighted
// returns, a holding's own, and the time-weighted curve.
//
// Annualizing compounds a return over the rest of the year, so 20% made in a
// month prints as roughly 800% a year — arithmetically right, and a forecast
// nobody should read off a single month. A three-month-old book showed +49.6%
// on Holdings and −15% on Reports at the same time, which is what made this a
// rule rather than a per-page choice.
//
// 350 days rather than 365 so that a calendar-year window still qualifies: it
// is measured from its opening bound to its last session, which lands a few
// days short of a full year once a weekend or the New Year holiday falls at
// either end. Refusing those would withhold the one period an annual rate is
// literally the answer for, and extrapolating the remaining days moves the
// figure by a fraction of a percent.
const minAnnualizedSpan = 350 * 24 * time.Hour

// minAnnualizedDays is the same threshold in days, for the curve, which
// measures its span in calendar days between session dates.
var minAnnualizedDays = minAnnualizedSpan.Hours() / 24

// shortSpanReason is what Unavailable says when the rule above withholds a rate.
const shortSpanReason = "covers less than a year, so an annual rate would only extrapolate a short run"

// annualizedXIRR is models.XIRR behind the minimum-span rule: the rate in basis
// points, or nil and the reason there is none.
func annualizedXIRR(flows []models.CashFlow) (*int64, string) {
	if spanOf(flows) < minAnnualizedSpan {
		return nil, shortSpanReason
	}
	rate, err := models.XIRR(flows)
	if err != nil {
		return nil, err.Error()
	}
	bps := int64(math.Round(rate * 10000))
	return &bps, ""
}

// spanOf is the time between the earliest and latest flow.
func spanOf(flows []models.CashFlow) time.Duration {
	if len(flows) == 0 {
		return 0
	}
	first, last := flows[0].At, flows[0].At
	for _, f := range flows[1:] {
		if f.At.Before(first) {
			first = f.At
		}
		if f.At.After(last) {
			last = f.At
		}
	}
	return last.Sub(first)
}
