package db

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"stockbook/internal/models"
)

// buyOn records a purchase, which on the curve is money in on that day.
func buyOn(t *testing.T, s *DB, userID, instrumentID string, atDay int, qty, price int64) {
	t.Helper()
	if _, err := s.CreateTransaction(models.Transaction{
		ID: uuid.NewString(), UserID: userID, InstrumentID: instrumentID,
		Side: models.SideBuy, Quantity: qty, Price: price, TradedAt: day(atDay),
	}); err != nil {
		t.Fatalf("CreateTransaction: %v", err)
	}
}

func chooseBenchmark(t *testing.T, s *DB, userID string, currency models.Currency, instrumentID string) {
	t.Helper()
	if err := s.SaveBenchmark(userID, currency, instrumentID); err != nil {
		t.Fatalf("SaveBenchmark: %v", err)
	}
}

// The self-consistency check, and the one that would catch almost any error in
// the recursion: a book that holds nothing but the benchmark itself, bought on
// the same days, must report the benchmark as exactly level with it. Any
// asymmetry between how the book's value is folded and how the synthetic
// account is advanced shows up here as a gap that should not exist.
func TestABookHoldingOnlyTheBenchmarkMatchesIt(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "saver")
	bench := seedInstrument(t, s, "0050")
	closesFrom(t, s, bench.ID, 0, 10_000, 10_500, 9_800, 11_200, 11_000)

	// Bought on the first session and added to on the third, so the flow path
	// is exercised rather than a single lump left alone.
	buyOn(t, s, user.ID, bench.ID, 0, shares(10), 10_000)
	buyOn(t, s, user.ID, bench.ID, 2, shares(5), 9_800)
	chooseBenchmark(t, s, user.ID, models.CurrencyTWD, bench.ID)

	curve := mustCurve(t, s, user.ID)
	if curve.BenchmarkUnavailable != "" {
		t.Fatalf("no comparison: %s", curve.BenchmarkUnavailable)
	}
	for _, p := range curve.Points {
		// A rounding step on each side, so exact equality is too strong; one
		// minor unit is the most either can be out by.
		if diff := p.BenchmarkValue - p.MarketValue; diff > 1 || diff < -1 {
			t.Errorf("%s: benchmark %d vs book %d", p.Date, p.BenchmarkValue, p.MarketValue)
		}
		if diff := p.BenchmarkIndex - p.Index; diff > 1 || diff < -1 {
			t.Errorf("%s: benchmark index %d vs book %d", p.Date, p.BenchmarkIndex, p.Index)
		}
	}
}

// A benchmark's distributions are part of what holding it would have paid, and
// the book's own index already counts the dividends it received — so comparing
// against a bare price series would dock the benchmark every payout it ever
// made while crediting the book with all of its own.
func TestBenchmarkCountsItsDistributions(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "saver")
	held := seedInstrument(t, s, "2330")
	bench := seedInstrument(t, s, "0050")

	// Both flat in price over the window, so the only return either can report
	// is the distribution.
	closesFrom(t, s, held.ID, 0, 10_000, 10_000, 10_000)
	closesFrom(t, s, bench.ID, 0, 10_000, 10_000, 10_000)
	buyOn(t, s, user.ID, held.ID, 0, shares(10), 10_000)
	chooseBenchmark(t, s, user.ID, models.CurrencyTWD, bench.ID)

	flat := mustCurve(t, s, user.ID)
	if got := *flat.BenchmarkTWRBps; got != 0 {
		t.Fatalf("a flat benchmark paying nothing returned %d bps, want 0", got)
	}

	// NT$5.00 a share on a NT$100.00 share: 5% of the price, on the second
	// session of three.
	if err := s.SaveDividendEvents(bench.ID, []models.DividendEvent{
		{ExDate: day(1).UTC().Format(time.DateOnly), Amount: 500},
	}); err != nil {
		t.Fatalf("SaveDividendEvents: %v", err)
	}

	paid := mustCurve(t, s, user.ID)
	if got := *paid.BenchmarkTWRBps; got != 500 {
		t.Errorf("benchmark returned %d bps after a 5%% distribution, want 500", got)
	}
	// The book itself is untouched by what the benchmark paid.
	if *paid.TWRBps != *flat.TWRBps {
		t.Errorf("the book's own return moved with the benchmark's dividend")
	}
}

// The comparison starts level. Anything else would report a difference that
// was never earned — a benchmark starting from the cash paid rather than the
// value held would credit or dock it the first day's fill on day one.
func TestBenchmarkStartsFromTheBooksOwnValue(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "saver")
	held := seedInstrument(t, s, "2330")
	bench := seedInstrument(t, s, "0050")
	closesFrom(t, s, held.ID, 0, 12_000, 12_500)
	closesFrom(t, s, bench.ID, 0, 3_000, 3_300)

	// Bought at 100.00 but closing at 120.00: the fill and the close differ, so
	// starting from the wrong one is visible.
	buyOn(t, s, user.ID, held.ID, 0, shares(10), 10_000)
	chooseBenchmark(t, s, user.ID, models.CurrencyTWD, bench.ID)

	curve := mustCurve(t, s, user.ID)
	first := curve.Points[0]
	if first.BenchmarkValue != first.MarketValue {
		t.Errorf("day one: benchmark %d, book %d — the comparison must start level",
			first.BenchmarkValue, first.MarketValue)
	}
	if first.BenchmarkIndex != indexBase {
		t.Errorf("day one index %d, want the base %d", first.BenchmarkIndex, indexBase)
	}
	// 3000 -> 3300 is 10%, applied to the book's own opening value.
	if want := int64(float64(first.MarketValue) * 1.1); curve.Points[1].BenchmarkValue != want {
		t.Errorf("day two benchmark value %d, want %d", curve.Points[1].BenchmarkValue, want)
	}
}

