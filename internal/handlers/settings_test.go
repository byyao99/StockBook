package handlers_test

import (
	"net/http"
	"testing"

	"stockbook/internal/models"
)

// feeProfile mirrors the endpoint's payload for one profile.
type feeProfile struct {
	Key         string `json:"key"`
	RatePpm     int64  `json:"rate_ppm"`
	MinFee      int64  `json:"min_fee"`
	SellTaxPpm  int64  `json:"sell_tax_ppm"`
	DiscountBps int64  `json:"discount_bps"`
}

func (e *testEnv) feeProfiles(t *testing.T, token string) []feeProfile {
	t.Helper()
	rec := e.do(t, http.MethodGet, "/api/v1/settings/fees", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings/fees = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var got []feeProfile
	decodeData(t, rec, &got)
	return got
}

func profileByKey(t *testing.T, profiles []feeProfile, key models.FeeProfileKey) feeProfile {
	t.Helper()
	for _, p := range profiles {
		if p.Key == string(key) {
			return p
		}
	}
	t.Fatalf("no profile %q in %+v", key, profiles)
	return feeProfile{}
}

// A user who has never opened the settings page still gets a usable set. This
// is the whole point of merging defaults at read time: six blank rates would
// leave the trade form suggesting nothing, which is the state this feature
// exists to remove.
func TestFeeProfilesDefaultsForNewUser(t *testing.T) {
	e := setup(t)
	tok := e.token(t, "alice", models.RoleUser)

	got := e.feeProfiles(t, tok)
	if len(got) != len(models.FeeProfileKeys) {
		t.Fatalf("got %d profiles, want %d", len(got), len(models.FeeProfileKeys))
	}
	for i, key := range models.FeeProfileKeys {
		if got[i].Key != string(key) {
			t.Errorf("profile %d = %q, want %q (order must be canonical)", i, got[i].Key, key)
		}
	}
	tw := profileByKey(t, got, models.FeeTWStock)
	if tw.RatePpm != 1425 || tw.MinFee != 2000 || tw.SellTaxPpm != 3000 {
		t.Errorf("tw_stock default = %+v, want 1425/2000/3000", tw)
	}
	if etf := profileByKey(t, got, models.FeeTWETF); etf.SellTaxPpm != 1000 {
		t.Errorf("tw_etf sell tax = %d, want 1000 (an ETF pays a lower rate)", etf.SellTaxPpm)
	}
}

// Saving a subset changes only what it names. The settings form can send one
// row without silently resetting the other five.
func TestSaveFeeProfilesIsPartial(t *testing.T) {
	e := setup(t)
	tok := e.token(t, "alice", models.RoleUser)

	body := map[string]any{"profiles": []map[string]any{
		{"key": "tw_stock", "rate_ppm": 399, "min_fee": 100, "sell_tax_ppm": 3000},
	}}
	rec := e.do(t, http.MethodPut, "/api/v1/settings/fees", body, tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	got := e.feeProfiles(t, tok)
	if tw := profileByKey(t, got, models.FeeTWStock); tw.RatePpm != 399 || tw.MinFee != 100 {
		t.Errorf("tw_stock = %+v, want the saved 399/100", tw)
	}
	if us := profileByKey(t, got, models.FeeUSStock); us.RatePpm != 1500 {
		t.Errorf("us_stock = %d, want the untouched default 1500", us.RatePpm)
	}
}

// Saving twice updates in place rather than accumulating rows, which is what
// the composite primary key and the upsert are for.
func TestSaveFeeProfilesIsIdempotent(t *testing.T) {
	e := setup(t)
	tok := e.token(t, "alice", models.RoleUser)

	for _, rate := range []int64{399, 500} {
		body := map[string]any{"profiles": []map[string]any{
			{"key": "tw_stock", "rate_ppm": rate, "min_fee": 100, "sell_tax_ppm": 3000},
		}}
		if rec := e.do(t, http.MethodPut, "/api/v1/settings/fees", body, tok); rec.Code != http.StatusOK {
			t.Fatalf("PUT %d = %d, want 200 (body: %s)", rate, rec.Code, rec.Body.String())
		}
	}

	got := e.feeProfiles(t, tok)
	if len(got) != len(models.FeeProfileKeys) {
		t.Fatalf("got %d profiles, want %d — a second save duplicated a row",
			len(got), len(models.FeeProfileKeys))
	}
	if tw := profileByKey(t, got, models.FeeTWStock); tw.RatePpm != 500 {
		t.Errorf("tw_stock = %d, want the second save's 500", tw.RatePpm)
	}
}

// A stored zero is a claim, not a blank: a user whose broker charges no
// commission must get 0 back rather than being handed the default again. This
// is why the merge keys off row presence and never off the value.
func TestSavedZeroRateIsNotOverwrittenByDefault(t *testing.T) {
	e := setup(t)
	tok := e.token(t, "alice", models.RoleUser)

	body := map[string]any{"profiles": []map[string]any{
		{"key": "us_stock", "rate_ppm": 0, "min_fee": 0, "sell_tax_ppm": 0},
	}}
	if rec := e.do(t, http.MethodPut, "/api/v1/settings/fees", body, tok); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	if us := profileByKey(t, e.feeProfiles(t, tok), models.FeeUSStock); us.RatePpm != 0 || us.MinFee != 0 {
		t.Errorf("us_stock = %+v, want the saved zeros, not the 1500/1500 default", us)
	}
}

// Settings are personal. An admin has no more business reading someone's
// brokerage rates than reading their ledger.
func TestFeeProfilesAreScopedToTheCaller(t *testing.T) {
	e := setup(t)
	alice := e.token(t, "alice", models.RoleUser)
	admin := e.token(t, "root", models.RoleAdmin)

	body := map[string]any{"profiles": []map[string]any{
		{"key": "tw_stock", "rate_ppm": 399, "min_fee": 100, "sell_tax_ppm": 3000},
	}}
	if rec := e.do(t, http.MethodPut, "/api/v1/settings/fees", body, alice); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200", rec.Code)
	}

	if tw := profileByKey(t, e.feeProfiles(t, admin), models.FeeTWStock); tw.RatePpm != 1425 {
		t.Errorf("admin sees tw_stock = %d, want their own default 1425", tw.RatePpm)
	}
}

func TestSaveFeeProfilesRejectsBadInput(t *testing.T) {
	e := setup(t)
	tok := e.token(t, "alice", models.RoleUser)

	cases := []struct {
		name string
		body map[string]any
		why  string
	}{
		{
			name: "unknown key",
			body: map[string]any{"profiles": []map[string]any{
				{"key": "uk_stock", "rate_ppm": 1425, "min_fee": 2000, "sell_tax_ppm": 0}}},
			why: "a key nothing can ever select would silently never apply",
		},
		{
			name: "negative rate",
			body: map[string]any{"profiles": []map[string]any{
				{"key": "tw_stock", "rate_ppm": -1, "min_fee": 2000, "sell_tax_ppm": 0}}},
			why: "a negative fee would pay the user to trade",
		},
		{
			// The failure this guards is silent: 14250 charges ten times too
			// much on every later trade and nothing downstream looks wrong.
			name: "rate above the ceiling",
			body: map[string]any{"profiles": []map[string]any{
				{"key": "tw_stock", "rate_ppm": 100001, "min_fee": 2000, "sell_tax_ppm": 0}}},
			why: "no brokerage charges over 10%, so this is a misplaced decimal",
		},
		{
			name: "sell tax above the ceiling",
			body: map[string]any{"profiles": []map[string]any{
				{"key": "tw_stock", "rate_ppm": 1425, "min_fee": 2000, "sell_tax_ppm": 100001}}},
			why: "same misplaced decimal, other field",
		},
		{
			name: "same key twice",
			body: map[string]any{"profiles": []map[string]any{
				{"key": "tw_stock", "rate_ppm": 1425, "min_fee": 2000, "sell_tax_ppm": 3000},
				{"key": "tw_stock", "rate_ppm": 399, "min_fee": 100, "sell_tax_ppm": 3000}}},
			why: "the page would save something other than what it showed",
		},
		{
			name: "empty list",
			body: map[string]any{"profiles": []map[string]any{}},
			why:  "nothing to save is a malformed request, not a no-op",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.do(t, http.MethodPut, "/api/v1/settings/fees", tc.body, tok)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("got %d, want 400 — %s (body: %s)", rec.Code, tc.why, rec.Body.String())
			}
		})
	}

	// Nothing above should have been written.
	if tw := profileByKey(t, e.feeProfiles(t, tok), models.FeeTWStock); tw.RatePpm != 1425 {
		t.Errorf("tw_stock = %d after refused saves, want the untouched default 1425", tw.RatePpm)
	}
}

