package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"stockbook/internal/db"
	"stockbook/internal/models"
	"stockbook/internal/quotes"
)

// stubFetcher stands in for the quote provider so no test reaches the network.
// Quotes maps a Yahoo ticker to what the provider would say about it; anything
// missing fails the way a delisted symbol does.
type stubFetcher struct {
	quotes    map[string]quotes.Quote
	fail      map[string]error
	calls     []string
	results   []quotes.SearchResult
	searchErr error
	// history is served by ticker for the sync tests; historyErr overrides it.
	// A ticker with neither staged comes back as an empty series, which is what
	// the provider returns for a window containing no sessions.
	history    map[string]quotes.History
	historyErr map[string]error
	// historyCalls records the window each ticker was asked for, so the tests
	// can pin down that a top-up requests a narrow range rather than the lot.
	historyCalls []historyCall
	// permissive answers for any ticker whose market this system models,
	// deriving the exchange from the suffix. Creating an instrument now requires
	// a quotable ticker, so tests that add one through the API need a provider
	// that knows it without every test enumerating symbols.
	permissive bool
}

// Search returns whatever the test staged, so the pick-and-add flow can be
// exercised without reaching the provider.
func (s *stubFetcher) Search(_ context.Context, _ string) ([]quotes.SearchResult, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	return s.results, nil
}

// historyCall is one recorded request for a range of daily closes.
type historyCall struct {
	ticker   string
	from, to string
}

// History serves whatever the test staged, recording the window it was asked
// for. No test ever reaches the network.
func (s *stubFetcher) History(_ context.Context, ticker string, from, to time.Time) (quotes.History, error) {
	s.historyCalls = append(s.historyCalls, historyCall{
		ticker: ticker,
		from:   from.UTC().Format(time.DateOnly),
		to:     to.UTC().Format(time.DateOnly),
	})
	if err, ok := s.historyErr[ticker]; ok {
		return quotes.History{}, err
	}
	if h, ok := s.history[ticker]; ok {
		return h, nil
	}
	return quotes.History{Currency: synthesizeQuote(ticker).Currency}, nil
}

func (s *stubFetcher) Fetch(_ context.Context, ticker string) (quotes.Quote, error) {
	s.calls = append(s.calls, ticker)
	if err, ok := s.fail[ticker]; ok {
		return quotes.Quote{}, err
	}
	if q, ok := s.quotes[ticker]; ok {
		return q, nil
	}
	if s.permissive {
		return synthesizeQuote(ticker), nil
	}
	// Wrapped the way the real provider wraps it, so callers that distinguish
	// "unknown symbol" from "provider is unwell" are exercised faithfully.
	return quotes.Quote{}, fmt.Errorf("%w for %s: No data found, symbol may be delisted",
		quotes.ErrNoQuote, ticker)
}

// synthesizeQuote invents a plausible answer for a ticker, with the exchange
// and currency that its suffix implies so the create path's cross-check passes.
func synthesizeQuote(ticker string) quotes.Quote {
	exchange, currency := "NMS", models.CurrencyUSD
	switch {
	case strings.HasSuffix(ticker, ".TWO"):
		exchange, currency = "TWO", models.CurrencyTWD
	case strings.HasSuffix(ticker, ".TW"):
		exchange, currency = "TAI", models.CurrencyTWD
	}
	return quotes.Quote{
		Price:    10000,
		Currency: currency,
		AsOf:     time.Now(),
		Name:     ticker + " Corp",
		Exchange: exchange,
		Type:     "EQUITY",
	}
}

// refreshResponse mirrors the endpoint's payload.
type refreshResponse struct {
	Updated int `json:"updated"`
	Failed  int `json:"failed"`
	Fresh   int `json:"fresh"`
	Results []struct {
		Symbol    string `json:"symbol"`
		Status    string `json:"status"`
		LastPrice *int64 `json:"last_price"`
		Error     string `json:"error"`
	} `json:"results"`
}