// A benchmark quoted in another currency would compare a return against an
// exchange rate, which is the same reason no total here is ever summed across
// currencies.
func TestBenchmarkMustMatchTheCurrencyItMeasures(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "saver")
	usd := seedInstrumentIn(t, s, "VOO", models.CurrencyUSD)

	err := s.SaveBenchmark(user.ID, models.CurrencyTWD, usd.ID)
	if !errors.Is(err, ErrBenchmarkCurrency) {
		t.Errorf("SaveBenchmark across currencies: %v, want ErrBenchmarkCurrency", err)
	}
}

// Chosen but unpriced is a different problem from not chosen, and only one of
// them is fixed by syncing. Both must say which.
func TestBenchmarkWithoutHistorySaysSo(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "saver")
	held := seedInstrument(t, s, "2330")
	bench := seedInstrument(t, s, "0050")
	closesFrom(t, s, held.ID, 0, 10_000, 10_500)
	buyOn(t, s, user.ID, held.ID, 0, shares(10), 10_000)

	unset := mustCurve(t, s, user.ID)
	if unset.Benchmark != nil {
		t.Errorf("a benchmark appeared without being chosen: %+v", unset.Benchmark)
	}
	if unset.BenchmarkUnavailable == "" {
		t.Error("an unmeasured book must say it is unmeasured")
	}
	// Zero, not the index base. A point carrying 10000 would draw a flat line
	// at 100 that nothing had measured — a comparison the reader never made,
	// shown as if it had been made and come out even.
	for _, p := range unset.Points {
		if p.BenchmarkIndex != 0 || p.BenchmarkValue != 0 {
			t.Errorf("%s: benchmark figures %d/%d with no benchmark, want 0/0",
				p.Date, p.BenchmarkIndex, p.BenchmarkValue)
		}
	}

	// Chosen, but its prices begin after the book did.
	closesFrom(t, s, bench.ID, 5, 10_000, 10_100)
	chooseBenchmark(t, s, user.ID, models.CurrencyTWD, bench.ID)

	late := mustCurve(t, s, user.ID)
	if late.Benchmark == nil || late.Benchmark.Symbol != "0050" {
		t.Fatalf("the chosen benchmark should still be named: %+v", late.Benchmark)
	}
	if late.BenchmarkUnavailable == "" {
		t.Error("a benchmark whose history starts too late must say so, not compare half a period")
	}
	if late.BenchmarkTWRBps != nil {
		t.Errorf("a benchmark that could not run reported %d bps", *late.BenchmarkTWRBps)
	}
}

// The benchmark's prices have to reach back to the book's first trade, not to
// the day it was chosen — and an instrument nobody trades is the normal case
// for one, which is exactly what the history sync used to decline to fetch.
func TestHistoryIsNeededForABenchmarkNobodyTrades(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "saver")
	held := seedInstrument(t, s, "2330")
	bench := seedInstrument(t, s, "0050")
	buyOn(t, s, user.ID, held.ID, 7, shares(10), 10_000)
	chooseBenchmark(t, s, user.ID, models.CurrencyTWD, bench.ID)

	needed, err := s.EarliestHistoryNeeded(bench.ID)
	if err != nil {
		t.Fatalf("EarliestHistoryNeeded: %v", err)
	}
	want := day(7).UTC().Format(time.DateOnly)
	if needed != want {
		t.Errorf("benchmark history needed from %q, want the book's first trade %q", needed, want)
	}
}

// One book's benchmark is nobody else's business, like every other setting.
func TestAnotherUsersBenchmarkIsNotUsed(t *testing.T) {
	s := newTestDB(t)
	mine := seedUser(t, s, "mine")
	theirs := seedUser(t, s, "theirs")
	held := seedInstrument(t, s, "2330")
	bench := seedInstrument(t, s, "0050")
	closesFrom(t, s, held.ID, 0, 10_000, 10_500)
	closesFrom(t, s, bench.ID, 0, 10_000, 10_500)
	buyOn(t, s, mine.ID, held.ID, 0, shares(10), 10_000)
	chooseBenchmark(t, s, theirs.ID, models.CurrencyTWD, bench.ID)

	curve := mustCurve(t, s, mine.ID)
	if curve.Benchmark != nil {
		t.Errorf("another user's benchmark leaked into my curve: %+v", curve.Benchmark)
	}
}
