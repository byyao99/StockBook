package models

import (
	"errors"
	"math"
	"testing"
	"time"
)

// day returns noon UTC on a fixed date, matching how the trade form stores a
// chosen day.
func day(year int, month time.Month, d int) time.Time {
	return time.Date(year, month, d, 12, 0, 0, 0, time.UTC)
}

// rateTolerance is a hundredth of a basis point: finer than anything the API
// reports, far coarser than the search converges to.
const rateTolerance = 1e-6

// The spans below avoid leap years on purpose. XIRR is actual/365, so 2024 is
// 366/365 of a year and a "two year" span containing it is not exactly 2 —
// pinning an exact rate against one would be testing the calendar, not the math.
func TestXIRRSolvesKnownRates(t *testing.T) {
	cases := []struct {
		name  string
		flows []CashFlow
		want  float64
	}{
		{
			// Doubling in exactly one year is 100% a year, whatever the amounts.
			name: "doubles in a year",
			flows: []CashFlow{
				{At: day(2025, time.January, 1), Amount: -1_000_000},
				{At: day(2026, time.January, 1), Amount: 2_000_000},
			},
			want: 1.0,
		},
		{
			name: "halves in a year",
			flows: []CashFlow{
				{At: day(2025, time.January, 1), Amount: -1_000_000},
				{At: day(2026, time.January, 1), Amount: 500_000},
			},
			want: -0.5,
		},
		{
			// Doubling in two years is not 50% a year — compounding makes it
			// sqrt(2)-1. A gain-over-cost percentage cannot see this, which is
			// the whole reason this function exists.
			name: "doubles in two years",
			flows: []CashFlow{
				{At: day(2025, time.January, 1), Amount: -1_000_000},
				{At: day(2027, time.January, 1), Amount: 2_000_000},
			},
			want: math.Sqrt2 - 1,
		},
		{
			// Breaking even is zero a year, and must not be mistaken for a
			// missing answer at the boundary between a gain and a loss.
			name: "breaks even",
			flows: []CashFlow{
				{At: day(2025, time.January, 1), Amount: -1_000_000},
				{At: day(2026, time.January, 1), Amount: 1_000_000},
			},
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := XIRR(tc.flows)
			if err != nil {
				t.Fatalf("XIRR: %v", err)
			}
			if math.Abs(got-tc.want) > rateTolerance {
				t.Errorf("got %.8f, want %.8f", got, tc.want)
			}
		})
	}
}

// The point of a money-weighted return: two books with the same total gain over
// the same span rank differently when one had more money at work for longer.
// A (value-cost)/cost percentage reports both as +20% and cannot tell them apart.
func TestXIRRWeightsMoneyByHowLongItWorked(t *testing.T) {
	// Both pay in 200,000 in total and end with 240,000 on the same day.
	early := []CashFlow{
		{At: day(2024, time.January, 1), Amount: -100_000},
		{At: day(2024, time.February, 1), Amount: -100_000},
		{At: day(2026, time.January, 1), Amount: 240_000},
	}
	late := []CashFlow{
		{At: day(2024, time.January, 1), Amount: -100_000},
		{At: day(2025, time.December, 1), Amount: -100_000},
		{At: day(2026, time.January, 1), Amount: 240_000},
	}

	earlyRate, err := XIRR(early)
	if err != nil {
		t.Fatalf("early: %v", err)
	}
	lateRate, err := XIRR(late)
	if err != nil {
		t.Fatalf("late: %v", err)
	}
	// The second book earned the same 40,000 while exposing its second 100,000
	// for one month instead of two years, so its money worked harder.
	if lateRate <= earlyRate {
		t.Errorf("late %.4f should beat early %.4f: the same gain on money at "+
			"work for less time is a higher rate", lateRate, earlyRate)
	}
}

// A dividend is a payment in like any other, so a share whose price never moves
// still earns a return.
func TestXIRRCountsIncomeOnAnUnsoldHolding(t *testing.T) {
	flows := []CashFlow{
		{At: day(2025, time.January, 1), Amount: -1_000_000}, // buy
		{At: day(2025, time.July, 1), Amount: 50_000},        // dividend
		{At: day(2026, time.January, 1), Amount: 1_000_000},  // still worth what it cost
	}
	rate, err := XIRR(flows)
	if err != nil {
		t.Fatalf("XIRR: %v", err)
	}
	if rate <= 0 {
		t.Errorf("got %.6f, want a positive rate: the price went nowhere but the "+
			"holding paid 5%%", rate)
	}
}

// Flow order is an accident of how the ledger was queried and must not change
// the answer.
func TestXIRRIgnoresFlowOrder(t *testing.T) {
	forward := []CashFlow{
		{At: day(2024, time.March, 1), Amount: -500_000},
		{At: day(2024, time.September, 15), Amount: -300_000},
		{At: day(2025, time.June, 30), Amount: 400_000},
		{At: day(2026, time.August, 1), Amount: 600_000},
	}
	reversed := make([]CashFlow, len(forward))
	for i, f := range forward {
		reversed[len(forward)-1-i] = f
	}

	a, err := XIRR(forward)
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	b, err := XIRR(reversed)
	if err != nil {
		t.Fatalf("reversed: %v", err)
	}
	if math.Abs(a-b) > rateTolerance {
		t.Errorf("order changed the rate: %.8f vs %.8f", a, b)
	}
}

// Every refusal reports why rather than returning a zero rate, because "cannot
// be measured" and "broke even" are different answers and one of them is false.
func TestXIRRRefusesWhatItCannotMeasure(t *testing.T) {
	cases := []struct {
		name  string
		flows []CashFlow
		want  error
	}{
		{
			name:  "nothing at all",
			flows: nil,
			want:  ErrOneDirection,
		},
		{
			// A book of nothing but unsold holdings with no quote looks like this.
			name: "money only ever went out",
			flows: []CashFlow{
				{At: day(2025, time.January, 1), Amount: -1_000_000},
				{At: day(2025, time.June, 1), Amount: -500_000},
			},
			want: ErrOneDirection,
		},
		{
			name: "bought and sold in the same instant",
			flows: []CashFlow{
				{At: day(2025, time.January, 1), Amount: -1_000_000},
				{At: day(2025, time.January, 1), Amount: 1_100_000},
			},
			want: ErrSameInstant,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rate, err := XIRR(tc.flows)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got error %v, want %v", err, tc.want)
			}
			if rate != 0 {
				t.Errorf("got rate %v alongside an error, want 0", rate)
			}
		})
	}
}
