package db

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"stockbook/internal/models"
)

// stamps reads a user's ledger for one instrument in ledger order and returns
// the realized stamp of each entry.
func stamps(t *testing.T, s *DB, userID, instrumentID string) []*int64 {
	t.Helper()
	var txs []models.Transaction
	if err := s.db.Where("user_id = ? AND instrument_id = ?", userID, instrumentID).
		Order(ledgerOrder).Find(&txs).Error; err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	out := make([]*int64, 0, len(txs))
	for _, tx := range txs {
		out = append(out, tx.RealizedPL)
	}
	return out
}

// The stamps are a decomposition of the position's running total, so they must
// add back up to it — for every sequence, through every write path. This is the
// test that makes the per-entry numbers trustworthy: a report built on stamps
// that do not sum to the position is reporting a different book than the
// holdings page.
func TestStampsSumToPositionRealized(t *testing.T) {
	sequences := map[string][]entry{
		"single buy":            {{models.SideBuy, 100, 5000, 0, 1}},
		"buy sell partial":      {{models.SideBuy, 200, 6000, 0, 1}, {models.SideSell, 50, 8000, 0, 2}},
		"full exit":             {{models.SideBuy, 100, 5000, 0, 1}, {models.SideSell, 100, 6000, 0, 2}},
		"with fees":             {{models.SideBuy, 100, 5000, 1425, 1}, {models.SideSell, 40, 5500, 900, 2}},
		"uneven cost basis":     {{models.SideBuy, 3, 1000, 0, 1}, {models.SideBuy, 1, 1001, 0, 2}, {models.SideSell, 1, 1500, 0, 3}},
		"many small trades":     {{models.SideBuy, 7, 333, 11, 1}, {models.SideBuy, 13, 777, 3, 2}, {models.SideSell, 5, 900, 7, 3}, {models.SideBuy, 2, 1234, 1, 4}, {models.SideSell, 9, 450, 5, 5}},
		"same day trades":       {{models.SideBuy, 10, 1000, 0, 1}, {models.SideBuy, 10, 2000, 0, 1}, {models.SideSell, 15, 1800, 0, 1}},
		"sell everything twice": {{models.SideBuy, 10, 1000, 0, 1}, {models.SideSell, 10, 1200, 0, 2}, {models.SideBuy, 10, 900, 0, 3}, {models.SideSell, 10, 800, 0, 4}},
		"back-dated buy":        {{models.SideBuy, 100, 9000, 0, 3}, {models.SideSell, 50, 9500, 0, 5}, {models.SideBuy, 100, 7000, 0, 1}},
		"dividend then sell":    {{models.SideBuy, 1000, 7000, 0, 1}, {models.SideDividend, 1000, 500, 0, 2}, {models.SideSell, 400, 9500, 0, 3}},
		"dividend after exit":   {{models.SideBuy, 1000, 7000, 0, 1}, {models.SideSell, 1000, 9500, 0, 2}, {models.SideDividend, 1000, 500, 0, 3}},
		"back-dated dividend":   {{models.SideBuy, 1000, 7000, 0, 1}, {models.SideSell, 400, 9500, 0, 5}, {models.SideDividend, 1000, 500, 0, 3}},
	}

	for name, seq := range sequences {
		t.Run(name, func(t *testing.T) {
			s := newTestDB(t)
			user := seedUser(t, s, "u-"+uuid.NewString()[:8])
			inst := seedInstrument(t, s, "S"+uuid.NewString()[:6])
			for _, e := range seq {
				mustRecord(t, s, user.ID, inst.ID, e)
			}

			var total int64
			for _, stamp := range stamps(t, s, user.ID, inst.ID) {
				if stamp != nil {
					total += *stamp
				}
			}
			if want := storedState(t, s, user.ID, inst.ID).RealizedPL; total != want {
				t.Errorf("stamps sum to %d, position holds %d", total, want)
			}
		})
	}
}

// A buy realizes nothing, which is not the same as realizing zero: the column
// stays NULL so a report can tell "no result" from "broke even".
func TestBuysAreNotStamped(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "buyer")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 5000, 0, 1})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 100, 5000, 0, 2})

	got := stamps(t, s, user.ID, inst.ID)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0] != nil {
		t.Errorf("buy stamped with %d, want nil", *got[0])
	}
	// Sold at cost: a real zero, which must be stored as one rather than left
	// looking like an absent value.
	if got[1] == nil || *got[1] != 0 {
		t.Errorf("break-even sell stamped %v, want 0", got[1])
	}
}

