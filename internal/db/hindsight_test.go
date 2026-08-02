package db

import (
	"testing"

	"github.com/google/uuid"

	"stockbook/internal/models"
)

// onlyHindsight returns the single currency summary a test expects.
func onlyHindsight(t *testing.T, summaries []HindsightSummary) HindsightSummary {
	t.Helper()
	if len(summaries) != 1 {
		t.Fatalf("got %d currency summaries, want 1: %+v", len(summaries), summaries)
	}
	return summaries[0]
}

func mustHindsight(t *testing.T, s *DB, userID string) []HindsightSummary {
	t.Helper()
	summaries, err := s.HindsightReport(userID, nil, nil)
	if err != nil {
		t.Fatalf("HindsightReport: %v", err)
	}
	return summaries
}

// The whole point: a sale made before a rise shows exactly what it cost, and the
// number is the sale's own proceeds against those same shares at today's quote.
func TestHindsightPricesASaleMadeTooEarly(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "early")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 100, 12_000, 0, 30})
	priceAt(t, s, inst.ID, 20_000) // doubled again since

	summary := onlyHindsight(t, mustHindsight(t, s, user.ID))
	if summary.Proceeds != 1_200_000 {
		t.Errorf("proceeds %d, want 1200000", summary.Proceeds)
	}
	if summary.ValueIfHeld != 2_000_000 {
		t.Errorf("value if held %d, want 2000000", summary.ValueIfHeld)
	}
	if summary.SellingGain != -800_000 {
		t.Errorf("selling gain %d, want -800000: the sale banked a profit and "+
			"still left 800000 on the table", summary.SellingGain)
	}
	if summary.SharesSold != 100 || summary.Sells != 1 {
		t.Errorf("unexpected counts: %+v", summary)
	}
}

// Selling ahead of a fall is the other half of the same question, and has to
// read as a win rather than as an absent number.
func TestHindsightCreditsASaleMadeBeforeAFall(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "lucky")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 100, 12_000, 0, 30})
	priceAt(t, s, inst.ID, 5_000)

	summary := onlyHindsight(t, mustHindsight(t, s, user.ID))
	if summary.SellingGain != 700_000 {
		t.Errorf("selling gain %d, want 700000", summary.SellingGain)
	}
}

// Selling fees come out of what the sale actually brought in, so they belong on
// the proceeds side of the comparison and nowhere else.
func TestHindsightMeasuresProceedsAfterFees(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "fees")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 100, 12_000, 4_500, 30})
	priceAt(t, s, inst.ID, 12_000) // price unchanged since the sale

	summary := onlyHindsight(t, mustHindsight(t, s, user.ID))
	if summary.Proceeds != 1_195_500 {
		t.Errorf("proceeds %d, want 1195500 (fee deducted)", summary.Proceeds)
	}
	// The price went nowhere, so the only difference left is the fee.
	if summary.SellingGain != -4_500 {
		t.Errorf("selling gain %d, want -4500: at an unchanged price, selling "+
			"cost exactly the fee", summary.SellingGain)
	}
}

// SellingGain is defined as the difference and must never drift from it,
// through every row and every total.
func TestHindsightGainReconcilesWithItsComponents(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "reconcile")
	a := seedInstrument(t, s, "2330")
	b := seedInstrument(t, s, "2317")

	mustRecord(t, s, user.ID, a.ID, entry{models.SideBuy, 300, 7_000, 0, 0})
	mustRecord(t, s, user.ID, a.ID, entry{models.SideSell, 100, 9_500, 120, 10})
	mustRecord(t, s, user.ID, a.ID, entry{models.SideSell, 50, 8_800, 90, 40})
	priceAt(t, s, a.ID, 11_000)
	mustRecord(t, s, user.ID, b.ID, entry{models.SideBuy, 200, 5_000, 0, 5})
	mustRecord(t, s, user.ID, b.ID, entry{models.SideSell, 200, 6_000, 300, 20})
	priceAt(t, s, b.ID, 4_000)

	summary := onlyHindsight(t, mustHindsight(t, s, user.ID))
	if got := summary.Proceeds - summary.ValueIfHeld; got != summary.SellingGain {
		t.Errorf("summary gain %d, but proceeds - value if held is %d",
			summary.SellingGain, got)
	}

	var rowGain, rowProceeds, rowValue int64
	var rowSells int
	for _, row := range summary.Instruments {
		if got := row.Proceeds - row.ValueIfHeld; got != row.SellingGain {
			t.Errorf("%s gain %d, but proceeds - value if held is %d",
				row.Symbol, row.SellingGain, got)
		}
		rowGain += row.SellingGain
		rowProceeds += row.Proceeds
		rowValue += row.ValueIfHeld
		rowSells += row.Sells
	}
	if rowGain != summary.SellingGain || rowProceeds != summary.Proceeds ||
		rowValue != summary.ValueIfHeld || rowSells != summary.Sells {
		t.Errorf("rows do not add up to the total: %+v", summary)
	}
}

