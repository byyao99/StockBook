package db

import (
	"errors"
	"testing"
	"time"

	"stockbook/internal/models"
)

// asOfDay is when the open shares are valued, expressed on the same fixed base
// the ledger helpers use so the XIRR has a fixed span to annualize over.
func asOfDay(n int) time.Time { return day(n) }

// The headline decomposition: what the holding banked on the way out, what it
// banked for holding, and what the cash actually did — all three over the
// holding's whole life rather than over a period.
func TestHoldingDetailDecomposesWhatWasBanked(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "holder")
	inst := seedInstrument(t, s, "2330")
	priceAt(t, s, inst.ID, 60_000)

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	// A payout on the full holding, then half the shares sold at a profit.
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideDividend, 1000, 400, 0, 30})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 500, 55_000, 0, 60})

	got, err := s.HoldingDetail(user.ID, inst.ID, asOfDay(90))
	if err != nil {
		t.Fatalf("HoldingDetail: %v", err)
	}

	if got.Buys != 1 || got.Sells != 1 || got.DividendCount != 1 {
		t.Errorf("counts buys/sells/dividends = %d/%d/%d, want 1/1/1",
			got.Buys, got.Sells, got.DividendCount)
	}
	if got.UnstampedEntries != 0 {
		t.Errorf("unstamped %d, want 0", got.UnstampedEntries)
	}
	// 1000 shares at NT$500 each.
	if got.Invested != 500*1000*100 {
		t.Errorf("invested %d, want 50000000", got.Invested)
	}
	// 500 shares at NT$550.
	if got.Proceeds != 550*500*100 {
		t.Errorf("proceeds %d, want 27500000", got.Proceeds)
	}
	// 1000 shares at NT$4.
	if got.Dividends != 400_000 {
		t.Errorf("dividends %d, want 400000", got.Dividends)
	}
	// Half the shares released half the cost: 27,500,000 - 25,000,000.
	if got.TradingPL != 2_500_000 {
		t.Errorf("trading PL %d, want 2500000", got.TradingPL)
	}
	// Invariant 6: the stamps decompose the position's running total.
	if got.RealizedPL != got.TradingPL+got.Dividends {
		t.Errorf("realized %d != trading %d + dividends %d",
			got.RealizedPL, got.TradingPL, got.Dividends)
	}
	if got.FirstTradedAt == nil || !got.FirstTradedAt.Equal(day(0)) {
		t.Errorf("first traded %v, want %v", got.FirstTradedAt, day(0))
	}
	if got.LastTradedAt == nil || !got.LastTradedAt.Equal(day(60)) {
		t.Errorf("last traded %v, want %v", got.LastTradedAt, day(60))
	}
}

// The yield is against what the remaining shares cost, which is the question a
// long-term holder asks — what the money they committed now pays them.
func TestHoldingDetailYieldIsMeasuredAgainstCost(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "income")
	inst := seedInstrument(t, s, "2330")
	priceAt(t, s, inst.ID, 100_000)

	// 1000 shares at NT$500 is a cost basis of NT$500,000.
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	// NT$20 a share over the year is NT$20,000, which is 4% of that cost —
	// and 2% of what the shares are now worth, which is the figure this
	// deliberately does not report.
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideDividend, 1000, 2000, 0, 100})

	got, err := s.HoldingDetail(user.ID, inst.ID, asOfDay(200))
	if err != nil {
		t.Fatalf("HoldingDetail: %v", err)
	}
	if got.TrailingDividends != 2_000_000 {
		t.Errorf("trailing dividends %d, want 2000000", got.TrailingDividends)
	}
	if got.YieldOnCostBps == nil || *got.YieldOnCostBps != 400 {
		t.Errorf("yield on cost %v, want 400 bps", got.YieldOnCostBps)
	}
}

// Older than a year is outside the window and must not be counted, or the
// figure stops being a rate per year and becomes a lifetime total.
func TestHoldingDetailTrailingYieldExcludesOlderPayouts(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "income")
	inst := seedInstrument(t, s, "2330")
	priceAt(t, s, inst.ID, 50_000)

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideDividend, 1000, 2000, 0, 30})

	// Two years on, that payout is long outside the window.
	got, err := s.HoldingDetail(user.ID, inst.ID, asOfDay(730))
	if err != nil {
		t.Fatalf("HoldingDetail: %v", err)
	}
	if got.TrailingDividends != 0 {
		t.Errorf("trailing dividends %d, want 0", got.TrailingDividends)
	}
	// Zero against a real cost basis is a genuine zero: nothing was banked.
	if got.YieldOnCostBps == nil || *got.YieldOnCostBps != 0 {
		t.Errorf("yield on cost %v, want 0 bps", got.YieldOnCostBps)
	}
	// The lifetime total still counts it, which is the other question.
	if got.Dividends != 2_000_000 {
		t.Errorf("lifetime dividends %d, want 2000000", got.Dividends)
	}
}

// Once the shares are gone there is no cost left to divide by, and 0% would
// read as an income holding that stopped paying.
func TestHoldingDetailYieldIsUnknownOnceFullyExited(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "seller")
	inst := seedInstrument(t, s, "2330")
	priceAt(t, s, inst.ID, 60_000)

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 1000, 60_000, 0, 400})

	got, err := s.HoldingDetail(user.ID, inst.ID, asOfDay(430))
	if err != nil {
		t.Fatalf("HoldingDetail: %v", err)
	}
	if got.YieldOnCostBps != nil {
		t.Errorf("yield on cost %v for a closed holding, want nil", *got.YieldOnCostBps)
	}
	// A closed holding needs no quote: its flows are complete and its ending
	// value is genuinely zero, so it still has a rate.
	if got.XIRRBps == nil {
		t.Fatalf("no rate for a closed holding: %q", got.Unavailable)
	}
}

