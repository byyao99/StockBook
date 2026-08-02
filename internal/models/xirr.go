package models

import (
	"errors"
	"math"
	"time"
)

// Reasons a set of cash flows has no money-weighted return. They are returned
// rather than a zero rate because "we cannot tell" and "you made nothing" are
// different answers, and the second one is a lie in every one of these cases.
var (
	// ErrOneDirection is returned when money only ever moved one way. A rate of
	// return is the rate at which what went out grew into what came back; with
	// nothing on one side of that there is no growth to solve for. A book of
	// nothing but unsold, unpriced holdings looks exactly like this.
	ErrOneDirection = errors.New("a return needs both money paid in and money received back")

	// ErrSameInstant is returned when every flow falls on the same moment. An
	// annualized rate divides a gain by the time it took, and that time is zero.
	ErrSameInstant = errors.New("every cash flow falls at the same moment, so there is no period to annualize over")

	// ErrNoRate is returned when no rate in the searched range zeroes the net
	// present value. In practice this means a loss deeper than searchFloor —
	// close to total — over a long period.
	ErrNoRate = errors.New("no annual rate of return fits these cash flows")
)

// daysPerYear is the actual/365 convention every published XIRR uses,
// spreadsheets included. A leap year is therefore 366/365 of a year. Matching
// the convention beats being astronomically right: a number the user can
// reconcile against a broker statement is worth more than one that is 0.03%
// more defensible and agrees with nothing.
const daysPerYear = 365.0

// The bracket the root is searched in, as annual rates.
//
// searchFloor stops just short of -100% because the discount factor
// (1+rate)^years collapses to zero there and the present value diverges. A book
// that genuinely lost more than 99.99% a year has no number worth printing.
// searchCeiling is absurdly high on purpose: it costs one extra halving per
// factor of two, and a day-trader's first week can honestly annualize into the
// thousands of percent.
const (
	searchFloor   = -0.9999
	searchCeiling = 1000.0

	// bisectionSteps and rateEpsilon bound the search. Halving a bracket of
	// 1000 sixty times already takes it below 1e-15, so the epsilon is what
	// normally ends the loop and the step count is the backstop.
	bisectionSteps = 200
	rateEpsilon    = 1e-12
)

// CashFlow is one dated movement of money across the boundary of the caller's
// pocket: negative when they paid, positive when they were paid. Amount is
// integer minor units like every other monetary value in this system.
type CashFlow struct {
	At     time.Time
	Amount int64
}

// XIRR returns the annualized money-weighted rate of return implied by flows —
// the constant annual rate at which every payment in, discounted from its own
// date, exactly cancels every payment out. 0.1234 means 12.34% a year.
//
// This is the number a simple (value-cost)/cost percentage cannot give. That
// one is blind to time and to size: doubling 10,000 over five years and
// doubling 1,000,000 over one both read as "+100%", and a well-timed second
// purchase counts for no more than the first. XIRR weights every dollar by how
// long it was actually at work, which is what makes it comparable between one
// book and another, or against a savings rate.
//
// The rate is found by bisection rather than Newton-Raphson. Newton converges in
// a handful of iterations on friendly input and diverges on the input that
// actually turns up here — a ledger with flows in both directions gives the
// present value more than one turning point, and a derivative step from a bad
// guess lands outside the domain. Bisection cannot diverge, needs no derivative
// and no starting guess, and the whole search is a few hundred passes over a
// list computed once per page load. Robustness is worth more than the
// microseconds.
//
// XIRR is pure and does not depend on the order of flows.
func XIRR(flows []CashFlow) (float64, error) {
	var paid, received int64
	var first, last time.Time
	for i, f := range flows {
		if f.Amount < 0 {
			paid -= f.Amount
		} else {
			received += f.Amount
		}
		if i == 0 || f.At.Before(first) {
			first = f.At
		}
		if i == 0 || f.At.After(last) {
			last = f.At
		}
	}
	if paid == 0 || received == 0 {
		return 0, ErrOneDirection
	}
	if !last.After(first) {
		return 0, ErrSameInstant
	}

	// Discounting is relative to the earliest flow, so the exponents stay small
	// and the first payment is undiscounted. The choice of anchor cannot change
	// the root: moving it multiplies the whole present value by a constant.
	years := make([]float64, len(flows))
	amounts := make([]float64, len(flows))
	for i, f := range flows {
		years[i] = f.At.Sub(first).Hours() / 24 / daysPerYear
		amounts[i] = float64(f.Amount)
	}

	presentValue := func(rate float64) float64 {
		total := 0.0
		for i := range amounts {
			total += amounts[i] / math.Pow(1+rate, years[i])
		}
		return total
	}

	low, high := searchFloor, searchCeiling
	lowValue, highValue := presentValue(low), presentValue(high)
	if lowValue == 0 {
		return low, nil
	}
	if highValue == 0 {
		return high, nil
	}
	// No sign change means no root inside the bracket. Reporting that is better
	// than returning whichever end came closest, which would look like an answer.
	if math.IsNaN(lowValue) || math.IsNaN(highValue) || (lowValue > 0) == (highValue > 0) {
		return 0, ErrNoRate
	}

	lowIsPositive := lowValue > 0
	for i := 0; i < bisectionSteps && high-low > rateEpsilon; i++ {
		mid := low + (high-low)/2
		if (presentValue(mid) > 0) == lowIsPositive {
			low = mid
		} else {
			high = mid
		}
	}
	return low + (high-low)/2, nil
}
