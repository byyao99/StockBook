package handlers_test

import (
	"net/http"
	"testing"
	"time"

	"stockbook/internal/db"
	"stockbook/internal/models"
)

// seedSplitEvent records a split against an instrument, as a history sync would.
func (e *testEnv) seedSplitEvent(t *testing.T, instrumentID, date string, ratioPpm int64) {
	t.Helper()
	err := e.s.SaveSplitEvents(instrumentID, []models.SplitEvent{{
		Date: date, RatioPpm: ratioPpm,
	}})
	if err != nil {
		t.Fatalf("SaveSplitEvents: %v", err)
	}
}

// The endpoint's whole job: a book holding shares across a split is told so,
// and can dismiss it once it has dealt with it.
func TestSplitWarningIsRaisedAndCanBeDismissed(t *testing.T) {
	e := setup(t)
	token := e.token(t, "holder", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	buy := tradePayload(inst.ID, models.SideBuy, 100, 70000, tradedOn(2025, time.January, 5))
	if rec := e.do(t, http.MethodPost, "/api/v1/transactions", buy, token); rec.Code != http.StatusCreated {
		t.Fatalf("buy: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	e.seedSplitEvent(t, inst.ID, "2025-06-30", 2*models.SplitRatioScale)

	var warnings []db.UnadjustedSplit
	rec := e.do(t, http.MethodGet, "/api/v1/reports/splits", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("splits: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	decodeData(t, rec, &warnings)
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %+v", len(warnings), warnings)
	}
	if warnings[0].Shares != shares(100) {
		t.Errorf("shares %d, want the 100 held before the split", warnings[0].Shares)
	}

	ack := map[string]any{"instrument_id": inst.ID, "date": "2025-06-30"}
	if rec := e.do(t, http.MethodPost, "/api/v1/reports/splits/ack", ack, token); rec.Code != http.StatusNoContent {
		t.Fatalf("ack: got %d (body: %s)", rec.Code, rec.Body.String())
	}

	rec = e.do(t, http.MethodGet, "/api/v1/reports/splits", nil, token)
	decodeData(t, rec, &warnings)
	if len(warnings) != 0 {
		t.Errorf("dismissed warning still raised: %+v", warnings)
	}

	// Still visible when asked for, so the undo has somewhere to live.
	rec = e.do(t, http.MethodGet, "/api/v1/reports/splits?include_acknowledged=true", nil, token)
	decodeData(t, rec, &warnings)
	if len(warnings) != 1 || !warnings[0].Acknowledged {
		t.Fatalf("want one acknowledged warning, got %+v", warnings)
	}

	if rec := e.do(t, http.MethodDelete, "/api/v1/reports/splits/ack", ack, token); rec.Code != http.StatusNoContent {
		t.Fatalf("undo: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	rec = e.do(t, http.MethodGet, "/api/v1/reports/splits", nil, token)
	decodeData(t, rec, &warnings)
	if len(warnings) != 1 {
		t.Errorf("want the warning back after undoing the dismissal, got %+v", warnings)
	}
}

// The split is shared market data but the warning is personal, like every other
// read over a ledger here.
func TestSplitWarningsAreScopedToTheCaller(t *testing.T) {
	e := setup(t)
	holder := e.token(t, "holder", models.RoleUser)
	admin := e.token(t, "admin", models.RoleAdmin)
	inst := e.seedInstrument(t, "2330", nil)

	buy := tradePayload(inst.ID, models.SideBuy, 100, 70000, tradedOn(2025, time.January, 5))
	if rec := e.do(t, http.MethodPost, "/api/v1/transactions", buy, holder); rec.Code != http.StatusCreated {
		t.Fatalf("buy: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	e.seedSplitEvent(t, inst.ID, "2025-06-30", 2*models.SplitRatioScale)

	var warnings []db.UnadjustedSplit
	rec := e.do(t, http.MethodGet, "/api/v1/reports/splits", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("splits: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	decodeData(t, rec, &warnings)
	if len(warnings) != 0 {
		t.Errorf("an admin saw another user's split warning: %+v", warnings)
	}

	if rec := e.do(t, http.MethodGet, "/api/v1/reports/splits", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}
}

// Dismissing a split nothing knows about is a 404, not a stored row that
// silences nothing.
func TestAcknowledgingAnUnknownSplitIsNotFound(t *testing.T) {
	e := setup(t)
	token := e.token(t, "holder", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	ack := map[string]any{"instrument_id": inst.ID, "date": "2025-06-30"}
	if rec := e.do(t, http.MethodPost, "/api/v1/reports/splits/ack", ack, token); rec.Code != http.StatusNotFound {
		t.Errorf("got %d, want 404", rec.Code)
	}
}