// A doubling over a year is about 100% a year, and it is the holding's own
// flows that measure it — not the book's.
func TestHoldingDetailMeasuresItsOwnReturn(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "holder")
	inst := seedInstrument(t, s, "2330")
	other := seedInstrument(t, s, "2317")
	priceAt(t, s, inst.ID, 100_000)
	priceAt(t, s, other.ID, 1)

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	// A disaster in another holding, which must not touch this one's figure.
	mustRecord(t, s, user.ID, other.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})

	got, err := s.HoldingDetail(user.ID, inst.ID, asOfDay(365))
	if err != nil {
		t.Fatalf("HoldingDetail: %v", err)
	}
	if got.XIRRBps == nil {
		t.Fatalf("no rate: %q", got.Unavailable)
	}
	if *got.XIRRBps < 9900 || *got.XIRRBps > 10100 {
		t.Errorf("xirr %d bps, want about 10000 (a double over a year)", *got.XIRRBps)
	}
}

// Annualizing a short run compounds it over the rest of the year — 20% in a
// month prints as about 800% — so below a year there is no rate, and a reason.
// The span is measured to asOf while shares are held and to the last entry once
// they are gone, so a quick round trip stays short however long ago it was.
func TestHoldingDetailWithholdsTheRateUnderAYear(t *testing.T) {
	cases := []struct {
		name    string
		entries []entry
		asOf    int
		want    bool
	}{
		{"open for a month", []entry{{models.SideBuy, 1000, 50_000, 0, 0}}, 30, false},
		{"open a day short of a year", []entry{{models.SideBuy, 1000, 50_000, 0, 0}}, 364, false},
		{"open exactly a year", []entry{{models.SideBuy, 1000, 50_000, 0, 0}}, 365, true},
		{"closed after a month, asked long after", []entry{
			{models.SideBuy, 1000, 50_000, 0, 0},
			{models.SideSell, 1000, 60_000, 0, 30},
		}, 730, false},
		{"closed after more than a year", []entry{
			{models.SideBuy, 1000, 50_000, 0, 0},
			{models.SideSell, 1000, 60_000, 0, 400},
		}, 730, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestDB(t)
			user := seedUser(t, s, "holder")
			inst := seedInstrument(t, s, "2330")
			priceAt(t, s, inst.ID, 60_000)
			for _, e := range tc.entries {
				mustRecord(t, s, user.ID, inst.ID, e)
			}

			got, err := s.HoldingDetail(user.ID, inst.ID, asOfDay(tc.asOf))
			if err != nil {
				t.Fatalf("HoldingDetail: %v", err)
			}
			if tc.want {
				if got.XIRRBps == nil {
					t.Errorf("no rate: %q", got.Unavailable)
				}
				return
			}
			if got.XIRRBps != nil {
				t.Errorf("xirr %d bps under a year, want none", *got.XIRRBps)
			}
			if got.Unavailable == "" {
				t.Error("no rate and no reason given")
			}
		})
	}
}

// Unknown is not zero: an open holding with no quote has no ending value, and
// valuing it at nought would report a wipeout.
func TestHoldingDetailWithoutAQuoteReportsWhyRatherThanZero(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "holder")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})

	got, err := s.HoldingDetail(user.ID, inst.ID, asOfDay(365))
	if err != nil {
		t.Fatalf("HoldingDetail: %v", err)
	}
	if got.XIRRBps != nil {
		t.Errorf("xirr %d for an unpriced holding, want none", *got.XIRRBps)
	}
	if got.Unavailable == "" {
		t.Error("no rate and no reason given")
	}
}

// A ledger is personal: another user's holding is not found rather than
// forbidden, so an id cannot be probed for existence.
func TestHoldingDetailIsNotFoundForAnotherUser(t *testing.T) {
	s := newTestDB(t)
	holder := seedUser(t, s, "holder")
	other := seedUser(t, s, "other")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, holder.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})

	if _, err := s.HoldingDetail(other.ID, inst.ID, asOfDay(10)); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// The single-holding curve folds one instrument's trades and leaves the rest of
// the book out — which is the whole point of drawing it on a holding's page.
func TestInstrumentCurveFoldsOnlyThatHolding(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "holder")
	inst := seedInstrument(t, s, "2330")
	other := seedInstrument(t, s, "2317")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 50_000, 0, 0})
	mustRecord(t, s, user.ID, other.ID, entry{models.SideBuy, 1000, 10_000, 0, 0})
	closesFrom(t, s, inst.ID, 0, 50_000, 50_000, 50_000, 50_000)
	closesFrom(t, s, other.ID, 0, 10_000, 10_000, 10_000, 10_000)

	curves, err := s.InstrumentCurve(user.ID, inst.ID, "", dayStr(3))
	if err != nil {
		t.Fatalf("InstrumentCurve: %v", err)
	}
	if len(curves) != 1 {
		t.Fatalf("got %d curves, want 1: %+v", len(curves), curves)
	}
	if curves[0].Instruments != 1 {
		t.Errorf("curve folds %d instruments, want 1", curves[0].Instruments)
	}
	points := curves[0].Points
	if len(points) == 0 {
		t.Fatalf("no points: %q", curves[0].Unavailable)
	}
	// 1000 shares at NT$500, and nothing from the other holding.
	if got := points[len(points)-1].MarketValue; got != 50_000_000 {
		t.Errorf("market value %d, want 50000000 (this holding alone)", got)
	}
}