// A buy squeezed into the middle of history changes the average the later sell
// was measured against, so that sell's stamp has to be rewritten too. This is
// the case an "only stamp the row being written" shortcut would get wrong.
func TestBackDatedBuyRestampsLaterSell(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "backdater")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 9000, 0, 3})
	sell := mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 50, 9500, 0, 5})

	// 50 shares bought at 9000 and sold at 9500 banks 25,000.
	if sell.RealizedPL == nil || *sell.RealizedPL != 25000 {
		t.Fatalf("initial stamp %v, want 25000", sell.RealizedPL)
	}

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 7000, 0, 1})

	// The average is now 8000, so the same sale banks 75,000 instead.
	after, err := s.GetTransaction(sell.ID, user.ID)
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	if after.RealizedPL == nil || *after.RealizedPL != 75000 {
		t.Errorf("restamped to %v, want 75000", after.RealizedPL)
	}
}

// An edit and a delete both replay, and both must leave the stamps consistent
// with the position they produced.
func TestEditAndDeleteRestampLedger(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "editor")
	inst := seedInstrument(t, s, "2330")

	buy := mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 7000, 0, 1})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 9000, 0, 2})
	sell := mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 50, 9500, 0, 3})

	// Correcting the first buy's price moves the average, and with it the sale.
	if _, err := s.UpdateTransaction(buy.ID, user.ID, TransactionUpdate{
		Quantity: 100, Price: 5000, TradedAt: day(1),
	}); err != nil {
		t.Fatalf("UpdateTransaction: %v", err)
	}
	// Average 7000: the sale releases 350,000 and brings in 475,000.
	assertStamp(t, s, user.ID, sell.ID, 125000)

	// Removing the cheaper buy leaves only the 9000 lot behind the sale.
	if err := s.DeleteTransaction(buy.ID, user.ID); err != nil {
		t.Fatalf("DeleteTransaction: %v", err)
	}
	assertStamp(t, s, user.ID, sell.ID, 25000)
}

func assertStamp(t *testing.T, s *DB, userID, txID string, want int64) {
	t.Helper()
	got, err := s.GetTransaction(txID, userID)
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	if got.RealizedPL == nil || *got.RealizedPL != want {
		t.Fatalf("stamp %v, want %d", got.RealizedPL, want)
	}
	// The decomposition must still add up to the total it came from.
	var total int64
	for _, stamp := range stamps(t, s, userID, got.InstrumentID) {
		if stamp != nil {
			total += *stamp
		}
	}
	if position := storedState(t, s, userID, got.InstrumentID).RealizedPL; total != position {
		t.Fatalf("stamps sum to %d, position holds %d", total, position)
	}
}

// Sells recorded before the column existed are stamped on the next start, by
// replaying the holdings that contain them. Clearing the column by hand is the
// closest a test can get to reopening a database written by the old code.
func TestBackfillStampsExistingSells(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backfill.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	user := seedUser(t, s, "legacy")
	inst := seedInstrument(t, s, "2330")
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 7000, 0, 1})
	sell := mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 50, 9500, 0, 2})

	if err := s.db.Model(&models.Transaction{}).Where("1 = 1").
		Updates(map[string]any{"realized_pl": nil}).Error; err != nil {
		t.Fatalf("clear stamps: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })

	got, err := reopened.GetTransaction(sell.ID, user.ID)
	if err != nil {
		t.Fatalf("GetTransaction: %v", err)
	}
	if got.RealizedPL == nil || *got.RealizedPL != 125000 {
		t.Errorf("backfilled stamp %v, want 125000", got.RealizedPL)
	}
}

// A dividend banks its payout without moving the holding, and — unlike a buy
// landing mid-history — it does not disturb the average, so the sells around it
// keep their own results.
func TestDividendStampsPayoutAndLeavesHoldingAlone(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "holder")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 70000, 0, 1})
	sell := mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 400, 95000, 0, 2})
	before := storedState(t, s, user.ID, inst.ID)

	div := mustRecord(t, s, user.ID, inst.ID, entry{models.SideDividend, 600, 500, 0, 3})
	if div.RealizedPL == nil || *div.RealizedPL != 300000 {
		t.Fatalf("dividend stamped %v, want 300000", div.RealizedPL)
	}
	if div.NetAmount != 300000 {
		t.Errorf("dividend net %d, want 300000", div.NetAmount)
	}

	after := storedState(t, s, user.ID, inst.ID)
	if after.Quantity != before.Quantity || after.CostBasis != before.CostBasis {
		t.Errorf("holding moved from %+v to %+v", before, after)
	}
	if want := before.RealizedPL + 300000; after.RealizedPL != want {
		t.Errorf("realized %d, want %d", after.RealizedPL, want)
	}

	// The earlier sale's own result is untouched: a dividend changes no average.
	assertStamp(t, s, user.ID, sell.ID, *sell.RealizedPL)
}

