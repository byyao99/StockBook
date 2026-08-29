package handlers_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"stockbook/internal/db"
	"stockbook/internal/models"
)

// tradedOn returns noon UTC on a fixed day, matching how the trade form stores
// a chosen date: mid-day, so which calendar day a trade belongs to does not
// depend on the reader's time zone.
func tradedOn(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
}

func TestRealizedReportEndpoint(t *testing.T) {
	e := setup(t)
	token := e.token(t, "reporter", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	buy := tradePayload(inst.ID, models.SideBuy, 100, 7000, tradedOn(2025, time.January, 5))
	if rec := e.do(t, http.MethodPost, "/api/v1/transactions", buy, token); rec.Code != http.StatusCreated {
		t.Fatalf("buy: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	// The closing day of the reporting period: the entry a bound read as
	// midnight would drop.
	sell := tradePayload(inst.ID, models.SideSell, 50, 9500, tradedOn(2025, time.December, 31))
	rec := e.do(t, http.MethodPost, "/api/v1/transactions", sell, token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("sell: got %d (body: %s)", rec.Code, rec.Body.String())
	}

	// The sale carries its own result, so the ledger can be read entry by entry
	// without consulting the position.
	var created models.Transaction
	decodeData(t, rec, &created)
	if created.RealizedPL == nil || *created.RealizedPL != 125000 {
		t.Errorf("created sell reports realized %v, want 125000", created.RealizedPL)
	}

	var report []db.RealizedSummary
	rec = e.do(t, http.MethodGet,
		"/api/v1/reports/realized?from=2025-01-01&to=2025-12-31", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("report: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	decodeData(t, rec, &report)

	if len(report) != 1 {
		t.Fatalf("got %d currencies, want 1: %+v", len(report), report)
	}
	if report[0].RealizedPL != 125000 {
		t.Errorf("realized %d, want 125000", report[0].RealizedPL)
	}
	if report[0].Sells != 1 || len(report[0].Instruments) != 1 {
		t.Errorf("unexpected shape: %+v", report[0])
	}

	// A period that ends before the sale must not contain it.
	rec = e.do(t, http.MethodGet,
		"/api/v1/reports/realized?from=2024-01-01&to=2024-12-31", nil, token)
	decodeData(t, rec, &report)
	if len(report) != 0 {
		t.Errorf("2024 reports %+v, want nothing", report)
	}
}

// A dividend goes in through the ordinary ledger endpoint — it is an entry, not
// a separate resource — and comes back out of the report as income kept apart
// from the trading result.
func TestDividendIsRecordedAndReportedSeparately(t *testing.T) {
	e := setup(t)
	token := e.token(t, "incomer", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	buy := tradePayload(inst.ID, models.SideBuy, 1000, 70000, tradedOn(2025, time.January, 5))
	if rec := e.do(t, http.MethodPost, "/api/v1/transactions", buy, token); rec.Code != http.StatusCreated {
		t.Fatalf("buy: got %d (body: %s)", rec.Code, rec.Body.String())
	}

	// 1000 shares at NT$5.00 each, less NT$106.00 of withholding.
	payout := tradePayload(inst.ID, models.SideDividend, 1000, 500, tradedOn(2025, time.July, 15))
	payout["fee"] = 10600
	rec := e.do(t, http.MethodPost, "/api/v1/transactions", payout, token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("dividend: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var created models.Transaction
	decodeData(t, rec, &created)
	if created.RealizedPL == nil || *created.RealizedPL != 489400 {
		t.Errorf("dividend realized %v, want 489400", created.RealizedPL)
	}

	var report []db.RealizedSummary
	rec = e.do(t, http.MethodGet, "/api/v1/reports/realized?from=2025-01-01&to=2025-12-31", nil, token)
	decodeData(t, rec, &report)
	if len(report) != 1 {
		t.Fatalf("got %d currencies, want 1: %+v", len(report), report)
	}
	if report[0].Dividends != 489400 || report[0].TradingPL != 0 {
		t.Errorf("got dividends %d and trading %d, want 489400 and 0",
			report[0].Dividends, report[0].TradingPL)
	}
	if report[0].Sells != 0 || report[0].DividendCount != 1 {
		t.Errorf("counts: %d sells, %d dividends", report[0].Sells, report[0].DividendCount)
	}

	// The holding itself is untouched: a dividend is income, not a refund of
	// what the shares cost.
	var positions []map[string]any
	rec = e.do(t, http.MethodGet, "/api/v1/positions", nil, token)
	decodeData(t, rec, &positions)
	if len(positions) != 1 {
		t.Fatalf("got %d positions, want 1", len(positions))
	}
	if positions[0]["cost_basis"] != float64(70000000) {
		t.Errorf("cost basis %v, want 70000000", positions[0]["cost_basis"])
	}
	if positions[0]["quantity"] != float64(1000) {
		t.Errorf("quantity %v, want 1000", positions[0]["quantity"])
	}
	if positions[0]["realized_pl"] != float64(489400) {
		t.Errorf("position realized %v, want 489400", positions[0]["realized_pl"])
	}
}

func TestLedgerRejectsAnUnknownSide(t *testing.T) {
	e := setup(t)
	token := e.token(t, "typo", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	payload := tradePayload(inst.ID, "split", 100, 500, tradedOn(2025, time.July, 15))
	rec := e.do(t, http.MethodPost, "/api/v1/transactions", payload, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
	// The message names the alternatives rather than only refusing.
	if !strings.Contains(rec.Body.String(), "dividend") {
		t.Errorf("error does not name the valid sides: %s", rec.Body.String())
	}
}

func TestRealizedReportRequiresAuth(t *testing.T) {
	e := setup(t)
	if rec := e.do(t, http.MethodGet, "/api/v1/reports/realized", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
}

// An admin's powers cover the shared master data, not other people's books, and
// a report is just another way of reading one.
func TestRealizedReportDoesNotLeakAnotherBook(t *testing.T) {
	e := setup(t)
	owner := e.token(t, "owner", models.RoleUser)
	admin := e.token(t, "admin", models.RoleAdmin)
	inst := e.seedInstrument(t, "2330", nil)

	buy := tradePayload(inst.ID, models.SideBuy, 100, 7000, tradedOn(2025, time.March, 1))
	e.do(t, http.MethodPost, "/api/v1/transactions", buy, owner)
	sell := tradePayload(inst.ID, models.SideSell, 100, 9000, tradedOn(2025, time.March, 2))
	e.do(t, http.MethodPost, "/api/v1/transactions", sell, owner)

	var report []db.RealizedSummary
	rec := e.do(t, http.MethodGet, "/api/v1/reports/realized", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d (body: %s)", rec.Code, rec.Body.String())
	}
	decodeData(t, rec, &report)
	if len(report) != 0 {
		t.Errorf("admin sees another user's realized results: %+v", report)
	}
}

func TestRealizedReportRejectsAMalformedDate(t *testing.T) {
	e := setup(t)
	token := e.token(t, "reporter", models.RoleUser)
	rec := e.do(t, http.MethodGet, "/api/v1/reports/realized?from=last-tuesday", nil, token)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
}

// The returns endpoint answers over the caller's whole ledger, valuing what is
// still held at its current quote. The rate itself is pinned down against fixed
// dates in the db and models tests; here the point is that the route is wired,
// scoped to the caller, and reports the components its number was built from.
func TestReturnsReportEndpoint(t *testing.T) {
	e := setup(t)
	token := e.token(t, "investor", models.RoleUser)
	price := int64(12000)
	inst := e.seedInstrument(t, "2330", &price)

	buy := tradePayload(inst.ID, models.SideBuy, 100, 10000, tradedOn(2025, time.January, 2))
	if rec := e.do(t, http.MethodPost, "/api/v1/transactions", buy, token); rec.Code != http.StatusCreated {
		t.Fatalf("buy: got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var report []db.ReturnsSummary
	rec := e.do(t, http.MethodGet, "/api/v1/reports/returns", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("returns: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	decodeData(t, rec, &report)

	if len(report) != 1 {
		t.Fatalf("got %d currencies, want 1: %+v", len(report), report)
	}
	got := report[0]
	if got.Invested != 1000000 || got.EndingValue != 1200000 || got.NetGain != 200000 {
		t.Errorf("unexpected components: %+v", got)
	}
	if got.XIRRBps == nil {
		t.Fatalf("no rate computed: %q", got.Unavailable)
	}
	// The trade is in the past and the holding is up 20%, so the annualized rate
	// is positive whenever this test happens to run.
	if *got.XIRRBps <= 0 {
		t.Errorf("rate %d bps, want positive", *got.XIRRBps)
	}
	if got.OpenPositions != 1 || got.PricedPositions != 1 {
		t.Errorf("coverage %d priced of %d open, want 1 of 1",
			got.PricedPositions, got.OpenPositions)
	}
}

// A ledger is personal, and so is every number derived from one.
func TestReturnsReportDoesNotLeakAnotherBook(t *testing.T) {
	e := setup(t)
	owner := e.token(t, "owner", models.RoleUser)
	admin := e.token(t, "admin", models.RoleAdmin)
	price := int64(12000)
	inst := e.seedInstrument(t, "2330", &price)

	buy := tradePayload(inst.ID, models.SideBuy, 100, 10000, tradedOn(2025, time.January, 2))
	e.do(t, http.MethodPost, "/api/v1/transactions", buy, owner)

	var report []db.ReturnsSummary
	rec := e.do(t, http.MethodGet, "/api/v1/reports/returns", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d (body: %s)", rec.Code, rec.Body.String())
	}
	decodeData(t, rec, &report)
	if len(report) != 0 {
		t.Errorf("admin sees another user's return: %+v", report)
	}
}

func TestReturnsReportRequiresAuth(t *testing.T) {
	e := setup(t)
	if rec := e.do(t, http.MethodGet, "/api/v1/reports/returns", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
}

// What the sales in a period would be worth had the shares never been sold.
// The arithmetic is pinned down in the db tests; here the point is that the
// route is wired, honours the period, and reports the components behind the
// number so a reader can check it.
func TestHindsightReportEndpoint(t *testing.T) {
	e := setup(t)
	token := e.token(t, "seller", models.RoleUser)
	price := int64(20000)
	inst := e.seedInstrument(t, "2330", &price)

	buy := tradePayload(inst.ID, models.SideBuy, 100, 10000, tradedOn(2025, time.January, 5))
	if rec := e.do(t, http.MethodPost, "/api/v1/transactions", buy, token); rec.Code != http.StatusCreated {
		t.Fatalf("buy: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	sell := tradePayload(inst.ID, models.SideSell, 100, 12000, tradedOn(2025, time.December, 31))
	if rec := e.do(t, http.MethodPost, "/api/v1/transactions", sell, token); rec.Code != http.StatusCreated {
		t.Fatalf("sell: got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var report []db.HindsightSummary
	// The closing day of the range: a bound read as midnight would drop the sale.
	rec := e.do(t, http.MethodGet,
		"/api/v1/reports/hindsight?from=2025-01-01&to=2025-12-31", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("hindsight: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	decodeData(t, rec, &report)

	if len(report) != 1 {
		t.Fatalf("got %d currencies, want 1: %+v", len(report), report)
	}
	got := report[0]
	if got.Proceeds != 1200000 || got.ValueIfHeld != 2000000 || got.SellingGain != -800000 {
		t.Errorf("unexpected components: %+v", got)
	}
	if len(got.Instruments) != 1 || got.Instruments[0].Symbol != "2330" {
		t.Errorf("unexpected breakdown: %+v", got.Instruments)
	}
}

// A ledger is personal, and so is every number derived from one.
func TestHindsightReportDoesNotLeakAnotherBook(t *testing.T) {
	e := setup(t)
	owner := e.token(t, "owner", models.RoleUser)
	admin := e.token(t, "admin", models.RoleAdmin)
	price := int64(20000)
	inst := e.seedInstrument(t, "2330", &price)

	buy := tradePayload(inst.ID, models.SideBuy, 100, 10000, tradedOn(2025, time.March, 1))
	e.do(t, http.MethodPost, "/api/v1/transactions", buy, owner)
	sell := tradePayload(inst.ID, models.SideSell, 100, 12000, tradedOn(2025, time.March, 2))
	e.do(t, http.MethodPost, "/api/v1/transactions", sell, owner)

	var report []db.HindsightSummary
	rec := e.do(t, http.MethodGet, "/api/v1/reports/hindsight", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d (body: %s)", rec.Code, rec.Body.String())
	}
	decodeData(t, rec, &report)
	if len(report) != 0 {
		t.Errorf("admin sees another user's sell decisions: %+v", report)
	}
}

func TestHindsightReportRejectsAMalformedDate(t *testing.T) {
	e := setup(t)
	token := e.token(t, "seller", models.RoleUser)
	rec := e.do(t, http.MethodGet, "/api/v1/reports/hindsight?to=whenever", nil, token)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
}

// seedCloses stores a run of consecutive daily closes for an instrument,
// starting on the given day. The curve can only be drawn over sessions it has
// prices for, so every curve test has to put some there first.
func (e *testEnv) seedCloses(t *testing.T, instrumentID string, start time.Time, closes ...int64) {
	t.Helper()
	rows := make([]models.DailyClose, 0, len(closes))
	for i, close := range closes {
		rows = append(rows, models.DailyClose{
			Date:  start.AddDate(0, 0, i).UTC().Format(time.DateOnly),
			Close: close,
		})
	}
	if err := e.s.SaveDailyCloses(instrumentID, rows); err != nil {
		t.Fatalf("SaveDailyCloses: %v", err)
	}
}

// The curve reports one point per stored session, with the index chained from
// the daily returns and the market value beside it.
func TestCurveReportEndpoint(t *testing.T) {
	e := setup(t)
	token := e.token(t, "charter", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	buy := tradePayload(inst.ID, models.SideBuy, 100, 10000, tradedOn(2025, time.March, 3))
	if rec := e.do(t, http.MethodPost, "/api/v1/transactions", buy, token); rec.Code != http.StatusCreated {
		t.Fatalf("buy: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	// Three sessions rising 100.00 -> 110.00 -> 120.00.
	e.seedCloses(t, inst.ID, tradedOn(2025, time.March, 3), 10000, 11000, 12000)

	var report []db.CurrencyCurve
	rec := e.do(t, http.MethodGet,
		"/api/v1/reports/curve?from=2025-03-03&to=2025-03-05", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("curve: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	decodeData(t, rec, &report)

	if len(report) != 1 {
		t.Fatalf("got %d currencies, want 1: %+v", len(report), report)
	}
	got := report[0]
	if got.Unavailable != "" {
		t.Fatalf("curve reports itself unavailable: %q", got.Unavailable)
	}
	if len(got.Points) != 3 {
		t.Fatalf("got %d points, want 3: %+v", len(got.Points), got.Points)
	}

	// The first session anchors the index at its base and reports no return,
	// there being no previous close to measure one against.
	want := []db.CurvePoint{
		{Date: "2025-03-03", MarketValue: 1000000, NetInvested: 1000000, Index: 10000},
		{Date: "2025-03-04", MarketValue: 1100000, NetInvested: 1000000, Index: 11000},
		{Date: "2025-03-05", MarketValue: 1200000, NetInvested: 1000000, Index: 12000},
	}
	for i, w := range want {
		if got.Points[i] != w {
			t.Errorf("point %d = %+v, want %+v", i, got.Points[i], w)
		}
	}

	// +20% on the index over the window, and never below its own high-water mark.
	if got.TWRBps == nil || *got.TWRBps != 2000 {
		t.Errorf("twr %v, want 2000 bps", got.TWRBps)
	}
	if got.MaxDrawdownBps == nil || *got.MaxDrawdownBps != 0 {
		t.Errorf("drawdown %v, want 0", got.MaxDrawdownBps)
	}
	if got.Instruments != 1 || got.WithoutHistory != 0 {
		t.Errorf("instruments=%d without_history=%d, want 1 and 0", got.Instruments, got.WithoutHistory)
	}
}

// A holding with no stored prices leaves the curve entirely rather than being
// valued at zero, and the response has to say so in words — an empty chart that
// explains nothing reads as a book that went nowhere.
func TestCurveReportExplainsAnEmptyCurve(t *testing.T) {
	e := setup(t)
	token := e.token(t, "unsynced", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	buy := tradePayload(inst.ID, models.SideBuy, 100, 10000, tradedOn(2025, time.March, 3))
	if rec := e.do(t, http.MethodPost, "/api/v1/transactions", buy, token); rec.Code != http.StatusCreated {
		t.Fatalf("buy: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	// Deliberately no closes stored.

	var report []db.CurrencyCurve
	rec := e.do(t, http.MethodGet, "/api/v1/reports/curve", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("curve: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	decodeData(t, rec, &report)

	if len(report) != 1 {
		t.Fatalf("got %d currencies, want 1: %+v", len(report), report)
	}
	got := report[0]
	if len(got.Points) != 0 {
		t.Errorf("got %d points, want none: %+v", len(got.Points), got.Points)
	}
	if got.WithoutHistory != 1 {
		t.Errorf("without_history = %d, want 1", got.WithoutHistory)
	}
	if got.Unavailable == "" {
		t.Error("an empty curve must explain itself, so the UI can say what to do about it")
	}
}

// A ledger is personal, and so is the curve drawn from one.
func TestCurveReportDoesNotLeakAnotherBook(t *testing.T) {
	e := setup(t)
	owner := e.token(t, "owner", models.RoleUser)
	admin := e.token(t, "admin", models.RoleAdmin)
	inst := e.seedInstrument(t, "2330", nil)

	buy := tradePayload(inst.ID, models.SideBuy, 100, 10000, tradedOn(2025, time.March, 3))
	e.do(t, http.MethodPost, "/api/v1/transactions", buy, owner)
	e.seedCloses(t, inst.ID, tradedOn(2025, time.March, 3), 10000, 11000)

	var report []db.CurrencyCurve
	rec := e.do(t, http.MethodGet, "/api/v1/reports/curve", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d (body: %s)", rec.Code, rec.Body.String())
	}
	decodeData(t, rec, &report)
	if len(report) != 0 {
		t.Errorf("admin sees another user's curve: %+v", report)
	}
}

func TestCurveReportRejectsAMalformedDate(t *testing.T) {
	e := setup(t)
	token := e.token(t, "charter", models.RoleUser)
	rec := e.do(t, http.MethodGet, "/api/v1/reports/curve?from=whenever", nil, token)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", rec.Code)
	}
}
