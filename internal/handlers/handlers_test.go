package handlers_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"stockbook/internal/auth"
	"stockbook/internal/db"
	"stockbook/internal/handlers"
	"stockbook/internal/models"
	"stockbook/internal/router"
)

// testEnv bundles the wired router and its dependencies for a single test.
type testEnv struct {
	r  *gin.Engine
	s  *db.DB
	am *auth.Manager
}

// setup wires a router with no quote provider, which is what most tests want.
func setup(t *testing.T) *testEnv {
	return setupWithFetcher(t, nil)
}

// setupWithFetcher wires a router whose instrument handler uses the given quote
// provider. Tests pass a stub so the suite never reaches the network.
func setupWithFetcher(t *testing.T, fetcher handlers.QuoteFetcher) *testEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	am := auth.NewManager([]byte("test-secret"), time.Hour)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &testEnv{r: router.New(s, am, log, fetcher), s: s, am: am}
}

// token creates a user with the given role and returns a bearer token for it.
func (e *testEnv) token(t *testing.T, username string, role models.Role) string {
	t.Helper()
	hash, _ := auth.HashPassword("password123")
	user, err := e.s.CreateUser(models.User{
		ID: uuid.NewString(), Username: username, PasswordHash: hash, Role: role,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	tok, err := e.am.Issue(user.ID, user.Username, string(user.Role), user.TokenVersion)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return tok
}

// seedInstrument inserts an instrument directly, optionally with a quote.
func (e *testEnv) seedInstrument(t *testing.T, symbol string, lastPrice *int64) models.Instrument {
	t.Helper()
	item, err := e.s.CreateInstrument(models.Instrument{
		ID: uuid.NewString(), Symbol: symbol, Name: symbol + " Corp", Market: "TWSE",
	})
	if err != nil {
		t.Fatalf("CreateInstrument: %v", err)
	}
	if lastPrice != nil {
		item, err = e.s.UpdateInstrumentPrice(item.ID, lastPrice, nil)
		if err != nil {
			t.Fatalf("UpdateInstrumentPrice: %v", err)
		}
	}
	return item
}

// do issues an HTTP request against the router. body may be nil; token may be "".
func (e *testEnv) do(t *testing.T, method, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.r.ServeHTTP(rec, req)
	return rec
}

// decodeData unwraps the {"data": ...} envelope into v.
func decodeData(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	var body struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode envelope: %v (body: %s)", err, rec.Body.String())
	}
	if err := json.Unmarshal(body.Data, v); err != nil {
		t.Fatalf("decode data: %v (body: %s)", err, rec.Body.String())
	}
}

// tradePayload builds a transaction request body.
func tradePayload(instrumentID string, side models.TransactionSide, qty int, price int64, tradedAt time.Time) map[string]any {
	return map[string]any{
		"instrument_id": instrumentID,
		"side":          side,
		"quantity":      qty,
		"price":         price,
		"traded_at":     tradedAt.Format(time.RFC3339),
	}
}

func TestHealth(t *testing.T) {
	e := setup(t)
	rec := e.do(t, http.MethodGet, "/health", nil, "")
	if rec.Code != http.StatusOK {
		t.Errorf("got %d, want 200", rec.Code)
	}
}

func TestInstrumentWritesRequireAdmin(t *testing.T) {
	e := setup(t)
	payload := map[string]any{"symbol": "2330", "name": "TSMC", "market": "TWSE"}

	if rec := e.do(t, http.MethodPost, "/api/v1/instruments", payload, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}
	user := e.token(t, "plainuser", models.RoleUser)
	if rec := e.do(t, http.MethodPost, "/api/v1/instruments", payload, user); rec.Code != http.StatusForbidden {
		t.Errorf("user token: got %d, want 403", rec.Code)
	}
	admin := e.token(t, "admin", models.RoleAdmin)
	rec := e.do(t, http.MethodPost, "/api/v1/instruments", payload, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin token: got %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
}

// Reading the instrument list is something every signed-in user needs in order
// to enter a trade, but it is not public.
func TestInstrumentReadsRequireAuthOnly(t *testing.T) {
	e := setup(t)
	e.seedInstrument(t, "2330", nil)

	if rec := e.do(t, http.MethodGet, "/api/v1/instruments", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}
	user := e.token(t, "reader", models.RoleUser)
	if rec := e.do(t, http.MethodGet, "/api/v1/instruments", nil, user); rec.Code != http.StatusOK {
		t.Errorf("user token: got %d, want 200", rec.Code)
	}
}

func TestInstrumentSymbolIsNormalizedAndUnique(t *testing.T) {
	e := setup(t)
	admin := e.token(t, "admin", models.RoleAdmin)

	rec := e.do(t, http.MethodPost, "/api/v1/instruments",
		map[string]any{"symbol": "  aapl ", "name": "Apple", "market": "nasdaq"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	var created models.Instrument
	decodeData(t, rec, &created)
	if created.Symbol != "AAPL" {
		t.Errorf("symbol = %q, want AAPL", created.Symbol)
	}
	if created.Market != "NASDAQ" {
		t.Errorf("market = %q, want NASDAQ", created.Market)
	}

	// A differently-cased duplicate must collide rather than create a twin.
	rec = e.do(t, http.MethodPost, "/api/v1/instruments",
		map[string]any{"symbol": "aapl", "name": "Apple Inc", "market": "NASDAQ"}, admin)
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate: got %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}

	// An unknown market is a 400, not a silently-stored free-text value.
	rec = e.do(t, http.MethodPost, "/api/v1/instruments",
		map[string]any{"symbol": "VOD", "name": "Vodafone", "market": "LSE"}, admin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown market: got %d, want 400", rec.Code)
	}
}

func TestInstrumentWithTransactionsCannotBeDeleted(t *testing.T) {
	e := setup(t)
	admin := e.token(t, "admin", models.RoleAdmin)
	user := e.token(t, "trader", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	rec := e.do(t, http.MethodPost, "/api/v1/transactions",
		tradePayload(inst.ID, models.SideBuy, 10, 90000, time.Now().Add(-time.Hour)), user)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create transaction: got %d (body: %s)", rec.Code, rec.Body.String())
	}

	rec = e.do(t, http.MethodDelete, "/api/v1/instruments/"+inst.ID, nil, admin)
	if rec.Code != http.StatusConflict {
		t.Errorf("got %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
	// An untraded instrument deletes cleanly.
	spare := e.seedInstrument(t, "SPARE", nil)
	if rec := e.do(t, http.MethodDelete, "/api/v1/instruments/"+spare.ID, nil, admin); rec.Code != http.StatusNoContent {
		t.Errorf("untraded: got %d, want 204", rec.Code)
	}
}

func TestSetPriceRequiresAdminAndClearsCleanly(t *testing.T) {
	e := setup(t)
	admin := e.token(t, "admin", models.RoleAdmin)
	user := e.token(t, "trader", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	if rec := e.do(t, http.MethodPatch, "/api/v1/instruments/"+inst.ID+"/price",
		map[string]any{"last_price": 100000}, user); rec.Code != http.StatusForbidden {
		t.Errorf("user: got %d, want 403", rec.Code)
	}

	rec := e.do(t, http.MethodPatch, "/api/v1/instruments/"+inst.ID+"/price",
		map[string]any{"last_price": 100000}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin: got %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var priced models.Instrument
	decodeData(t, rec, &priced)
	if priced.LastPrice == nil || *priced.LastPrice != 100000 {
		t.Fatalf("last_price = %v, want 100000", priced.LastPrice)
	}
	if priced.PriceUpdatedAt == nil {
		t.Error("price_updated_at should be stamped alongside the quote")
	}

	// Clearing the quote must clear its timestamp too, so a nil price never
	// carries a misleading "as of" time.
	rec = e.do(t, http.MethodPatch, "/api/v1/instruments/"+inst.ID+"/price",
		map[string]any{"last_price": nil}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: got %d, want 200", rec.Code)
	}
	var cleared models.Instrument
	decodeData(t, rec, &cleared)
	if cleared.LastPrice != nil {
		t.Errorf("last_price = %v, want nil", cleared.LastPrice)
	}
	if cleared.PriceUpdatedAt != nil {
		t.Errorf("price_updated_at = %v, want nil", cleared.PriceUpdatedAt)
	}
}

// The cash movement is the server's arithmetic. A client that asserts its own
// total must be ignored, not believed.
func TestTransactionAmountsAreServerAuthoritative(t *testing.T) {
	e := setup(t)
	user := e.token(t, "trader", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	payload := tradePayload(inst.ID, models.SideBuy, 10, 90000, time.Now().Add(-time.Hour))
	payload["fee"] = 128
	payload["net_amount"] = 1 // a lie the server must not adopt
	payload["symbol"] = "FAKE"

	rec := e.do(t, http.MethodPost, "/api/v1/transactions", payload, user)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	var created models.Transaction
	decodeData(t, rec, &created)
	if want := int64(10*90000 + 128); created.NetAmount != want {
		t.Errorf("net_amount = %d, want %d", created.NetAmount, want)
	}
	if created.Symbol != "2330" {
		t.Errorf("symbol = %q, want the instrument's own 2330", created.Symbol)
	}
}

func TestTransactionRejectsFutureTrades(t *testing.T) {
	e := setup(t)
	user := e.token(t, "trader", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	rec := e.do(t, http.MethodPost, "/api/v1/transactions",
		tradePayload(inst.ID, models.SideBuy, 10, 90000, time.Now().Add(24*time.Hour)), user)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestTransactionValidatesSideAndQuantity(t *testing.T) {
	e := setup(t)
	user := e.token(t, "trader", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)
	past := time.Now().Add(-time.Hour)

	bad := tradePayload(inst.ID, "short", 10, 90000, past)
	if rec := e.do(t, http.MethodPost, "/api/v1/transactions", bad, user); rec.Code != http.StatusBadRequest {
		t.Errorf("bad side: got %d, want 400", rec.Code)
	}
	zero := tradePayload(inst.ID, models.SideBuy, 0, 90000, past)
	if rec := e.do(t, http.MethodPost, "/api/v1/transactions", zero, user); rec.Code != http.StatusBadRequest {
		t.Errorf("zero quantity: got %d, want 400", rec.Code)
	}
}

// A ledger is strictly personal. Another user's entry must read as absent, not
// as forbidden, so IDs cannot be probed for existence.
func TestAnotherUsersTransactionIsNotFound(t *testing.T) {
	e := setup(t)
	alice := e.token(t, "alice", models.RoleUser)
	bob := e.token(t, "bob", models.RoleUser)
	admin := e.token(t, "admin", models.RoleAdmin)
	inst := e.seedInstrument(t, "2330", nil)

	rec := e.do(t, http.MethodPost, "/api/v1/transactions",
		tradePayload(inst.ID, models.SideBuy, 10, 90000, time.Now().Add(-time.Hour)), alice)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var created models.Transaction
	decodeData(t, rec, &created)

	for name, tok := range map[string]string{"another user": bob, "an admin": admin} {
		if rec := e.do(t, http.MethodGet, "/api/v1/transactions/"+created.ID, nil, tok); rec.Code != http.StatusNotFound {
			t.Errorf("%s reading it: got %d, want 404", name, rec.Code)
		}
		if rec := e.do(t, http.MethodDelete, "/api/v1/transactions/"+created.ID, nil, tok); rec.Code != http.StatusNotFound {
			t.Errorf("%s deleting it: got %d, want 404", name, rec.Code)
		}
	}
	// The owner still sees it, and it survived the attempts above.
	if rec := e.do(t, http.MethodGet, "/api/v1/transactions/"+created.ID, nil, alice); rec.Code != http.StatusOK {
		t.Errorf("owner: got %d, want 200", rec.Code)
	}
}

// An admin manages the shared master data and accounts, not other people's
// books — their ledger and position lists show only their own rows.
func TestAdminSeesOnlyTheirOwnBook(t *testing.T) {
	e := setup(t)
	alice := e.token(t, "alice", models.RoleUser)
	admin := e.token(t, "admin", models.RoleAdmin)
	inst := e.seedInstrument(t, "2330", nil)

	rec := e.do(t, http.MethodPost, "/api/v1/transactions",
		tradePayload(inst.ID, models.SideBuy, 10, 90000, time.Now().Add(-time.Hour)), alice)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d", rec.Code)
	}

	rec = e.do(t, http.MethodGet, "/api/v1/transactions", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list: got %d", rec.Code)
	}
	var ledger []models.Transaction
	decodeData(t, rec, &ledger)
	if len(ledger) != 0 {
		t.Errorf("admin sees %d of someone else's entries, want 0", len(ledger))
	}

	rec = e.do(t, http.MethodGet, "/api/v1/positions", nil, admin)
	var positions []db.PositionView
	decodeData(t, rec, &positions)
	if len(positions) != 0 {
		t.Errorf("admin sees %d of someone else's positions, want 0", len(positions))
	}
}

// An unpriced instrument must report an unknown market value, not a zero one.
func TestUnpricedPositionReportsNullValuation(t *testing.T) {
	e := setup(t)
	user := e.token(t, "trader", models.RoleUser)
	priced := int64(100000)
	withQuote := e.seedInstrument(t, "2330", &priced)
	withoutQuote := e.seedInstrument(t, "2317", nil)
	past := time.Now().Add(-time.Hour)

	for _, inst := range []models.Instrument{withQuote, withoutQuote} {
		rec := e.do(t, http.MethodPost, "/api/v1/transactions",
			tradePayload(inst.ID, models.SideBuy, 10, 90000, past), user)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create for %s: got %d (body: %s)", inst.Symbol, rec.Code, rec.Body.String())
		}
	}

	rec := e.do(t, http.MethodGet, "/api/v1/positions", nil, user)
	var positions []db.PositionView
	decodeData(t, rec, &positions)
	if len(positions) != 2 {
		t.Fatalf("got %d positions, want 2", len(positions))
	}
	for _, p := range positions {
		switch p.Symbol {
		case "2330":
			if p.MarketValue == nil || *p.MarketValue != 1000000 {
				t.Errorf("2330 market_value = %v, want 1000000", p.MarketValue)
			}
			if p.UnrealizedPL == nil || *p.UnrealizedPL != 100000 {
				t.Errorf("2330 unrealized_pl = %v, want 100000", p.UnrealizedPL)
			}
		case "2317":
			if p.MarketValue != nil {
				t.Errorf("2317 market_value = %v, want null (no quote)", *p.MarketValue)
			}
			if p.UnrealizedPL != nil {
				t.Errorf("2317 unrealized_pl = %v, want null (no quote)", *p.UnrealizedPL)
			}
		}
	}

	// The summary must say how much of the book its totals cover rather than
	// folding the unpriced holding in as zero.
	rec = e.do(t, http.MethodGet, "/api/v1/positions/summary", nil, user)
	var summaries []db.CurrencySummary
	decodeData(t, rec, &summaries)
	if len(summaries) != 1 {
		t.Fatalf("got %d currency summaries, want 1", len(summaries))
	}
	summary := summaries[0]
	if summary.OpenPositions != 2 || summary.PricedPositions != 1 {
		t.Errorf("open=%d priced=%d, want 2/1", summary.OpenPositions, summary.PricedPositions)
	}
	if summary.TotalCostBasis != 1800000 {
		t.Errorf("total_cost_basis = %d, want 1800000", summary.TotalCostBasis)
	}
	if summary.PricedCostBasis != 900000 {
		t.Errorf("priced_cost_basis = %d, want 900000", summary.PricedCostBasis)
	}
	// The identity the summary promises must hold.
	if summary.TotalUnrealizedPL != summary.TotalMarketValue-summary.PricedCostBasis {
		t.Errorf("unrealized %d != market %d - priced cost %d",
			summary.TotalUnrealizedPL, summary.TotalMarketValue, summary.PricedCostBasis)
	}
}

// A fully-exited holding leaves the list but keeps counting toward realized P/L.
func TestClosedPositionsHideButStillCount(t *testing.T) {
	e := setup(t)
	user := e.token(t, "trader", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)
	past := time.Now().Add(-2 * time.Hour)

	e.do(t, http.MethodPost, "/api/v1/transactions", tradePayload(inst.ID, models.SideBuy, 10, 90000, past), user)
	e.do(t, http.MethodPost, "/api/v1/transactions", tradePayload(inst.ID, models.SideSell, 10, 95000, past.Add(time.Hour)), user)

	rec := e.do(t, http.MethodGet, "/api/v1/positions", nil, user)
	var open []db.PositionView
	decodeData(t, rec, &open)
	if len(open) != 0 {
		t.Errorf("closed position still listed: %+v", open)
	}

	rec = e.do(t, http.MethodGet, "/api/v1/positions?include_closed=true", nil, user)
	var all []db.PositionView
	decodeData(t, rec, &all)
	if len(all) != 1 {
		t.Fatalf("got %d positions with include_closed, want 1", len(all))
	}

	rec = e.do(t, http.MethodGet, "/api/v1/positions/summary", nil, user)
	var summaries []db.CurrencySummary
	decodeData(t, rec, &summaries)
	if len(summaries) != 1 {
		t.Fatalf("got %d currency summaries, want 1", len(summaries))
	}
	summary := summaries[0]
	if summary.OpenPositions != 0 {
		t.Errorf("open_positions = %d, want 0", summary.OpenPositions)
	}
	if summary.TotalRealizedPL != 50000 {
		t.Errorf("total_realized_pl = %d, want 50000", summary.TotalRealizedPL)
	}
}

// Selling more than is held is a 409 through the API, with a message naming the
// instrument so the user can act on it.
func TestOversellThroughAPIIsConflict(t *testing.T) {
	e := setup(t)
	user := e.token(t, "trader", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)
	past := time.Now().Add(-2 * time.Hour)

	e.do(t, http.MethodPost, "/api/v1/transactions", tradePayload(inst.ID, models.SideBuy, 10, 90000, past), user)

	rec := e.do(t, http.MethodPost, "/api/v1/transactions",
		tradePayload(inst.ID, models.SideSell, 11, 95000, past.Add(time.Hour)), user)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestListSortIsRestrictedToTheAllowlist(t *testing.T) {
	e := setup(t)
	admin := e.token(t, "admin", models.RoleAdmin)
	e.seedInstrument(t, "BBB", nil)
	e.seedInstrument(t, "AAA", nil)

	// An unknown (and hostile) sort key must fall back to the default order
	// rather than reaching SQL.
	rec := e.do(t, http.MethodGet, "/api/v1/instruments?sort=name;DROP+TABLE+instruments--", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var items []models.Instrument
	decodeData(t, rec, &items)
	if len(items) != 2 || items[0].Symbol != "AAA" {
		t.Errorf("expected default symbol order, got %+v", items)
	}
}