func TestRealizedReport(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "reporter")
	twse := seedInstrument(t, s, "2330")
	other := seedInstrument(t, s, "2317")
	nasdaq, err := s.CreateInstrument(models.Instrument{
		ID: uuid.NewString(), Symbol: "AAPL", Name: "Apple", Market: "NASDAQ",
		Currency: models.CurrencyUSD,
	})
	if err != nil {
		t.Fatalf("CreateInstrument: %v", err)
	}

	// 2330: bought at 7000, half sold at 9500 on day 2 for +125,000.
	mustRecord(t, s, user.ID, twse.ID, entry{models.SideBuy, 100, 7000, 0, 1})
	mustRecord(t, s, user.ID, twse.ID, entry{models.SideSell, 50, 9500, 0, 2})
	// 2317: a loss of 20,000 on day 3.
	mustRecord(t, s, user.ID, other.ID, entry{models.SideBuy, 100, 5000, 0, 1})
	mustRecord(t, s, user.ID, other.ID, entry{models.SideSell, 100, 4800, 0, 3})
	// AAPL: a USD gain that must never join the TWD total.
	mustRecord(t, s, user.ID, nasdaq.ID, entry{models.SideBuy, 10, 15000, 0, 1})
	mustRecord(t, s, user.ID, nasdaq.ID, entry{models.SideSell, 10, 20000, 0, 4})

	report, err := s.RealizedReport(user.ID, nil, nil)
	if err != nil {
		t.Fatalf("RealizedReport: %v", err)
	}
	if len(report) != 2 {
		t.Fatalf("got %d currencies, want 2 (TWD and USD kept apart)", len(report))
	}

	twd, usd := report[0], report[1]
	if twd.Currency != models.CurrencyTWD || usd.Currency != models.CurrencyUSD {
		t.Fatalf("currencies out of order: %s then %s", twd.Currency, usd.Currency)
	}
	if want := int64(125000 - 20000); twd.RealizedPL != want {
		t.Errorf("TWD realized %d, want %d", twd.RealizedPL, want)
	}
	if want := int64(50000); usd.RealizedPL != want {
		t.Errorf("USD realized %d, want %d", usd.RealizedPL, want)
	}
	if twd.TradingPL != twd.Proceeds-twd.Cost {
		t.Errorf("trading %d != proceeds %d - cost %d", twd.TradingPL, twd.Proceeds, twd.Cost)
	}
	if twd.UnstampedEntries != 0 {
		t.Errorf("unstamped sells %d, want 0", twd.UnstampedEntries)
	}

	// Instruments rank by contribution, so the winner leads.
	if len(twd.Instruments) != 2 {
		t.Fatalf("got %d TWD instruments, want 2", len(twd.Instruments))
	}
	if twd.Instruments[0].Symbol != "2330" || twd.Instruments[1].Symbol != "2317" {
		t.Errorf("instrument order %s, %s", twd.Instruments[0].Symbol, twd.Instruments[1].Symbol)
	}
	if twd.Instruments[0].Sells != 1 {
		t.Errorf("2330 sells %d, want 1", twd.Instruments[0].Sells)
	}
}

// Trading results and dividend income are reported apart. They answer different
// questions and are taxed differently, and a book that lost money on price while
// making it back on income must not read as a flat year.
func TestRealizedReportSeparatesDividendsFromTrading(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "incomer")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 70000, 0, 1})
	// Sold at a loss of 200,000, but 300,000 of dividends more than covered it.
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 1000, 50000, 0, 3})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideDividend, 1000, 300, 0, 2})

	report, err := s.RealizedReport(user.ID, nil, nil)
	if err != nil {
		t.Fatalf("RealizedReport: %v", err)
	}
	if len(report) != 1 {
		t.Fatalf("got %d currencies, want 1", len(report))
	}
	got := report[0]

	if got.TradingPL != -20000000 {
		t.Errorf("trading %d, want -20000000", got.TradingPL)
	}
	if got.Dividends != 300000 {
		t.Errorf("dividends %d, want 300000", got.Dividends)
	}
	if want := got.TradingPL + got.Dividends; got.RealizedPL != want {
		t.Errorf("realized %d != trading + dividends (%d)", got.RealizedPL, want)
	}
	if got.Sells != 1 || got.DividendCount != 1 {
		t.Errorf("counts: %d sells, %d dividends; want 1 and 1", got.Sells, got.DividendCount)
	}
	// Proceeds and cost describe the sales alone, so the identity that makes
	// them checkable is against trading, not against the combined total.
	if got.TradingPL != got.Proceeds-got.Cost {
		t.Errorf("trading %d != proceeds %d - cost %d", got.TradingPL, got.Proceeds, got.Cost)
	}
	if got.Dividends != 0 && got.Cost == got.Proceeds {
		t.Error("dividend leaked into the sales figures")
	}

	if len(got.Instruments) != 1 {
		t.Fatalf("got %d instruments, want 1", len(got.Instruments))
	}
	row := got.Instruments[0]
	if row.TradingPL != got.TradingPL || row.Dividends != got.Dividends {
		t.Errorf("row %+v disagrees with the currency total %+v", row, got)
	}
}

