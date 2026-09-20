package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"stockbook/internal/models"
	"stockbook/internal/quotes"
)

// syncReport mirrors the sync-history response envelope.
type syncReport struct {
	Synced  int `json:"synced"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
	Results []struct {
		Symbol   string `json:"symbol"`
		Ticker   string `json:"ticker"`
		Status   string `json:"status"`
		From     string `json:"from"`
		To       string `json:"to"`
		Added    int    `json:"added"`
		Sessions int64  `json:"sessions"`
		Error    string `json:"error"`
	} `json:"results"`
}

func decodeSync(t *testing.T, body []byte) syncReport {
	t.Helper()
	var envelope struct {
		Data syncReport `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode: %v (body: %s)", err, body)
	}
	return envelope.Data
}

// series builds a run of consecutive daily closes starting at from.
func series(from string, closes ...int64) quotes.History {
	start, _ := time.Parse(time.DateOnly, from)
	out := quotes.History{Currency: models.CurrencyTWD}
	for i, close := range closes {
		out.Closes = append(out.Closes, quotes.DailyClose{
			Date:  start.AddDate(0, 0, i).Format(time.DateOnly),
			Close: close,
		})
	}
	return out
}

func TestSyncHistoryStoresASeries(t *testing.T) {
	fetcher := &stubFetcher{
		permissive: true,
		history: map[string]quotes.History{
			"2330.TW": series("2025-07-01", 100000, 101050, 99500),
		},
	}
	e := setupWithFetcher(t, fetcher)
	token := e.token(t, "syncer", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	buy := tradePayload(inst.ID, models.SideBuy, 100, 100000, tradedOn(2025, time.July, 1))
	if rec := e.do(t, http.MethodPost, "/api/v1/transactions", buy, token); rec.Code != http.StatusCreated {
		t.Fatalf("buy: got %d (body: %s)", rec.Code, rec.Body.String())
	}

	rec := e.do(t, http.MethodPost, "/api/v1/instruments/sync-history", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	report := decodeSync(t, rec.Body.Bytes())

	if report.Synced != 1 || report.Failed != 0 {
		t.Fatalf("unexpected counts: %+v", report)
	}
	if len(report.Results) != 1 {
		t.Fatalf("got %d results, want 1", len(report.Results))
	}
	got := report.Results[0]
	if got.Added != 3 || got.Sessions != 3 {
		t.Errorf("added %d of %d sessions, want 3 of 3", got.Added, got.Sessions)
	}
	if got.Ticker != "2330.TW" {
		t.Errorf("ticker %q, want 2330.TW", got.Ticker)
	}
	// History is only worth having from the day the instrument was first
	// traded; earlier prices value nothing.
	if got.From != "2025-07-01" {
		t.Errorf("fetched from %q, want the first trade date 2025-07-01", got.From)
	}
}

// An instrument nobody has traded and nobody plans to buy has nothing to plot,
// so it is not fetched at all — that is what keeps a run proportional to the
// book rather than to the master data.
func TestSyncHistorySkipsUntradedInstruments(t *testing.T) {
	fetcher := &stubFetcher{permissive: true}
	e := setupWithFetcher(t, fetcher)
	token := e.token(t, "syncer", models.RoleUser)
	e.seedInstrument(t, "2330", nil)

	rec := e.do(t, http.MethodPost, "/api/v1/instruments/sync-history", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	report := decodeSync(t, rec.Body.Bytes())

	if report.Skipped != 1 || report.Synced != 0 {
		t.Errorf("unexpected counts: %+v", report)
	}
	if len(report.Results) != 0 {
		t.Errorf("an untraded instrument should not fill the results: %+v", report.Results)
	}
	if len(fetcher.historyCalls) != 0 {
		t.Errorf("the provider was called for an untraded instrument: %+v", fetcher.historyCalls)
	}
}

// The bug a benchmark makes easy to hit: an instrument already holding history
// that starts *after* what now needs valuing. The incremental top-up reaches
// back from the last stored session, so on its own it extends a series forward
// and never fills a gap at the front — pressing Sync did nothing at all while
// the chart went on saying the prices were missing.
//
// The gap has to be wider than historyOverlap for this to bite, which is the
// whole point: a few days at the front are swept up by the overlap already, and
// the case that matters is the six weeks between a book opening and the
// benchmark being bought.
func TestSyncHistoryRefetchesASeriesThatStartsTooLate(t *testing.T) {
	fetcher := &stubFetcher{
		permissive: true,
		history: map[string]quotes.History{
			"2330.TW": series("2025-07-01", 100000, 101050, 99500),
			"2317.TW": series("2025-08-15", 99000, 98000),
		},
	}
	e := setupWithFetcher(t, fetcher)
	token := e.token(t, "syncer", models.RoleUser)
	early := e.seedInstrument(t, "2330", nil)
	late := e.seedInstrument(t, "2317", nil)

	// The book opens in 2330 on 1 July and only buys 2317 six weeks later, so
	// 2317's own history begins long after the book's first session.
	for _, tx := range []struct {
		id string
		on time.Time
	}{
		{early.ID, tradedOn(2025, time.July, 1)},
		{late.ID, tradedOn(2025, time.August, 15)},
	} {
		buy := tradePayload(tx.id, models.SideBuy, 100, 100000, tx.on)
		if rec := e.do(t, http.MethodPost, "/api/v1/transactions", buy, token); rec.Code != http.StatusCreated {
			t.Fatalf("buy: got %d (body: %s)", rec.Code, rec.Body.String())
		}
	}
	if rec := e.do(t, http.MethodPost, "/api/v1/instruments/sync-history", nil, token); rec.Code != http.StatusOK {
		t.Fatalf("first sync: got %d", rec.Code)
	}

	// Now 2317 becomes the benchmark, which needs its prices back to the book's
	// first trade — six weeks before 2317 was ever bought.
	bench := map[string]any{
		"benchmarks": []map[string]any{{"currency": "TWD", "instrument_id": late.ID}},
	}
	if rec := e.do(t, http.MethodPut, "/api/v1/settings/benchmarks", bench, token); rec.Code != http.StatusOK {
		t.Fatalf("set benchmark: got %d (body: %s)", rec.Code, rec.Body.String())
	}

	rec := e.do(t, http.MethodPost, "/api/v1/instruments/sync-history", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("second sync: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	report := decodeSync(t, rec.Body.Bytes())

	found := false
	for _, r := range report.Results {
		if r.Symbol != "2317" {
			continue
		}
		found = true
		if r.From != "2025-07-01" {
			t.Errorf("refetched from %q, want the book's first trade 2025-07-01 — topping up "+
				"the end never fills a gap at the front", r.From)
		}
	}
	if !found {
		t.Fatalf("the benchmark was not synced at all: %+v", report.Results)
	}
}

// A savings plan is the other thing that needs history, and the ordinary way to
// start one is before the first purchase — so an instrument with a plan and no
// trades at all must still be fetched, from the plan's start date. Skipping it
// left every instalment prompt with no close to estimate the shares from, which
// is the whole reason those prices are wanted.
func TestSyncHistoryFetchesAPlannedInstrumentThatWasNeverTraded(t *testing.T) {
	fetcher := &stubFetcher{
		permissive: true,
		history: map[string]quotes.History{
			"2330.TW": series("2025-07-15", 100000, 101050),
		},
	}
	e := setupWithFetcher(t, fetcher)
	token := e.token(t, "saver", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	plan := map[string]any{
		"instrument_id": inst.ID,
		"days_of_month": "5,15,25",
		"amount":        300000,
		"started_on":    "2025-07-15",
	}
	if rec := e.do(t, http.MethodPost, "/api/v1/plans", plan, token); rec.Code != http.StatusCreated {
		t.Fatalf("create plan: got %d (body: %s)", rec.Code, rec.Body.String())
	}

	rec := e.do(t, http.MethodPost, "/api/v1/instruments/sync-history", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	report := decodeSync(t, rec.Body.Bytes())

	if report.Synced != 1 || report.Skipped != 0 {
		t.Fatalf("a planned instrument was not synced: %+v", report)
	}
	if got := report.Results[0].From; got != "2025-07-15" {
		t.Errorf("fetched from %q, want the plan's start 2025-07-15", got)
	}
}

// A second run tops up from just before the last session held rather than
// downloading the whole history again.
func TestSyncHistoryIsIncremental(t *testing.T) {
	fetcher := &stubFetcher{
		permissive: true,
		history: map[string]quotes.History{
			"2330.TW": series("2025-07-01", 100000, 101050, 99500),
		},
	}
	e := setupWithFetcher(t, fetcher)
	token := e.token(t, "syncer", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	buy := tradePayload(inst.ID, models.SideBuy, 100, 100000, tradedOn(2025, time.July, 1))
	e.do(t, http.MethodPost, "/api/v1/transactions", buy, token)

	if rec := e.do(t, http.MethodPost, "/api/v1/instruments/sync-history", nil, token); rec.Code != http.StatusOK {
		t.Fatalf("first sync: got %d", rec.Code)
	}
	rec := e.do(t, http.MethodPost, "/api/v1/instruments/sync-history", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("second sync: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	report := decodeSync(t, rec.Body.Bytes())

	// Re-storing the same three sessions must not multiply them: the write is
	// keyed on the session, so a refetch updates rather than accumulates.
	if report.Results[0].Sessions != 3 {
		t.Errorf("holds %d sessions after two runs, want 3 — a refetch duplicated rows",
			report.Results[0].Sessions)
	}
	if len(fetcher.historyCalls) != 2 {
		t.Fatalf("got %d provider calls, want 2", len(fetcher.historyCalls))
	}
	// The second window starts near the last stored session, not at the first
	// trade — otherwise every daily run would re-download years.
	if second := fetcher.historyCalls[1].from; second <= "2025-06-25" {
		t.Errorf("second run fetched from %q, which is the whole history again", second)
	}
}

// A currency the instrument is not recorded in would reinterpret every cost
// basis behind it, so the series is refused rather than stored.
func TestSyncHistoryRefusesAMismatchedCurrency(t *testing.T) {
	usd := series("2025-07-01", 10000)
	usd.Currency = models.CurrencyUSD
	fetcher := &stubFetcher{
		permissive: true,
		history:    map[string]quotes.History{"2330.TW": usd},
	}
	e := setupWithFetcher(t, fetcher)
	token := e.token(t, "syncer", models.RoleUser)
	inst := e.seedInstrument(t, "2330", nil)

	buy := tradePayload(inst.ID, models.SideBuy, 100, 100000, tradedOn(2025, time.July, 1))
	e.do(t, http.MethodPost, "/api/v1/transactions", buy, token)

	rec := e.do(t, http.MethodPost, "/api/v1/instruments/sync-history", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync: got %d (body: %s)", rec.Code, rec.Body.String())
	}
	report := decodeSync(t, rec.Body.Bytes())

	if report.Failed != 1 || report.Synced != 0 {
		t.Fatalf("unexpected counts: %+v", report)
	}
	if got := report.Results[0]; got.Sessions != 0 {
		t.Errorf("stored %d sessions despite the mismatch, want 0", got.Sessions)
	}
}

// One bad ticker must not stop the rest of the book from syncing.
func TestSyncHistoryReportsAFailureWithoutStoppingTheRun(t *testing.T) {
	fetcher := &stubFetcher{
		permissive: true,
		history: map[string]quotes.History{
			"2330.TW": series("2025-07-01", 100000),
		},
		historyErr: map[string]error{
			"2454.TW": quotes.ErrNoQuote,
		},
	}
	e := setupWithFetcher(t, fetcher)
	token := e.token(t, "syncer", models.RoleUser)
	good := e.seedInstrument(t, "2330", nil)
	bad := e.seedInstrument(t, "2454", nil)

	for _, id := range []string{good.ID, bad.ID} {
		buy := tradePayload(id, models.SideBuy, 100, 100000, tradedOn(2025, time.July, 1))
		e.do(t, http.MethodPost, "/api/v1/transactions", buy, token)
	}

	rec := e.do(t, http.MethodPost, "/api/v1/instruments/sync-history", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync: got %d, want 200 — a partial failure is not a failed run", rec.Code)
	}
	report := decodeSync(t, rec.Body.Bytes())
	if report.Synced != 1 || report.Failed != 1 {
		t.Errorf("unexpected counts: %+v", report)
	}
}

func TestSyncHistoryRequiresAuth(t *testing.T) {
	e := setup(t)
	if rec := e.do(t, http.MethodPost, "/api/v1/instruments/sync-history", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", rec.Code)
	}
}
