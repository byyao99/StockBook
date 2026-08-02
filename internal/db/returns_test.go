package db

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"stockbook/internal/models"
)

// priceAt gives an instrument a quote, which is what decides whether its
// holding can be valued and therefore whether it is counted at all.
func priceAt(t *testing.T, s *DB, instrumentID string, price int64) {
	t.Helper()
	if _, err := s.UpdateInstrumentPrice(instrumentID, QuoteUpdate{Price: &price}); err != nil {
		t.Fatalf("UpdateInstrumentPrice: %v", err)
	}
}

// only returns the single currency summary a test expects, failing otherwise.
func only(t *testing.T, summaries []ReturnsSummary) ReturnsSummary {
	t.Helper()
	if len(summaries) != 1 {
		t.Fatalf("got %d currency summaries, want 1: %+v", len(summaries), summaries)
	}
	return summaries[0]
}

// Shares still held are money the book has not given back yet, so the current
// market value closes the cash flows. Without that final payment in, a holding
// nobody ever sold would measure as a total loss.
func TestReturnsCountsUnsoldSharesAtMarketValue(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "holder")
	inst := seedInstrument(t, s, "2330")

	// 100 shares at 100.00, worth 120.00 a year later: +20% over exactly a year.
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	priceAt(t, s, inst.ID, 12_000)

	summary := only(t, mustReturns(t, s, user.ID, day(365)))
	if summary.Invested != 1_000_000 || summary.Returned != 0 || summary.EndingValue != 1_200_000 {
		t.Errorf("unexpected components: %+v", summary)
	}
	if summary.NetGain != 200_000 {
		t.Errorf("net gain %d, want 200000", summary.NetGain)
	}
	if summary.XIRRBps == nil {
		t.Fatalf("no rate computed: %q", summary.Unavailable)
	}
	if *summary.XIRRBps != 2000 {
		t.Errorf("rate %d bps, want 2000 (20%% over one year)", *summary.XIRRBps)
	}
}

// A dividend is money returned, so it lifts the rate even when the share price
// has not moved at all.
func TestReturnsCountsDividendsAsMoneyBack(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "income")
	inst := seedInstrument(t, s, "0056")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 3_000, 0, 0})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideDividend, 1000, 150, 0, 180})
	priceAt(t, s, inst.ID, 3_000) // unchanged price

	summary := only(t, mustReturns(t, s, user.ID, day(365)))
	if summary.Returned != 150_000 {
		t.Errorf("returned %d, want 150000 from the dividend", summary.Returned)
	}
	if summary.NetGain != summary.Returned+summary.EndingValue-summary.Invested {
		t.Errorf("net gain %d does not reconcile: %+v", summary.NetGain, summary)
	}
	if summary.XIRRBps == nil {
		t.Fatalf("no rate computed: %q", summary.Unavailable)
	}
	if *summary.XIRRBps <= 0 {
		t.Errorf("rate %d bps, want positive: the price went nowhere but the "+
			"holding paid 5%%", *summary.XIRRBps)
	}
}

// An open holding with no quote takes its whole history out of the report. Its
// ending value is unknown, and keeping its purchases while dropping only that
// would report the holding as a wipeout with nothing to say it was missing.
func TestReturnsExcludesUnpricedHoldingsWhole(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "partly-priced")
	priced := seedInstrument(t, s, "2330")
	unpriced := seedInstrument(t, s, "2454")

	mustRecord(t, s, user.ID, priced.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	priceAt(t, s, priced.ID, 12_000)
	// Same size, never quoted. Counting its 1,000,000 of cost against no ending
	// value would drag the whole book to roughly -60%.
	mustRecord(t, s, user.ID, unpriced.ID, entry{models.SideBuy, 100, 10_000, 0, 0})

	summary := only(t, mustReturns(t, s, user.ID, day(365)))
	if summary.OpenPositions != 2 || summary.PricedPositions != 1 {
		t.Errorf("coverage %d priced of %d open, want 1 of 2",
			summary.PricedPositions, summary.OpenPositions)
	}
	if summary.Invested != 1_000_000 {
		t.Errorf("invested %d, want 1000000: the unpriced holding's buy must be "+
			"left out along with its value", summary.Invested)
	}
	if summary.XIRRBps == nil || *summary.XIRRBps != 2000 {
		t.Errorf("rate %v, want 2000 bps — the same answer as if the unpriced "+
			"holding were not in the book at all (%q)", summary.XIRRBps, summary.Unavailable)
	}
}