// setupWithQuotes wires a router whose instrument handler uses the stub.
func (e *testEnv) refresh(t *testing.T, token string) (*refreshResponse, int) {
	t.Helper()
	rec := e.do(t, http.MethodPost, "/api/v1/instruments/refresh-quotes", nil, token)
	if rec.Code != http.StatusOK {
		return nil, rec.Code
	}
	var body struct {
		Data refreshResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, rec.Body.String())
	}
	return &body.Data, rec.Code
}

// Refreshing quotes needs a session but not a privileged one: a holdings page
// is useless without current prices, so a plain user must be able to fix a
// stale book rather than waiting on an admin.
func TestRefreshQuotesNeedsAuthButNotAdmin(t *testing.T) {
	e := setupWithFetcher(t, &stubFetcher{})

	if rec := e.do(t, http.MethodPost, "/api/v1/instruments/refresh-quotes", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}
	user := e.token(t, "trader", models.RoleUser)
	if rec := e.do(t, http.MethodPost, "/api/v1/instruments/refresh-quotes", nil, user); rec.Code != http.StatusOK {
		t.Errorf("user token: got %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
}

// Every fetch is an outbound call to a third party, so a quote that is already
// current must not be re-requested. Pressing refresh twice costs one round of
// traffic, not two.
func TestRefreshSkipsQuotesThatAreStillCurrent(t *testing.T) {
	fetcher := &stubFetcher{
		quotes: map[string]quotes.Quote{
			"TSLA": {Price: 31303, Currency: models.CurrencyUSD, AsOf: time.Now()},
		},
	}
	e := setupWithFetcher(t, fetcher)
	user := e.token(t, "trader", models.RoleUser)
	e.seedInstrumentIn(t, "TSLA", "NASDAQ", models.CurrencyUSD)

	first, _ := e.refresh(t, user)
	if first.Updated != 1 || first.Fresh != 0 {
		t.Fatalf("first run: updated=%d fresh=%d, want 1/0", first.Updated, first.Fresh)
	}

	second, _ := e.refresh(t, user)
	if second.Updated != 0 || second.Fresh != 1 {
		t.Errorf("second run: updated=%d fresh=%d, want 0/1", second.Updated, second.Fresh)
	}
	// A skipped-as-current instrument is counted, not listed: on a repeat press
	// it would be the whole list and none of it is actionable.
	if len(second.Results) != 0 {
		t.Errorf("second run listed %d results, want none", len(second.Results))
	}
	if len(fetcher.calls) != 1 {
		t.Errorf("provider was called %d times, want 1", len(fetcher.calls))
	}
}

// An instrument with no quote at all is never "fresh" — that is precisely the
// case a refresh exists to fix.
func TestRefreshAlwaysFetchesAnUnquotedInstrument(t *testing.T) {
	fetcher := &stubFetcher{
		quotes: map[string]quotes.Quote{
			"TSLA": {Price: 31303, Currency: models.CurrencyUSD, AsOf: time.Now()},
		},
	}
	e := setupWithFetcher(t, fetcher)
	user := e.token(t, "trader", models.RoleUser)
	e.seedInstrumentIn(t, "TSLA", "NASDAQ", models.CurrencyUSD)

	report, _ := e.refresh(t, user)
	if report.Updated != 1 || report.Fresh != 0 {
		t.Errorf("updated=%d fresh=%d, want 1/0", report.Updated, report.Fresh)
	}
}

// One bad symbol must not stop the rest of the book from updating, and the
// provider's own wording has to survive all the way to the caller.
func TestRefreshQuotesReportsPerSymbolOutcomes(t *testing.T) {
	marketTime := time.Date(2026, 7, 24, 13, 0, 0, 0, time.UTC)
	fetcher := &stubFetcher{
		quotes: map[string]quotes.Quote{
			"TSLA":    {Price: 31303, Currency: models.CurrencyUSD, AsOf: marketTime},
			"2330.TW": {Price: 235000, Currency: models.CurrencyTWD, AsOf: marketTime},
		},
		fail: map[string]error{
			"BOGUS": errors.New("no quote available: No data found, symbol may be delisted"),
		},
	}
	e := setupWithFetcher(t, fetcher)
	admin := e.token(t, "admin", models.RoleAdmin)

	e.seedInstrumentIn(t, "TSLA", "NASDAQ", models.CurrencyUSD)
	e.seedInstrumentIn(t, "2330", "TWSE", models.CurrencyTWD)
	e.seedInstrumentIn(t, "BOGUS", "NASDAQ", models.CurrencyUSD)
	// An instrument on a market the provider cannot address is skipped, not failed.
	e.seedInstrumentIn(t, "PRIVATE", "OTHER", models.CurrencyTWD)

	body, code := e.refresh(t, admin)
	if code != http.StatusOK {
		t.Fatalf("got %d, want 200", code)
	}
	if body.Updated != 2 || body.Failed != 1 {
		t.Errorf("updated=%d failed=%d, want 2/1", body.Updated, body.Failed)
	}

	byStatus := map[string]string{}
	for _, r := range body.Results {
		byStatus[r.Symbol] = r.Status
		if r.Symbol == "BOGUS" {
			// The provider's explanation is the only actionable diagnostic, so
			// it must not be flattened into a generic message.
			if !strings.Contains(r.Error, "may be delisted") {
				t.Errorf("BOGUS error = %q, should carry the provider's wording", r.Error)
			}
		}
		if r.Symbol == "PRIVATE" && !strings.Contains(r.Error, "OTHER") {
			t.Errorf("PRIVATE error = %q, should name the unsupported market", r.Error)
		}
	}
	for symbol, want := range map[string]string{
		"TSLA": "updated", "2330": "updated", "BOGUS": "failed", "PRIVATE": "skipped",
	} {
		if byStatus[symbol] != want {
			t.Errorf("%s status = %q, want %q", symbol, byStatus[symbol], want)
		}
	}

	// The stored quote must carry the market's timestamp, not the moment of the
	// fetch, so staleness shown to a user reflects the price's own age.
	rec := e.do(t, http.MethodGet, "/api/v1/instruments?q=TSLA", nil, admin)
	var items []models.Instrument
	decodeData(t, rec, &items)
	if len(items) != 1 || items[0].LastPrice == nil || *items[0].LastPrice != 31303 {
		t.Fatalf("TSLA quote not stored: %+v", items)
	}
	if items[0].PriceUpdatedAt == nil || !items[0].PriceUpdatedAt.Equal(marketTime) {
		t.Errorf("price_updated_at = %v, want the market time %v", items[0].PriceUpdatedAt, marketTime)
	}
}

// A failed fetch must leave the previous quote and its timestamp untouched —
// never zeroed, and never restamped as if it were fresh.
func TestFailedRefreshLeavesThePreviousQuoteAlone(t *testing.T) {
	fetcher := &stubFetcher{fail: map[string]error{"TSLA": errors.New("upstream is down")}}
	e := setupWithFetcher(t, fetcher)
	admin := e.token(t, "admin", models.RoleAdmin)

	inst := e.seedInstrumentIn(t, "TSLA", "NASDAQ", models.CurrencyUSD)
	price := int64(30000)
	// Stamp the seeded quote as old, or the freshness guard would skip it and
	// the failing fetch under test would never be attempted.
	stale := time.Now().Add(-24 * time.Hour)
	before, err := e.s.UpdateInstrumentPrice(inst.ID,
		db.QuoteUpdate{Price: &price, AsOf: &stale, CheckedAt: &stale})
	if err != nil {
		t.Fatalf("seed price: %v", err)
	}

	body, _ := e.refresh(t, admin)
	if body.Failed != 1 {
		t.Fatalf("failed=%d, want 1", body.Failed)
	}

	after, err := e.s.GetInstrument(inst.ID)
	if err != nil {
		t.Fatalf("GetInstrument: %v", err)
	}
	if after.LastPrice == nil || *after.LastPrice != 30000 {
		t.Errorf("last_price = %v, want the previous 30000", after.LastPrice)
	}
	if !after.PriceUpdatedAt.Equal(*before.PriceUpdatedAt) {
		t.Error("price_updated_at was restamped despite the failure, hiding the staleness")
	}
}

// A provider that quotes an instrument in a different currency than the book
// records is a data problem, not a price: adopting it would reinterpret every
// cost basis already stored against that instrument.
func TestRefreshRefusesACurrencyMismatch(t *testing.T) {
	fetcher := &stubFetcher{
		quotes: map[string]quotes.Quote{
			"TSLA": {Price: 31303, Currency: models.CurrencyUSD, AsOf: time.Now()},
		},
	}
	e := setupWithFetcher(t, fetcher)
	admin := e.token(t, "admin", models.RoleAdmin)

	// Recorded in TWD by mistake; the provider says USD.
	inst := e.seedInstrumentIn(t, "TSLA", "NASDAQ", models.CurrencyTWD)

	body, _ := e.refresh(t, admin)
	if body.Failed != 1 {
		t.Fatalf("failed=%d, want 1", body.Failed)
	}
	if !strings.Contains(body.Results[0].Error, "TWD") ||
		!strings.Contains(body.Results[0].Error, "USD") {
		t.Errorf("error %q should name both currencies", body.Results[0].Error)
	}

	after, err := e.s.GetInstrument(inst.ID)
	if err != nil {
		t.Fatalf("GetInstrument: %v", err)
	}
	if after.LastPrice != nil {
		t.Errorf("a mismatched quote was stored anyway: %v", *after.LastPrice)
	}
	if after.Currency != models.CurrencyTWD {
		t.Errorf("currency was overwritten to %q", after.Currency)
	}
}

// The name and currency on file come from the provider, not from whatever the
// caller claimed — the authority on what an instrument is called and what it
// trades in is the source that prices it.
func TestCreateTakesNameAndCurrencyFromTheProvider(t *testing.T) {
	e := setupWithFetcher(t, &stubFetcher{quotes: map[string]quotes.Quote{
		"2330.TW": {
			Price: 235000, Currency: models.CurrencyTWD, AsOf: time.Now(),
			Name: "Taiwan Semiconductor Manufacturing Company Limited", Exchange: "TAI",
		},
	}})
	admin := e.token(t, "admin", models.RoleAdmin)

	rec := e.do(t, http.MethodPost, "/api/v1/instruments", map[string]any{
		"symbol": "2330", "market": "TWSE",
		// These are ignored: the provider decides.
		"name": "WRONG", "currency": "USD",
	}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	var created models.Instrument
	decodeData(t, rec, &created)

	if created.Name != "Taiwan Semiconductor Manufacturing Company Limited" {
		t.Errorf("name = %q, want the provider's", created.Name)
	}
	if created.Currency != models.CurrencyTWD {
		t.Errorf("currency = %q, want the provider's TWD", created.Currency)
	}
	// A new instrument arrives already priced, so nothing has to be entered by
	// hand and its holdings can be valued immediately.
	if created.LastPrice == nil || *created.LastPrice != 235000 {
		t.Errorf("last_price = %v, want 235000 fetched at creation", created.LastPrice)
	}
	if created.PriceUpdatedAt == nil || created.QuoteCheckedAt == nil {
		t.Error("both quote timestamps should be stamped at creation")
	}
}

// An instrument the provider cannot price cannot be created. This is what makes
// an unquotable instrument unrepresentable rather than merely discouraged.
func TestCreateRefusesAnInstrumentTheProviderDoesNotKnow(t *testing.T) {
	e := setupWithFetcher(t, &stubFetcher{}) // knows nothing
	admin := e.token(t, "admin", models.RoleAdmin)

	rec := e.do(t, http.MethodPost, "/api/v1/instruments",
		map[string]any{"symbol": "NOSUCH", "market": "NASDAQ"}, admin)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "delisted") {
		t.Errorf("body %s should carry the provider's wording", rec.Body.String())
	}

	// And nothing was stored.
	list := e.do(t, http.MethodGet, "/api/v1/instruments", nil, admin)
	var items []models.Instrument
	decodeData(t, list, &items)
	if len(items) != 0 {
		t.Errorf("%d instruments stored despite the failure", len(items))
	}
}

// A market with no price feed cannot be tracked at all now, so it is refused up
// front rather than creating something that can never be valued.
func TestCreateRefusesAMarketWithNoPriceFeed(t *testing.T) {
	e := setupWithFetcher(t, &stubFetcher{permissive: true})
	admin := e.token(t, "admin", models.RoleAdmin)

	rec := e.do(t, http.MethodPost, "/api/v1/instruments",
		map[string]any{"symbol": "PRIVATE", "market": "OTHER"}, admin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

// A symbol paired with the wrong market can still resolve to some other
// exchange's instrument; adopting that would file a foreign listing locally.
func TestCreateRefusesAListingFromAnotherExchange(t *testing.T) {
	e := setupWithFetcher(t, &stubFetcher{quotes: map[string]quotes.Quote{
		// The caller asks for NASDAQ, but this ticker is a Frankfurt listing.
		"TL0": {Price: 30000, Currency: models.CurrencyUSD, AsOf: time.Now(), Name: "Tesla", Exchange: "FRA"},
	}})
	admin := e.token(t, "admin", models.RoleAdmin)

	rec := e.do(t, http.MethodPost, "/api/v1/instruments",
		map[string]any{"symbol": "TL0", "market": "NASDAQ"}, admin)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "FRA") {
		t.Errorf("body %s should name the exchange actually reached", rec.Body.String())
	}
}

// Most US ETFs list on NYSE Arca, Cboe or NYSE American rather than on NYSE or
// Nasdaq proper. Enumerating venues kept every one of them — VOO, SPY, VTI —
// out of the system, so a US listing is now accepted on whatever venue carries
// it: the ticker is bare either way, which is all pricing depends on.
func TestCreateAcceptsAUSListingFromAnyVenue(t *testing.T) {
	e := setupWithFetcher(t, &stubFetcher{quotes: map[string]quotes.Quote{
		"VOO": {Price: 67914, Currency: models.CurrencyUSD, AsOf: time.Now(),
			Name: "Vanguard S&P 500 ETF", Exchange: "PCX", Type: "ETF"},
	}})
	admin := e.token(t, "admin", models.RoleAdmin)

	rec := e.do(t, http.MethodPost, "/api/v1/instruments",
		map[string]any{"symbol": "VOO", "market": "NYSE"}, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d, want 201 (body: %s)", rec.Code, rec.Body.String())
	}
	var created models.Instrument
	decodeData(t, rec, &created)
	if created.Symbol != "VOO" || created.Market != "NYSE" || created.Currency != models.CurrencyUSD {
		t.Errorf("created %+v", created)
	}
	if created.LastPrice == nil || *created.LastPrice != 67914 {
		t.Errorf("price %v, want it fetched at creation", created.LastPrice)
	}
}

// An index prices perfectly well and cannot be owned. That used to be refused
// only because indices trade on venues the market mapping did not list; with US
// venues no longer enumerated, holdability is asked about directly.
func TestCreateRefusesSomethingThatCannotBeHeld(t *testing.T) {
	e := setupWithFetcher(t, &stubFetcher{quotes: map[string]quotes.Quote{
		"SPX": {Price: 600000, Currency: models.CurrencyUSD, AsOf: time.Now(),
			Name: "S&P 500", Exchange: "SNP", Type: "INDEX"},
	}})
	admin := e.token(t, "admin", models.RoleAdmin)

	rec := e.do(t, http.MethodPost, "/api/v1/instruments",
		map[string]any{"symbol": "SPX", "market": "NYSE"}, admin)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "index") {
		t.Errorf("body %s should say what the provider called it", rec.Body.String())
	}
}

// The currency is the backstop for a foreign listing the provider happens to
// name without a suffix: a bare ticker reads as a US one by design, so what it
// trades in is the thing left that can contradict that.
func TestCreateRefusesAListingInTheWrongCurrency(t *testing.T) {
	// The exchange agrees with the market asked for, so the cross-check passes
	// this through and the currency is the only thing left to catch it.
	e := setupWithFetcher(t, &stubFetcher{quotes: map[string]quotes.Quote{
		"TSM": {Price: 235000, Currency: models.CurrencyTWD, AsOf: time.Now(),
			Name: "TSMC", Exchange: "NYQ", Type: "EQUITY"},
	}})
	admin := e.token(t, "admin", models.RoleAdmin)

	rec := e.do(t, http.MethodPost, "/api/v1/instruments",
		map[string]any{"symbol": "TSM", "market": "NYSE"}, admin)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "TWD") {
		t.Errorf("body %s should name the currency it was quoted in", rec.Body.String())
	}
}

// Only the display name is editable. Symbol and market are identity and decide
// how the instrument is priced; currency belongs to the provider and is baked
// into every cost basis recorded since.
func TestUpdateOnlyRenames(t *testing.T) {
	e := setupWithFetcher(t, &stubFetcher{permissive: true})
	admin := e.token(t, "admin", models.RoleAdmin)
	inst := e.seedInstrumentIn(t, "2330", "TWSE", models.CurrencyTWD)

	rec := e.do(t, http.MethodPut, "/api/v1/instruments/"+inst.ID, map[string]any{
		"name": "台積電",
		// Ignored: not part of the rename contract.
		"symbol": "9999", "market": "NASDAQ", "currency": "USD",
	}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var renamed models.Instrument
	decodeData(t, rec, &renamed)

	if renamed.Name != "台積電" {
		t.Errorf("name = %q, want the new one", renamed.Name)
	}
	if renamed.Symbol != "2330" || renamed.Market != "TWSE" || renamed.Currency != models.CurrencyTWD {
		t.Errorf("identity changed: %+v", renamed)
	}

	// A blank name is refused rather than wiping the label.
	rec = e.do(t, http.MethodPut, "/api/v1/instruments/"+inst.ID,
		map[string]any{"name": "   "}, admin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("blank name: got %d, want 400", rec.Code)
	}
}

// Holdings in different currencies must never be added together — there is no
// FX rate in this system, so a single grand total would be meaningless.
func TestSummaryIsReportedPerCurrency(t *testing.T) {
	e := setup(t)
	user := e.token(t, "trader", models.RoleUser)
	past := time.Now().Add(-time.Hour)

	twdPrice, usdPrice := int64(235000), int64(31303)
	tw := e.seedInstrumentIn(t, "2330", "TWSE", models.CurrencyTWD)
	us := e.seedInstrumentIn(t, "TSLA", "NASDAQ", models.CurrencyUSD)
	if _, err := e.s.UpdateInstrumentPrice(tw.ID, db.QuoteUpdate{Price: &twdPrice}); err != nil {
		t.Fatalf("price: %v", err)
	}
	if _, err := e.s.UpdateInstrumentPrice(us.ID, db.QuoteUpdate{Price: &usdPrice}); err != nil {
		t.Fatalf("price: %v", err)
	}

	e.do(t, http.MethodPost, "/api/v1/transactions", tradePayload(tw.ID, models.SideBuy, 1000, 200000, past), user)
	e.do(t, http.MethodPost, "/api/v1/transactions", tradePayload(us.ID, models.SideBuy, 10, 30000, past), user)

	rec := e.do(t, http.MethodGet, "/api/v1/positions/summary", nil, user)
	var summaries []db.CurrencySummary
	decodeData(t, rec, &summaries)
	if len(summaries) != 2 {
		t.Fatalf("got %d summaries, want one per currency", len(summaries))
	}

	// Ordered by currency for a stable response: TWD then USD.
	twd, usd := summaries[0], summaries[1]
	if twd.Currency != models.CurrencyTWD || usd.Currency != models.CurrencyUSD {
		t.Fatalf("got currencies %q/%q, want TWD/USD", twd.Currency, usd.Currency)
	}
	if twd.TotalCostBasis != 200_000_000 {
		t.Errorf("TWD cost basis = %d, want 200000000", twd.TotalCostBasis)
	}
	if usd.TotalCostBasis != 300_000 {
		t.Errorf("USD cost basis = %d, want 300000", usd.TotalCostBasis)
	}
	// The identity each summary promises must hold within its own currency.
	for _, s := range summaries {
		if s.TotalUnrealizedPL != s.TotalMarketValue-s.PricedCostBasis {
			t.Errorf("%s: unrealized %d != market %d - priced cost %d",
				s.Currency, s.TotalUnrealizedPL, s.TotalMarketValue, s.PricedCostBasis)
		}
	}
}

// seedInstrumentIn inserts an instrument on a given market and currency.
func (e *testEnv) seedInstrumentIn(t *testing.T, symbol, market string, currency models.Currency) models.Instrument {
	t.Helper()
	item, err := e.s.CreateInstrument(models.Instrument{
		ID: uuid.NewString(), Symbol: symbol, Name: symbol + " Corp",
		Market: market, Currency: currency,
	})
	if err != nil {
		t.Fatalf("CreateInstrument: %v", err)
	}
	return item
}

// Pasting a whole provider ticker into the symbol field is refused at entry,
// because the exchange suffix comes from the market: "2330.TW" filed under TWSE
// would be looked up as "2330.TW.TW" and simply never find a quote.
func TestSymbolRejectsAPastedExchangeSuffix(t *testing.T) {
	e := setup(t)
	admin := e.token(t, "admin", models.RoleAdmin)

	for _, symbol := range []string{"2330.TW", "2330.tw", "6488.TWO"} {
		rec := e.do(t, http.MethodPost, "/api/v1/instruments", map[string]any{
			"symbol": symbol, "market": "TWSE",
		}, admin)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400 (body: %s)", symbol, rec.Code, rec.Body.String())
			continue
		}
		// The message must name the symbol to use, not just say no.
		if !strings.Contains(rec.Body.String(), "2330") && !strings.Contains(rec.Body.String(), "6488") {
			t.Errorf("%s: error should suggest the bare symbol (body: %s)", symbol, rec.Body.String())
		}
	}

	// The bare symbol is accepted, and US symbols with a hyphen are unaffected.
	for _, tc := range []struct{ symbol, market string }{{"2330", "TWSE"}, {"BRK-B", "NASDAQ"}} {
		rec := e.do(t, http.MethodPost, "/api/v1/instruments", map[string]any{
			"symbol": tc.symbol, "market": tc.market,
		}, admin)
		if rec.Code != http.StatusCreated {
			t.Errorf("%s: got %d, want 201 (body: %s)", tc.symbol, rec.Code, rec.Body.String())
		}
	}
}

// searchCandidates fetches the search endpoint and decodes its payload.
func (e *testEnv) search(t *testing.T, token, query string) ([]struct {
	Symbol   string `json:"symbol"`
	Name     string `json:"name"`
	Market   string `json:"market"`
	Currency string `json:"currency"`
	Ticker   string `json:"ticker"`
	Exists   bool   `json:"exists"`
}, int) {
	t.Helper()
	rec := e.do(t, http.MethodGet, "/api/v1/instruments/search?q="+query, nil, token)
	var out []struct {
		Symbol   string `json:"symbol"`
		Name     string `json:"name"`
		Market   string `json:"market"`
		Currency string `json:"currency"`
		Ticker   string `json:"ticker"`
		Exists   bool   `json:"exists"`
	}
	if rec.Code == http.StatusOK {
		decodeData(t, rec, &out)
	}
	return out, rec.Code
}

// Search feeds the add flow, so it is open to whoever may add: a plain user
// recording a trade needs to find the listing themselves.
func TestSearchNeedsAuthButNotAdmin(t *testing.T) {
	e := setupWithFetcher(t, &stubFetcher{})

	if _, code := e.search(t, "", "tesla"); code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", code)
	}
	user := e.token(t, "trader", models.RoleUser)
	if _, code := e.search(t, user, "tesla"); code != http.StatusOK {
		t.Errorf("user token: got %d, want 200", code)
	}
}

