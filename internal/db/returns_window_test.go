package db

import (
	"math"
	"testing"
	"time"

	"stockbook/internal/models"
)

// dayStr names the same fixed base the ledger helpers use, as a bound.
func dayStr(n int) string {
	return day(n).UTC().Format(time.DateOnly)
}

func mustReturnsBetween(t *testing.T, s *DB, userID, from, to string) ReturnsSummary {
	t.Helper()
	got, err := s.ReturnsBetween(userID, from, to)
	if err != nil {
		t.Fatalf("ReturnsBetween: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d summaries, want 1: %+v", len(got), got)
	}
	return got[0]
}

// The whole point of the window: the position the period was entered holding is
// money out, so a book held untouched through a rising year reports the rise.
// Without that opening flow there would be no outflow at all, and no rate.
func TestWindowedReturnsOpensWithWhatWasAlreadyHeld(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "windower")
	inst := seedInstrument(t, s, "2330")

	// Bought long before the window, then left alone through it.
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 5_000, 0, 0})
	closesFrom(t, s, inst.ID, 0, 5_000)
	closesFrom(t, s, inst.ID, 365, 10_000)
	closesFrom(t, s, inst.ID, 730, 11_000)

	got := mustReturnsBetween(t, s, user.ID, dayStr(365), dayStr(730))

	// Entered the year holding 100 at 100.00, left it holding 100 at 110.00.
	if got.OpeningValue != 1_000_000 {
		t.Errorf("opening value %d, want 1000000", got.OpeningValue)
	}
	if got.EndingValue != 1_100_000 {
		t.Errorf("ending value %d, want 1100000", got.EndingValue)
	}
	// 10% over one year, with no flows in between, is 10% a year.
	if got.XIRRBps == nil {
		t.Fatalf("no rate: %s", got.Unavailable)
	}
	if math.Abs(float64(*got.XIRRBps)-1000) > 20 {
		t.Errorf("rate %d bps, want about 1000", *got.XIRRBps)
	}
}

// The same book measured over a period that predates it reports nothing rather
// than a rate over flows it never had.
func TestWindowedReturnsSaysWhenNothingWasHeld(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "early")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 5_000, 0, 400})
	closesFrom(t, s, inst.ID, 400, 5_000, 5_100)

	got := mustReturnsBetween(t, s, user.ID, dayStr(0), dayStr(100))
	if got.XIRRBps != nil {
		t.Errorf("got a rate of %d bps over a period the book did not exist in", *got.XIRRBps)
	}
	if got.Unavailable == "" {
		t.Error("no reason given for the absent rate")
	}
}

// A trade dated exactly on the opening bound is inside the opening value. It
// must not also be counted as a flow, which would charge the period twice for
// one purchase and report a loss that never happened.
func TestWindowedReturnsDoesNotCountTheOpeningDayTwice(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "boundary")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 10})
	closesFrom(t, s, inst.ID, 10, 10_000)
	closesFrom(t, s, inst.ID, 375, 10_000)

	got := mustReturnsBetween(t, s, user.ID, dayStr(10), dayStr(375))

	if got.OpeningValue != 1_000_000 {
		t.Errorf("opening value %d, want the shares bought that day", got.OpeningValue)
	}
	if got.Invested != 0 {
		t.Errorf("invested %d, want 0 — that purchase is the opening position", got.Invested)
	}
	// Flat prices and no flows: a period that neither made nor lost anything.
	if got.XIRRBps == nil {
		t.Fatalf("no rate: %s", got.Unavailable)
	}
	if math.Abs(float64(*got.XIRRBps)) > 20 {
		t.Errorf("rate %d bps, want about 0 over a flat year", *got.XIRRBps)
	}
}

// A sale inside the window is money in, and the shares it removed are gone from
// the closing value — the two have to agree or the rate is nonsense.
func TestWindowedReturnsCountsASaleInThePeriod(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "seller")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 50, 12_000, 0, 200})
	closesFrom(t, s, inst.ID, 0, 10_000)
	closesFrom(t, s, inst.ID, 100, 12_000)
	closesFrom(t, s, inst.ID, 200, 12_000)
	closesFrom(t, s, inst.ID, 365, 12_000)

	got := mustReturnsBetween(t, s, user.ID, dayStr(100), dayStr(365))

	if got.Returned != 600_000 {
		t.Errorf("returned %d, want the 50 shares sold at 120.00", got.Returned)
	}
	if got.EndingValue != 600_000 {
		t.Errorf("ending value %d, want the 50 shares still held", got.EndingValue)
	}
	// Entered holding 100 at 120.00, took half out at the same price, ended with
	// the rest at the same price: nothing gained.
	if got.NetGain != 0 {
		t.Errorf("net gain %d, want 0", got.NetGain)
	}
}