// The period bounds decide which year a sale is counted in, so both ends have to
// bite — and the day named by the upper bound belongs inside the period.
func TestRealizedReportPeriodBounds(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "periods")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 5000, 0, 1})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 50, 6000, 0, 2})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 50, 7000, 0, 9})

	from, to := day(2), day(2).Add(24*time.Hour-time.Nanosecond)
	report, err := s.RealizedReport(user.ID, &from, &to)
	if err != nil {
		t.Fatalf("RealizedReport: %v", err)
	}
	if len(report) != 1 {
		t.Fatalf("got %d currencies, want 1", len(report))
	}
	if want := int64(50000); report[0].RealizedPL != want {
		t.Errorf("realized %d, want %d (only the day-2 sale)", report[0].RealizedPL, want)
	}
	if report[0].Sells != 1 {
		t.Errorf("sells %d, want 1", report[0].Sells)
	}
}

// A ledger is strictly personal, and so is every report over it.
func TestRealizedReportIsScopedToOwner(t *testing.T) {
	s := newTestDB(t)
	owner := seedUser(t, s, "owner")
	stranger := seedUser(t, s, "stranger")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, owner.ID, inst.ID, entry{models.SideBuy, 100, 5000, 0, 1})
	mustRecord(t, s, owner.ID, inst.ID, entry{models.SideSell, 100, 6000, 0, 2})

	report, err := s.RealizedReport(stranger.ID, nil, nil)
	if err != nil {
		t.Fatalf("RealizedReport: %v", err)
	}
	if len(report) != 0 {
		t.Errorf("stranger sees %+v, want nothing", report)
	}
}

// An unstamped sell is reported as unaccounted for rather than summed in as
// zero, which would understate the period and look like a break-even trade.
func TestRealizedReportCountsUnstampedEntries(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "unstamped")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 5000, 0, 1})
	sell := mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 100, 6000, 0, 2})
	if err := s.db.Model(&models.Transaction{}).Where("id = ?", sell.ID).
		Updates(map[string]any{"realized_pl": nil}).Error; err != nil {
		t.Fatalf("clear stamp: %v", err)
	}

	report, err := s.RealizedReport(user.ID, nil, nil)
	if err != nil {
		t.Fatalf("RealizedReport: %v", err)
	}
	if len(report) != 1 {
		t.Fatalf("got %d currencies, want 1", len(report))
	}
	if report[0].UnstampedEntries != 1 {
		t.Errorf("unstamped %d, want 1", report[0].UnstampedEntries)
	}
	if report[0].RealizedPL != 0 || report[0].Sells != 0 {
		t.Errorf("unstamped sale leaked into the totals: %+v", report[0])
	}
	if len(report[0].Instruments) != 0 {
		t.Errorf("unstamped sale produced an instrument row: %+v", report[0].Instruments)
	}
}

// A ledger left inconsistent by some earlier bug must not stop the database from
// opening: its sells stay unstamped and the report says so.
func TestBackfillSkipsInconsistentHolding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	user := seedUser(t, s, "broken")
	inst := seedInstrument(t, s, "2330")
	buy := mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 7000, 0, 1})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 50, 9500, 0, 2})

	// Delete the buy behind the sale directly, which the API would refuse, and
	// clear the stamps so the backfill has a reason to visit this holding.
	if err := s.db.Delete(&models.Transaction{}, "id = ?", buy.ID).Error; err != nil {
		t.Fatalf("delete buy: %v", err)
	}
	if err := s.db.Model(&models.Transaction{}).Where("1 = 1").
		Updates(map[string]any{"realized_pl": nil}).Error; err != nil {
		t.Fatalf("clear stamps: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen refused to start over one bad holding: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })

	if _, err := reopened.ReplayPosition(user.ID, inst.ID); !errors.Is(err, models.ErrInsufficientShares) {
		t.Fatalf("holding replays cleanly (%v); the test no longer covers what it claims", err)
	}
	report, err := reopened.RealizedReport(user.ID, nil, nil)
	if err != nil {
		t.Fatalf("RealizedReport: %v", err)
	}
	if len(report) != 1 || report[0].UnstampedEntries != 1 {
		t.Errorf("got %+v, want one currency with one unstamped sell", report)
	}
}