func TestFeeProfilesRequireAuth(t *testing.T) {
	e := setup(t)
	for _, m := range []string{http.MethodGet, http.MethodPut} {
		if rec := e.do(t, m, "/api/v1/settings/fees", nil, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token = %d, want 401", m, rec.Code)
		}
	}
}

// A broker's discount is stored apart from the list rate, because that is how
// the charge is quoted and because their product often is not a whole number of
// parts per million.
func TestSaveFeeProfilesKeepsTheDiscount(t *testing.T) {
	e := setup(t)
	tok := e.token(t, "alice", models.RoleUser)

	body := map[string]any{"profiles": []map[string]any{
		{"key": "tw_stock", "rate_ppm": 1425, "min_fee": 100, "sell_tax_ppm": 3000,
			"discount_bps": 2800},
	}}
	if rec := e.do(t, http.MethodPut, "/api/v1/settings/fees", body, tok); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	tw := profileByKey(t, e.feeProfiles(t, tok), models.FeeTWStock)
	if tw.RatePpm != 1425 {
		t.Errorf("rate = %d, want the list 1425 kept intact", tw.RatePpm)
	}
	if tw.DiscountBps != 2800 {
		t.Errorf("discount = %d, want 2800", tw.DiscountBps)
	}
}

// A client that predates the field pays list rather than being refused.
func TestOmittedDiscountMeansFullPrice(t *testing.T) {
	e := setup(t)
	tok := e.token(t, "alice", models.RoleUser)

	body := map[string]any{"profiles": []map[string]any{
		{"key": "tw_stock", "rate_ppm": 1425, "min_fee": 2000, "sell_tax_ppm": 3000},
	}}
	if rec := e.do(t, http.MethodPut, "/api/v1/settings/fees", body, tok); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if tw := profileByKey(t, e.feeProfiles(t, tok), models.FeeTWStock); tw.DiscountBps != 0 {
		t.Errorf("discount = %d, want 0 meaning no discount", tw.DiscountBps)
	}
}

func TestSaveFeeProfilesRejectsAnImpossibleDiscount(t *testing.T) {
	e := setup(t)
	tok := e.token(t, "alice", models.RoleUser)

	for _, discount := range []int64{-1, 10001} {
		body := map[string]any{"profiles": []map[string]any{
			{"key": "tw_stock", "rate_ppm": 1425, "min_fee": 2000, "sell_tax_ppm": 3000,
				"discount_bps": discount},
		}}
		rec := e.do(t, http.MethodPut, "/api/v1/settings/fees", body, tok)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("discount %d = %d, want 400 — above full price is not a discount "+
				"and below zero would pay the user to trade", discount, rec.Code)
		}
	}
}
