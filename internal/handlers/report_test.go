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
