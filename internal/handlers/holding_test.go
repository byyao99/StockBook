package handlers_test

import (
	"net/http"
	"testing"
	"time"

	"stockbook/internal/db"
	"stockbook/internal/models"
)

// A holding's own page: the position, what it banked, and its own rate.
func TestHoldingDetailEndpoint(t *testing.T) {
	e := setup(t)
	token := e.token(t, "holder", models.RoleUser)
	price := int64(90000)
	inst := e.seedInstrument(t, "2330", &price)

	buy := tradePayload(inst.ID, models.SideBuy, 100, 70000, tradedOn(2025, time.January, 6))
	if rec := e.do(t, http.MethodPost, "/api/v1/transactions", buy, token); rec.Code != http.StatusCreated {
		t.Fatalf("buy: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	div := map[string]any{
		"instrument_id": inst.ID,
		"side":          models.SideDividend,
		"quantity":      shares(100),
		"price":         1500,
		"traded_at":     tradedOn(2025, time.July, 10).Format(time.RFC3339),
	}
	if rec := e.do(t, http.MethodPost, "/api/v1/transactions", div, token); rec.Code != http.StatusCreated {
		t.Fatalf("dividend: got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var detail db.HoldingDetail
	rec := e.do(t, http.MethodGet, "/api/v1/positions/"+inst.ID, nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	decodeData(t, rec, &detail)

	if detail.Position.Symbol != "2330" {
		t.Errorf("symbol %q, want 2330", detail.Position.Symbol)
	}
	if detail.Invested != 100*70000 {
		t.Errorf("invested %d, want 7000000", detail.Invested)
	}
	if detail.Dividends != 100*1500 {
		t.Errorf("dividends %d, want 150000", detail.Dividends)
	}
	if detail.YieldOnCostBps == nil {
		t.Error("no yield on cost for a holding that has paid out")
	}
	if detail.XIRRBps == nil {
		t.Errorf("no rate: %q", detail.Unavailable)
	}
}

// The summary route must keep working beside the wildcard one, and an
// instrument the caller has never traded is 404 rather than an empty holding.
func TestHoldingDetailDoesNotShadowSummary(t *testing.T) {
	e := setup(t)
	token := e.token(t, "holder", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	if rec := e.do(t, http.MethodGet, "/api/v1/positions/summary", nil, token); rec.Code != http.StatusOK {
		t.Errorf("summary: got %d, want 200", rec.Code)
	}
	if rec := e.do(t, http.MethodGet, "/api/v1/positions/"+inst.ID, nil, token); rec.Code != http.StatusNotFound {
		t.Errorf("untraded instrument: got %d, want 404", rec.Code)
	}
}

// Another user's holding is not found, not forbidden, so ids cannot be probed.
func TestHoldingDetailIsPersonal(t *testing.T) {
	e := setup(t)
	holder := e.token(t, "holder", models.RoleUser)
	admin := e.token(t, "admin", models.RoleAdmin)
	price := int64(90000)
	inst := e.seedInstrument(t, "2330", &price)

	buy := tradePayload(inst.ID, models.SideBuy, 100, 70000, tradedOn(2025, time.January, 6))
	if rec := e.do(t, http.MethodPost, "/api/v1/transactions", buy, holder); rec.Code != http.StatusCreated {
		t.Fatalf("buy: got %d (body: %s)", rec.Code, rec.Body.String())
	}

	if rec := e.do(t, http.MethodGet, "/api/v1/positions/"+inst.ID, nil, admin); rec.Code != http.StatusNotFound {
		t.Errorf("admin reading another book: got %d, want 404", rec.Code)
	}
}

// The curve narrows to one holding when asked, which is what its page draws.
func TestCurveNarrowsToOneInstrument(t *testing.T) {
	e := setup(t)
	token := e.token(t, "holder", models.RoleUser)
	price := int64(90000)
	first := e.seedInstrument(t, "2330", &price)
	second := e.seedInstrument(t, "2317", &price)

	for _, inst := range []string{first.ID, second.ID} {
		buy := tradePayload(inst, models.SideBuy, 100, 70000, tradedOn(2025, time.January, 6))
		if rec := e.do(t, http.MethodPost, "/api/v1/transactions", buy, token); rec.Code != http.StatusCreated {
			t.Fatalf("buy: got %d (body: %s)", rec.Code, rec.Body.String())
		}
		closes := []models.DailyClose{
			{Date: "2025-01-06", Close: 70000},
			{Date: "2025-01-07", Close: 90000},
		}
		if err := e.s.SaveDailyCloses(inst, closes); err != nil {
			t.Fatalf("SaveDailyCloses: %v", err)
		}
	}

	var curves []db.CurrencyCurve
	rec := e.do(t, http.MethodGet, "/api/v1/reports/curve?instrument_id="+first.ID, nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("curve: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	decodeData(t, rec, &curves)
	if len(curves) != 1 {
		t.Fatalf("got %d curves, want 1", len(curves))
	}
	if curves[0].Instruments != 1 {
		t.Errorf("curve folds %d instruments, want 1", curves[0].Instruments)
	}

	// Unfiltered it is the whole book, which is what the reports page draws.
	rec = e.do(t, http.MethodGet, "/api/v1/reports/curve", nil, token)
	decodeData(t, rec, &curves)
	if len(curves) != 1 || curves[0].Instruments != 2 {
		t.Errorf("unfiltered curve folds %d instruments, want 2", curves[0].Instruments)
	}
}
