package handlers_test

import (
	"net/http"
	"strings"
	"testing"

	"stockbook/internal/models"
)

type targetView struct {
	InstrumentID string `json:"instrument_id"`
	Symbol       string `json:"symbol"`
	Currency     string `json:"currency"`
	WeightBps    int64  `json:"weight_bps"`
}

func (e *testEnv) targets(t *testing.T, token string) []targetView {
	t.Helper()
	rec := e.do(t, http.MethodGet, "/api/v1/settings/targets", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings/targets = %d (body: %s)", rec.Code, rec.Body.String())
	}
	var got []targetView
	decodeData(t, rec, &got)
	return got
}

func TestSaveTargetsRoundTrips(t *testing.T) {
	e := setup(t)
	tok := e.token(t, "alice", models.RoleUser)
	voo := e.seedInstrumentIn(t, "VOO", "NYSEARCA", models.CurrencyUSD)
	pltr := e.seedInstrumentIn(t, "PLTR", "NASDAQ", models.CurrencyUSD)

	if got := e.targets(t, tok); len(got) != 0 {
		t.Fatalf("a new user has %d targets, want none", len(got))
	}
	body := map[string]any{"targets": []map[string]any{
		{"instrument_id": voo.ID, "weight_bps": 7500},
		{"instrument_id": pltr.ID, "weight_bps": 2500},
	}}
	rec := e.do(t, http.MethodPut, "/api/v1/settings/targets", body, tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d (body: %s)", rec.Code, rec.Body.String())
	}
	got := e.targets(t, tok)
	if len(got) != 2 || got[0].Symbol != "VOO" || got[0].WeightBps != 7500 || got[0].Currency != "USD" {
		t.Fatalf("targets = %+v, want VOO 75%% first", got)
	}
}

// A malformed set is the caller's mistake, and the message says what to fix.
func TestSaveTargetsRejectsASetNotAddingUpTo100(t *testing.T) {
	e := setup(t)
	tok := e.token(t, "alice", models.RoleUser)
	voo := e.seedInstrumentIn(t, "VOO", "NYSEARCA", models.CurrencyUSD)

	body := map[string]any{"targets": []map[string]any{{"instrument_id": voo.ID, "weight_bps": 9000}}}
	rec := e.do(t, http.MethodPut, "/api/v1/settings/targets", body, tok)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "USD targets add up to 90.00%") {
		t.Fatalf("PUT = %d %s, want 400 naming the USD sum", rec.Code, rec.Body.String())
	}
}

func TestTargetsAreScopedToTheCaller(t *testing.T) {
	e := setup(t)
	alice := e.token(t, "alice", models.RoleUser)
	bob := e.token(t, "bob", models.RoleAdmin)
	voo := e.seedInstrumentIn(t, "VOO", "NYSEARCA", models.CurrencyUSD)

	body := map[string]any{"targets": []map[string]any{{"instrument_id": voo.ID, "weight_bps": 10000}}}
	if rec := e.do(t, http.MethodPut, "/api/v1/settings/targets", body, alice); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d", rec.Code)
	}
	// An admin sees no more of another user's plan than anybody else.
	if got := e.targets(t, bob); len(got) != 0 {
		t.Fatalf("bob sees %d of alice's targets", len(got))
	}
}

func TestTargetsRequireAuth(t *testing.T) {
	e := setup(t)
	if rec := e.do(t, http.MethodGet, "/api/v1/settings/targets", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET without a token = %d, want 401", rec.Code)
	}
}
