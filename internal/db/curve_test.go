package db

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"stockbook/internal/models"
)

// seedInstrumentIn adds an instrument denominated in a given currency, for the
// tests that check the per-currency split.
func seedInstrumentIn(t *testing.T, s *DB, symbol string, currency models.Currency) models.Instrument {
	t.Helper()
	market := "TWSE"
	if currency == models.CurrencyUSD {
		market = "NASDAQ"
	}
	i, err := s.CreateInstrument(models.Instrument{
		ID: uuid.NewString(), Symbol: symbol, Name: symbol + " Corp",
		Market: market, Currency: currency,
	})
	if err != nil {
		t.Fatalf("CreateInstrument: %v", err)
	}
	return i
}

// closesFrom stores a run of consecutive daily closes starting at the given day
// offset, using the same base date the ledger helpers do.
func closesFrom(t *testing.T, s *DB, instrumentID string, startDay int, closes ...int64) {
	t.Helper()
	rows := make([]models.DailyClose, 0, len(closes))
	for i, close := range closes {
		rows = append(rows, models.DailyClose{
			Date:  day(startDay + i).UTC().Format(time.DateOnly),
			Close: close,
		})
	}
	if err := s.SaveDailyCloses(instrumentID, rows); err != nil {
		t.Fatalf("SaveDailyCloses: %v", err)
	}
}

func mustCurve(t *testing.T, s *DB, userID string) CurrencyCurve {
	t.Helper()
	curves, err := s.EquityCurve(userID, "", day(3650).UTC().Format(time.DateOnly))
	if err != nil {
		t.Fatalf("EquityCurve: %v", err)
	}
	if len(curves) != 1 {
		t.Fatalf("got %d curves, want 1: %+v", len(curves), curves)
	}
	return curves[0]
}

// The market value is the shares held at each session's close, and the index
// tracks it exactly when nothing is paid in after the first day.
func TestCurveTracksMarketValue(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "curver")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	closesFrom(t, s, inst.ID, 0, 10_000, 11_000, 12_000)

	curve := mustCurve(t, s, user.ID)
	if len(curve.Points) != 3 {
		t.Fatalf("got %d points, want 3: %+v", len(curve.Points), curve.Points)
	}
	wantValues := []int64{1_000_000, 1_100_000, 1_200_000}
	for i, want := range wantValues {
		if curve.Points[i].MarketValue != want {
			t.Errorf("point %d market value %d, want %d", i, curve.Points[i].MarketValue, want)
		}
	}
	// Bought at the first close, so the index starts flat and follows the price.
	wantIndex := []int64{10_000, 11_000, 12_000}
	for i, want := range wantIndex {
		if curve.Points[i].Index != want {
			t.Errorf("point %d index %d, want %d", i, curve.Points[i].Index, want)
		}
	}
	if curve.TWRBps == nil || *curve.TWRBps != 2000 {
		t.Errorf("TWR %v, want 2000 bps", curve.TWRBps)
	}
}

// The whole reason the index exists: paying more money in must not move it.
// Two books holding the same stock over the same days perform identically, even
// when one of them keeps adding to the position.
func TestCurveIndexIgnoresContributions(t *testing.T) {
	s := newTestDB(t)
	passive := seedUser(t, s, "passive")
	saver := seedUser(t, s, "saver")
	inst := seedInstrument(t, s, "2330")
	closesFrom(t, s, inst.ID, 0, 10_000, 11_000, 12_000)

	mustRecord(t, s, passive.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})

	// The same start, plus a second purchase partway through at the going price.
	mustRecord(t, s, saver.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	mustRecord(t, s, saver.ID, inst.ID, entry{models.SideBuy, 500, 11_000, 0, 1})

	passiveCurve := mustCurve(t, s, passive.ID)
	saverCurve := mustCurve(t, s, saver.ID)

	if saverCurve.Points[2].MarketValue <= passiveCurve.Points[2].MarketValue {
		t.Fatal("the saver should hold more, or this test proves nothing")
	}
	if *saverCurve.TWRBps != *passiveCurve.TWRBps {
		t.Errorf("saver returned %d bps and the passive book %d — buying more must "+
			"not change the return, only the amount it is earned on",
			*saverCurve.TWRBps, *passiveCurve.TWRBps)
	}
}

// Drawdown is measured on the index, so selling out is not a crash.
func TestCurveDrawdownIsNotMovedByWithdrawals(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "seller")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	// Sells the whole position on the last day, at the price it closed at.
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 100, 12_000, 0, 2})
	closesFrom(t, s, inst.ID, 0, 10_000, 11_000, 12_000)

	curve := mustCurve(t, s, user.ID)
	if curve.MaxDrawdownBps == nil {
		t.Fatal("no drawdown computed")
	}
	if *curve.MaxDrawdownBps != 0 {
		t.Errorf("drawdown %d bps, want 0: the book only ever rose, and taking "+
			"the money out at the end is not a fall", *curve.MaxDrawdownBps)
	}
}

// A real fall is reported, as a positive depth from the high-water mark.
func TestCurveReportsARealDrawdown(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "dipper")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	closesFrom(t, s, inst.ID, 0, 10_000, 20_000, 15_000, 18_000)

	curve := mustCurve(t, s, user.ID)
	// Peak 20,000, trough 15,000: a 25% fall.
	if *curve.MaxDrawdownBps != 2500 {
		t.Errorf("drawdown %d bps, want 2500", *curve.MaxDrawdownBps)
	}
	if *curve.TWRBps != 8000 {
		t.Errorf("TWR %d bps, want 8000 — the book still ended 80%% up", *curve.TWRBps)
	}
}