// A book that cannot be valued at all reports why rather than reporting 0%,
// which would read as breaking even.
func TestReturnsSaysWhyWhenNothingCanBeValued(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "unquoted")
	inst := seedInstrument(t, s, "9999")
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})

	summary := only(t, mustReturns(t, s, user.ID, day(365)))
	if summary.XIRRBps != nil {
		t.Errorf("rate %d, want none: nothing in this book has a quote", *summary.XIRRBps)
	}
	if summary.Unavailable == "" {
		t.Error("no reason given for the missing rate")
	}
	if summary.OpenPositions != 1 || summary.PricedPositions != 0 {
		t.Errorf("coverage %d priced of %d open, want 0 of 1",
			summary.PricedPositions, summary.OpenPositions)
	}
}

// Currencies are reported apart, like every other total here: there is no FX
// rate, so one rate of return across a TWD and a USD book would be invented.
func TestReturnsSeparatesCurrencies(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "two-books")

	twd := seedInstrument(t, s, "2330")
	usd, err := s.CreateInstrument(models.Instrument{
		ID: uuid.NewString(), Symbol: "AAPL", Name: "Apple", Market: "NASDAQ",
		Currency: models.CurrencyUSD,
	})
	if err != nil {
		t.Fatalf("CreateInstrument: %v", err)
	}

	mustRecord(t, s, user.ID, twd.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	priceAt(t, s, twd.ID, 12_000)
	mustRecord(t, s, user.ID, usd.ID, entry{models.SideBuy, 10, 20_000, 0, 0})
	priceAt(t, s, usd.ID, 10_000) // halved

	summaries := mustReturns(t, s, user.ID, day(365))
	if len(summaries) != 2 {
		t.Fatalf("got %d summaries, want one per currency: %+v", len(summaries), summaries)
	}
	if summaries[0].Currency != models.CurrencyTWD || summaries[1].Currency != models.CurrencyUSD {
		t.Fatalf("not ordered by currency: %+v", summaries)
	}
	if summaries[0].XIRRBps == nil || *summaries[0].XIRRBps != 2000 {
		t.Errorf("TWD rate %v, want 2000 bps", summaries[0].XIRRBps)
	}
	if summaries[1].XIRRBps == nil || *summaries[1].XIRRBps != -5000 {
		t.Errorf("USD rate %v, want -5000 bps", summaries[1].XIRRBps)
	}
}

// A closed holding needs no quote: its cash flows are complete and its ending
// value is genuinely zero, so it is always counted.
func TestReturnsCountsClosedHoldingsWithoutAQuote(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "exited")
	inst := seedInstrument(t, s, "2317")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 100, 12_000, 0, 365})

	summary := only(t, mustReturns(t, s, user.ID, day(400)))
	if summary.OpenPositions != 0 || summary.EndingValue != 0 {
		t.Errorf("a fully exited book should hold nothing: %+v", summary)
	}
	if summary.XIRRBps == nil || *summary.XIRRBps != 2000 {
		t.Errorf("rate %v, want 2000 bps (%q)", summary.XIRRBps, summary.Unavailable)
	}
}

func mustReturns(t *testing.T, s *DB, userID string, asOf time.Time) []ReturnsSummary {
	t.Helper()
	summaries, err := s.ReturnsReport(userID, asOf)
	if err != nil {
		t.Fatalf("ReturnsReport: %v", err)
	}
	return summaries
}