// A candidate carries everything an add needs, so choosing one types nothing —
// and a symbol taken from the provider's own answer is quotable by construction.
func TestSearchReturnsReadyToAddCandidates(t *testing.T) {
	fetcher := &stubFetcher{permissive: true, results: []quotes.SearchResult{
		{Symbol: "TSLA", Name: "Tesla, Inc.", Market: "NASDAQ", Currency: models.CurrencyUSD, Ticker: "TSLA"},
		{Symbol: "2330", Name: "TSMC", Market: "TWSE", Currency: models.CurrencyTWD, Ticker: "2330.TW"},
	}}
	e := setupWithFetcher(t, fetcher)
	admin := e.token(t, "admin", models.RoleAdmin)

	// One of them is already in the master data.
	e.seedInstrumentIn(t, "2330", "TWSE", models.CurrencyTWD)

	got, code := e.search(t, admin, "tesla")
	if code != http.StatusOK {
		t.Fatalf("got %d, want 200", code)
	}
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2", len(got))
	}
	if got[0].Symbol != "TSLA" || got[0].Market != "NASDAQ" || got[0].Currency != "USD" {
		t.Errorf("TSLA candidate = %+v", got[0])
	}
	// Existing entries are flagged, so a duplicate is visible before it is
	// attempted rather than arriving as a 409 afterwards.
	if got[0].Exists {
		t.Error("TSLA should not be marked as existing")
	}
	if !got[1].Exists {
		t.Error("2330 is already in the master data and should be flagged")
	}

	// A candidate must be addable exactly as returned.
	rec := e.do(t, http.MethodPost, "/api/v1/instruments", map[string]any{
		"symbol": got[0].Symbol, "market": got[0].Market,
	}, admin)
	if rec.Code != http.StatusCreated {
		t.Errorf("adding a candidate verbatim: got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestSearchRejectsABlankQuery(t *testing.T) {
	e := setupWithFetcher(t, &stubFetcher{})
	admin := e.token(t, "admin", models.RoleAdmin)

	if _, code := e.search(t, admin, "%20%20"); code != http.StatusBadRequest {
		t.Errorf("got %d, want 400", code)
	}
}

// A provider outage is reported as such, with its own wording, rather than as
// an empty result set that would read as "no such company".
func TestSearchSurfacesAProviderFailure(t *testing.T) {
	e := setupWithFetcher(t, &stubFetcher{searchErr: errors.New("quote provider returned 429 Too Many Requests")})
	admin := e.token(t, "admin", models.RoleAdmin)

	rec := e.do(t, http.MethodGet, "/api/v1/instruments/search?q=tesla", nil, admin)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("got %d, want 502 (body: %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "429") {
		t.Errorf("body %s should carry the provider's wording", rec.Body.String())
	}
}
