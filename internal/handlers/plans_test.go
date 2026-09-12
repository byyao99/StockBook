package handlers_test

import (
	"net/http"
	"testing"
	"time"

	"stockbook/internal/db"
	"stockbook/internal/models"
)

func (e *testEnv) createPlan(t *testing.T, token, instrumentID, days string, amount int64, startedOn string) map[string]any {
	t.Helper()
	rec := e.do(t, http.MethodPost, "/api/v1/plans", map[string]any{
		"instrument_id": instrumentID,
		"days_of_month": days,
		"amount":        amount,
		"started_on":    startedOn,
	}, token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create plan: %d (body: %s)", rec.Code, rec.Body.String())
	}
	var plan map[string]any
	decodeData(t, rec, &plan)
	return plan
}

// The schedule is stored canonically however it was typed, so two plans running
// on the same days cannot read as different ones.
func TestPlanDaysAreStoredCanonically(t *testing.T) {
	e := setup(t)
	token := e.token(t, "saver", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	plan := e.createPlan(t, token, inst.ID, " 25,5 , 15,5 ", 300000, "2026-01-01")
	if plan["days_of_month"] != "5,15,25" {
		t.Errorf("days_of_month = %v, want \"5,15,25\"", plan["days_of_month"])
	}
}

func TestPlanRejectsAnImpossibleSchedule(t *testing.T) {
	e := setup(t)
	token := e.token(t, "saver", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	for _, days := range []string{"0", "32", "", "the fifth"} {
		rec := e.do(t, http.MethodPost, "/api/v1/plans", map[string]any{
			"instrument_id": inst.ID,
			"days_of_month": days,
			"amount":        300000,
			"started_on":    "2026-01-01",
		}, token)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("days %q: got %d, want 400", days, rec.Code)
		}
	}
}

// Instalments that came due with nothing against them are reported, with the
// best estimate available — and the estimate is absent, not zero, when no price
// is stored to make one from.
func TestPendingInstalmentsAreReported(t *testing.T) {
	e := setup(t)
	token := e.token(t, "saver", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)
	e.createPlan(t, token, inst.ID, "5", 300000, "2026-01-01")
	e.seedCloses(t, inst.ID, tradedOn(2026, time.January, 5), 50000)

	var pending []db.PendingPurchase
	rec := e.do(t, http.MethodGet, "/api/v1/plans/pending", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("pending: %d (body: %s)", rec.Code, rec.Body.String())
	}
	decodeData(t, rec, &pending)

	if len(pending) == 0 {
		t.Fatal("a plan running since January reported nothing due")
	}
	oldest := pending[len(pending)-1]
	if oldest.DueOn != "2026-01-05" {
		t.Errorf("oldest due %s, want 2026-01-05", oldest.DueOn)
	}
	// NT$3,000 at NT$500.00 is 6 shares exactly.
	if oldest.EstimatedShares == nil || *oldest.EstimatedShares != shares(6) {
		t.Errorf("estimated shares %v, want %d", oldest.EstimatedShares, shares(6))
	}
}

// Ending a plan keeps it: the purchases it prompted are still in the ledger,
// and it is the only record of why they are spaced as they are.
func TestEndingAPlanKeepsItButStopsItComingDue(t *testing.T) {
	e := setup(t)
	token := e.token(t, "saver", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)
	plan := e.createPlan(t, token, inst.ID, "5", 300000, "2026-01-01")

	rec := e.do(t, http.MethodPut,
		"/api/v1/plans/"+plan["id"].(string)+"/end?on=2026-02-01", nil, token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("end: %d (body: %s)", rec.Code, rec.Body.String())
	}

	var plans []db.PlanView
	listed := e.do(t, http.MethodGet, "/api/v1/plans", nil, token)
	decodeData(t, listed, &plans)
	if len(plans) != 1 {
		t.Fatalf("got %d plans after ending one, want it kept", len(plans))
	}
	if plans[0].EndedOn != "2026-02-01" {
		t.Errorf("ended_on = %q, want 2026-02-01", plans[0].EndedOn)
	}

	var pending []db.PendingPurchase
	p := e.do(t, http.MethodGet, "/api/v1/plans/pending", nil, token)
	decodeData(t, p, &pending)
	for _, item := range pending {
		if item.DueOn > "2026-02-01" {
			t.Errorf("instalment %s came due after the plan ended", item.DueOn)
		}
	}
}

// A plan is personal, like every number derived from a book.
func TestPlansDoNotLeakBetweenBooks(t *testing.T) {
	e := setup(t)
	mine := e.token(t, "mine", models.RoleUser)
	theirs := e.token(t, "theirs", models.RoleAdmin)
	inst := e.seedInstrument(t, "2330", nil)
	plan := e.createPlan(t, theirs, inst.ID, "5", 300000, "2026-01-01")

	var plans []db.PlanView
	rec := e.do(t, http.MethodGet, "/api/v1/plans", nil, mine)
	decodeData(t, rec, &plans)
	if len(plans) != 0 {
		t.Errorf("saw another book's plan: %+v", plans)
	}
	// Nor can it be ended or deleted by id alone.
	if rec := e.do(t, http.MethodDelete, "/api/v1/plans/"+plan["id"].(string), nil, mine); rec.Code != http.StatusNotFound {
		t.Errorf("deleting another book's plan: %d, want 404", rec.Code)
	}
}

func TestPlanEndpointsRequireAuth(t *testing.T) {
	e := setup(t)
	for _, path := range []string{"/api/v1/plans", "/api/v1/plans/pending"} {
		if rec := e.do(t, http.MethodGet, path, nil, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a token: %d, want 401", path, rec.Code)
		}
	}
}