// The windowed report inherits the curve's exclusion rather than the live-quote
// one: what it cannot value historically it drops whole, and says how many.
func TestWindowedReturnsExcludesHoldingsWithoutHistory(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "gappy")
	priced := seedInstrument(t, s, "2330")
	unpriced := seedInstrument(t, s, "2454")

	mustRecord(t, s, user.ID, priced.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	mustRecord(t, s, user.ID, unpriced.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	closesFrom(t, s, priced.ID, 0, 10_000)
	closesFrom(t, s, priced.ID, 365, 11_000)
	// The second instrument's prices begin long after it was first held.
	closesFrom(t, s, unpriced.ID, 300, 99_000)

	got := mustReturnsBetween(t, s, user.ID, dayStr(0), dayStr(365))

	if got.WithoutHistory != 1 {
		t.Errorf("without history %d, want 1", got.WithoutHistory)
	}
	// Only the priced holding is measured, so the ending value is its alone.
	if got.EndingValue != 1_100_000 {
		t.Errorf("ending value %d, want only the instrument with history", got.EndingValue)
	}
}

// Two currencies are two books, as everywhere else here.
func TestWindowedReturnsSeparatesCurrencies(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "mixed")
	tw := seedInstrumentIn(t, s, "2330", models.CurrencyTWD)
	us := seedInstrumentIn(t, s, "AAPL", models.CurrencyUSD)

	mustRecord(t, s, user.ID, tw.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	mustRecord(t, s, user.ID, us.ID, entry{models.SideBuy, 10, 20_000, 0, 0})
	closesFrom(t, s, tw.ID, 0, 10_000)
	closesFrom(t, s, tw.ID, 365, 11_000)
	closesFrom(t, s, us.ID, 0, 20_000)
	closesFrom(t, s, us.ID, 365, 30_000)

	got, err := s.ReturnsBetween(user.ID, dayStr(0), dayStr(365))
	if err != nil {
		t.Fatalf("ReturnsBetween: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d summaries, want one per currency: %+v", len(got), got)
	}
	if got[0].Currency != models.CurrencyTWD || got[1].Currency != models.CurrencyUSD {
		t.Errorf("currencies %s and %s, want TWD then USD", got[0].Currency, got[1].Currency)
	}
	// Nothing is ever added across them.
	if got[0].EndingValue != 1_100_000 {
		t.Errorf("TWD ending value %d, want 1100000", got[0].EndingValue)
	}
	if got[1].EndingValue != 300_000 {
		t.Errorf("USD ending value %d, want 300000", got[1].EndingValue)
	}
}

// An open `from` means since the book began, which needs no opening position —
// the first purchase is the first flow, as on the since-inception report.
func TestWindowedReturnsWithAnOpenStartHasNoOpeningPosition(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "opener")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	closesFrom(t, s, inst.ID, 0, 10_000)
	closesFrom(t, s, inst.ID, 365, 11_000)

	got := mustReturnsBetween(t, s, user.ID, "", dayStr(365))

	if got.OpeningValue != 0 {
		t.Errorf("opening value %d, want none for an open start", got.OpeningValue)
	}
	if got.Invested != 1_000_000 {
		t.Errorf("invested %d, want the purchase counted as a flow", got.Invested)
	}
	if got.XIRRBps == nil {
		t.Fatalf("no rate: %s", got.Unavailable)
	}
	if math.Abs(float64(*got.XIRRBps)-1000) > 20 {
		t.Errorf("rate %d bps, want about 1000", *got.XIRRBps)
	}
}

// A period whose far bound runs past the last stored session must be measured
// to that session, not to the bound. Dating the closing figure in the future
// spreads the same gain over a longer assumed period and reports a lower annual
// rate, with nothing on screen to say why — which is exactly what asking for
// "this year" in September does.
func TestWindowedReturnsClosesAtTheLastSessionNotTheBound(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "partway")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	// Prices run to the half-year and stop; the book is up 50% by then.
	closesFrom(t, s, inst.ID, 0, 10_000)
	closesFrom(t, s, inst.ID, 182, 15_000)

	// Asked for the whole year, though only half of it has happened.
	got := mustReturnsBetween(t, s, user.ID, dayStr(0), dayStr(365))

	if want := day(182).UTC().Format(time.DateOnly); got.AsOf.Format(time.DateOnly) != want {
		t.Errorf("valued as of %s, want the last stored session %s",
			got.AsOf.Format(time.DateOnly), want)
	}
	if got.XIRRBps == nil {
		t.Fatalf("no rate: %s", got.Unavailable)
	}
	// +50% in half a year annualizes to about +125%, not the +50% it would
	// report if the gain were spread across the full year asked for.
	if *got.XIRRBps < 11_000 {
		t.Errorf("rate %d bps, want about 12500 — the gain was earned in half a year",
			*got.XIRRBps)
	}
}