// Worst first: the sales that cost the most are what the report is read for.
func TestHindsightOrdersWorstDecisionFirst(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "ordered")
	regret := seedInstrument(t, s, "2330")
	win := seedInstrument(t, s, "2317")

	mustRecord(t, s, user.ID, regret.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	mustRecord(t, s, user.ID, regret.ID, entry{models.SideSell, 100, 10_000, 0, 10})
	priceAt(t, s, regret.ID, 30_000)
	mustRecord(t, s, user.ID, win.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	mustRecord(t, s, user.ID, win.ID, entry{models.SideSell, 100, 10_000, 0, 10})
	priceAt(t, s, win.ID, 1_000)

	summary := onlyHindsight(t, mustHindsight(t, s, user.ID))
	if len(summary.Instruments) != 2 {
		t.Fatalf("got %d rows, want 2", len(summary.Instruments))
	}
	if summary.Instruments[0].Symbol != "2330" {
		t.Errorf("first row is %s, want the costliest sale (2330) at the top",
			summary.Instruments[0].Symbol)
	}
}

// A sale of an instrument with no quote has nothing to be compared against.
// Valuing those shares at zero would report every such sale as a masterstroke.
func TestHindsightExcludesSalesItCannotPrice(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "unquoted")
	priced := seedInstrument(t, s, "2330")
	unpriced := seedInstrument(t, s, "2454")

	mustRecord(t, s, user.ID, priced.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	mustRecord(t, s, user.ID, priced.ID, entry{models.SideSell, 100, 12_000, 0, 10})
	priceAt(t, s, priced.ID, 20_000)
	mustRecord(t, s, user.ID, unpriced.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	mustRecord(t, s, user.ID, unpriced.ID, entry{models.SideSell, 100, 12_000, 0, 10})

	summary := onlyHindsight(t, mustHindsight(t, s, user.ID))
	if summary.UnpricedSales != 1 {
		t.Errorf("unpriced sales %d, want 1", summary.UnpricedSales)
	}
	if summary.Sells != 1 || len(summary.Instruments) != 1 {
		t.Errorf("the unquoted sale should be counted, not folded in: %+v", summary)
	}
	if summary.SellingGain != -800_000 {
		t.Errorf("selling gain %d, want -800000 from the priced sale alone",
			summary.SellingGain)
	}
}

// Buying back is where "selling cost you this" stops being the whole story, so
// the shares held now are reported to make the re-entry visible.
func TestHindsightShowsWhatIsStillHeld(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "round-trip")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 10_000, 0, 0})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 100, 9_000, 0, 10})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 100, 8_000, 0, 20})
	priceAt(t, s, inst.ID, 20_000)

	summary := onlyHindsight(t, mustHindsight(t, s, user.ID))
	row := summary.Instruments[0]
	if row.SharesSold != 100 || row.SharesHeld != 100 {
		t.Errorf("sold %d and holds %d, want 100 and 100 — the re-entry has to "+
			"be visible next to the loss this row reports", row.SharesSold, row.SharesHeld)
	}
}

// Only sales are asked about: a buy has nothing to regret yet, and a dividend
// is earned by holding rather than by selling.
func TestHindsightIgnoresBuysAndDividends(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "holder")
	inst := seedInstrument(t, s, "0056")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 1000, 3_000, 0, 0})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideDividend, 1000, 150, 0, 180})
	priceAt(t, s, inst.ID, 4_000)

	if summaries := mustHindsight(t, s, user.ID); len(summaries) != 0 {
		t.Errorf("a book that never sold has nothing to report: %+v", summaries)
	}
}

// The period selects sales by when they were made; the price they are compared
// against is always today's.
func TestHindsightHonoursThePeriod(t *testing.T) {
	s := newTestDB(t)
	user := seedUser(t, s, "periods")
	inst := seedInstrument(t, s, "2330")

	mustRecord(t, s, user.ID, inst.ID, entry{models.SideBuy, 200, 10_000, 0, 0})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 100, 12_000, 0, 10})
	mustRecord(t, s, user.ID, inst.ID, entry{models.SideSell, 100, 15_000, 0, 100})
	priceAt(t, s, inst.ID, 20_000)

	from, to := day(50), day(150)
	summaries, err := s.HindsightReport(user.ID, &from, &to)
	if err != nil {
		t.Fatalf("HindsightReport: %v", err)
	}
	summary := onlyHindsight(t, summaries)
	if summary.Sells != 1 || summary.Proceeds != 1_500_000 {
		t.Errorf("the period should hold only the later sale: %+v", summary)
	}
	if summary.SellingGain != -500_000 {
		t.Errorf("selling gain %d, want -500000", summary.SellingGain)
	}
}

// Currencies are reported apart, like every other total here.
func TestHindsightSeparatesCurrencies(t *testing.T) {
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
	mustRecord(t, s, user.ID, twd.ID, entry{models.SideSell, 100, 12_000, 0, 10})
	priceAt(t, s, twd.ID, 20_000)
	mustRecord(t, s, user.ID, usd.ID, entry{models.SideBuy, 10, 20_000, 0, 0})
	mustRecord(t, s, user.ID, usd.ID, entry{models.SideSell, 10, 20_000, 0, 10})
	priceAt(t, s, usd.ID, 10_000)

	summaries := mustHindsight(t, s, user.ID)
	if len(summaries) != 2 {
		t.Fatalf("got %d summaries, want one per currency: %+v", len(summaries), summaries)
	}
	if summaries[0].Currency != models.CurrencyTWD || summaries[1].Currency != models.CurrencyUSD {
		t.Fatalf("not ordered by currency: %+v", summaries)
	}
	if summaries[0].SellingGain != -800_000 {
		t.Errorf("TWD gain %d, want -800000", summaries[0].SellingGain)
	}
	if summaries[1].SellingGain != 100_000 {
		t.Errorf("USD gain %d, want 100000", summaries[1].SellingGain)
	}
}