// A holiday leaves no bar, and the last close carries forward rather than the
// holding dropping to nothing for a day.
func TestCurveCarriesTheLastCloseThroughAGap(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "holiday")
	a := seedInstrument(t, s, "2330")
	b := seedInstrument(t, s, "2317")

	mustRecord(t, s, user.ID, a.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	mustRecord(t, s, user.ID, b.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	// Only b traded on the middle session; a's price has to carry.
	closesFrom(t, s, a.ID, 0, 10_000)
	if err := s.SaveDailyCloses(a.ID, []models.DailyClose{
		{Date: day(2).UTC().Format(time.DateOnly), Close: 10_000},
	}); err != nil {
		t.Fatalf("SaveDailyCloses: %v", err)
	}
	closesFrom(t, s, b.ID, 0, 10_000, 10_000, 10_000)

	curve := mustCurve(t, s, user.ID)
	if len(curve.Points) != 3 {
		t.Fatalf("got %d points, want 3: %+v", len(curve.Points), curve.Points)
	}
	for i, p := range curve.Points {
		if p.MarketValue != 2_000_000 {
			t.Errorf("point %d (%s) market value %d, want 2000000 — a session with "+
				"no bar must carry the last close, not drop the holding",
				i, p.Date, p.MarketValue)
		}
	}
}

// An instrument whose prices do not reach back to when it was held cannot be
// valued for those days. Counting it short would look like a real drawdown, so
// it leaves the curve entirely — trades and all — and is reported.
func TestCurveExcludesHoldingsWithoutEnoughHistory(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "partial")
	priced := seedInstrument(t, s, "2330")
	late := seedInstrument(t, s, "2454")

	mustRecord(t, s, user.ID, priced.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	mustRecord(t, s, user.ID, late.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	closesFrom(t, s, priced.ID, 0, 10_000, 11_000)
	// Prices begin a day after it was bought.
	closesFrom(t, s, late.ID, 1, 10_000, 11_000)

	curve := mustCurve(t, s, user.ID)
	if curve.Instruments != 2 || curve.WithoutHistory != 1 {
		t.Errorf("coverage %d of %d without history, want 1 of 2",
			curve.WithoutHistory, curve.Instruments)
	}
	for _, p := range curve.Points {
		if p.MarketValue > 1_100_000 {
			t.Errorf("the excluded holding leaked into the value: %+v", p)
		}
	}
}

// A book with no stored prices at all says so rather than reporting a flat line,
// which would read as a year of going nowhere.
func TestCurveSaysWhyWhenThereIsNoHistory(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "unsynced")
	inst := seedInstrument(t, s, "2330")
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})

	curve := mustCurve(t, s, user.ID)
	if len(curve.Points) != 0 {
		t.Errorf("got %d points, want none", len(curve.Points))
	}
	if curve.Unavailable == "" {
		t.Error("no reason given for the empty curve")
	}
	if curve.TWRBps != nil {
		t.Errorf("returned %d bps from a book it cannot value", *curve.TWRBps)
	}
}

// Currencies are separate books, on the same terms as every other total.
func TestCurveSeparatesCurrencies(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "two-books")
	twd := seedInstrument(t, s, "2330")
	usd := seedInstrumentIn(t, s, "AAPL", models.CurrencyUSD)

	mustRecord(t, s, user.ID, twd.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	mustRecord(t, s, user.ID, usd.ID, entry{models.SideBuy, 10, 20_000, 0, 0})
	closesFrom(t, s, twd.ID, 0, 10_000, 12_000)
	closesFrom(t, s, usd.ID, 0, 20_000, 10_000)

	curves, err := s.EquityCurve(user.ID, "", day(3650).UTC().Format(time.DateOnly))
	if err != nil {
		t.Fatalf("EquityCurve: %v", err)
	}
	if len(curves) != 2 {
		t.Fatalf("got %d curves, want one per currency", len(curves))
	}
	if curves[0].Currency != models.CurrencyTWD || curves[1].Currency != models.CurrencyUSD {
		t.Fatalf("not ordered by currency: %+v", curves)
	}
	if *curves[0].TWRBps != 2000 {
		t.Errorf("TWD returned %d bps, want 2000", *curves[0].TWRBps)
	}
	if *curves[1].TWRBps != -5000 {
		t.Errorf("USD returned %d bps, want -5000", *curves[1].TWRBps)
	}
}

// A trade dated on a day the market did not open still has to count; it folds
// into the next session rather than vanishing.
func TestCurveFoldsANonTradingDayIntoTheNextSession(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "weekend")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	// Bought on a day with no bar, between the two sessions that have one.
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 1})
	closesFrom(t, s, inst.ID, 0, 10_000)
	if err := s.SaveDailyCloses(inst.ID, []models.DailyClose{
		{Date: day(2).UTC().Format(time.DateOnly), Close: 10_000},
	}); err != nil {
		t.Fatalf("SaveDailyCloses: %v", err)
	}

	curve := mustCurve(t, s, user.ID)
	last := curve.Points[len(curve.Points)-1]
	if last.MarketValue != 2_000_000 {
		t.Errorf("final market value %d, want 2000000 — the trade dated on a "+
			"closed day was dropped", last.MarketValue)
	}
	if last.NetInvested != 2_000_000 {
		t.Errorf("net invested %d, want 2000000", last.NetInvested)
	}
}
