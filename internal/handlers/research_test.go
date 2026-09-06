package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"stockbook/internal/models"
	"stockbook/internal/news"
	"stockbook/internal/quotes"
)

// stubCollector stands in for the news aggregator so no test reaches the
// network. build turns the holdings a run was given into the articles the
// providers would have returned, which is how a test names instrument IDs it
// only learns after seeding.
type stubCollector struct {
	build func(held []models.Instrument) news.Report
	calls [][]string // the symbols asked for on each run
	since []time.Time
}

func (s *stubCollector) Collect(_ context.Context, held []models.Instrument, since time.Time) news.Report {
	symbols := make([]string, 0, len(held))
	for _, item := range held {
		symbols = append(symbols, item.Symbol)
	}
	s.calls = append(s.calls, symbols)
	s.since = append(s.since, since)
	if s.build == nil {
		return news.Report{}
	}
	return s.build(held)
}

// article builds one collected headline attributed to the given holdings.
func article(id string, at time.Time, instrumentIDs ...string) news.Article {
	return news.Article{
		ID:            id,
		Source:        news.SourceCnyes,
		Title:         "headline " + id,
		Summary:       "summary",
		URL:           "https://news.cnyes.com/news/id/" + id,
		Publisher:     "cnyes",
		Language:      "zh-TW",
		PublishedAt:   at,
		InstrumentIDs: instrumentIDs,
	}
}

// synced is the source result a working provider returns for some holdings.
func synced(held []models.Instrument) news.SourceResult {
	result := news.SourceResult{Source: news.SourceCnyes, Scope: "stub", Status: "synced"}
	for _, item := range held {
		result.Covered = append(result.Covered, item.ID)
	}
	return result
}

// feedResponse mirrors the news endpoint's payload.
type feedResponse []struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Language    string   `json:"language"`
	PublishedAt string   `json:"published_at"`
	Symbols     []string `json:"symbols"`
}

// syncResponse mirrors the research sync endpoint's payload.
type syncResponse struct {
	Holdings int `json:"holdings"`
	News     struct {
		Collected int    `json:"collected"`
		Fresh     int    `json:"fresh"`
		Error     string `json:"error"`
		Sources   []struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"sources"`
	} `json:"news"`
	Fundamentals struct {
		Synced  int `json:"synced"`
		Failed  int `json:"failed"`
		Fresh   int `json:"fresh"`
		Results []struct {
			Symbol   string `json:"symbol"`
			Status   string `json:"status"`
			Currency string `json:"currency"`
			Added    int    `json:"added"`
			Error    string `json:"error"`
		} `json:"results"`
	} `json:"fundamentals"`
}

// financialsResponse mirrors the fundamentals endpoint's payload.
type financialsResponse []struct {
	AsOfDate   string `json:"as_of_date"`
	PeriodType string `json:"period_type"`
	Currency   string `json:"currency"`
	Revenue    *int64 `json:"revenue"`
	NetIncome  *int64 `json:"net_income"`
	DilutedEPS *int64 `json:"diluted_eps"`
}

// buy records a purchase so the instrument becomes an open holding.
func (e *testEnv) buy(t *testing.T, token, instrumentID string, qty int) {
	t.Helper()
	rec := e.do(t, http.MethodPost, "/api/v1/transactions",
		tradePayload(instrumentID, models.SideBuy, qty, 10000, time.Now().Add(-24*time.Hour)), token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("buy: status %d, body %s", rec.Code, rec.Body.String())
	}
}

func (e *testEnv) syncResearch(t *testing.T, token string) syncResponse {
	t.Helper()
	rec := e.do(t, http.MethodPost, "/api/v1/research/sync", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync: status %d, body %s", rec.Code, rec.Body.String())
	}
	var got syncResponse
	decodeData(t, rec, &got)
	return got
}

func (e *testEnv) feed(t *testing.T, token, query string) feedResponse {
	t.Helper()
	rec := e.do(t, http.MethodGet, "/api/v1/research/news"+query, nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed: status %d, body %s", rec.Code, rec.Body.String())
	}
	var got feedResponse
	decodeData(t, rec, &got)
	return got
}

// The articles are shared objective data, but which of them reaches a reader is
// decided entirely by what that reader holds — including for an admin, who has
// no privileged view of anybody's book.
func TestNewsFeedIsScopedToTheCallersHoldings(t *testing.T) {
	collector := &stubCollector{}
	e := setupWithProviders(t, &stubFetcher{permissive: true}, collector)

	mine := e.token(t, "mine", models.RoleUser)
	theirs := e.token(t, "theirs", models.RoleAdmin)

	held := e.seedInstrument(t, "2330", nil)
	other := e.seedInstrument(t, "2454", nil)
	e.buy(t, mine, held.ID, 100)
	e.buy(t, theirs, other.ID, 100)

	now := time.Now().UTC()
	collector.build = func(h []models.Instrument) news.Report {
		return news.Report{
			Articles: []news.Article{
				article("cnyes:1", now, held.ID),
				article("cnyes:2", now.Add(-time.Hour), other.ID),
			},
			Results: []news.SourceResult{synced(h)},
		}
	}
	e.syncResearch(t, mine)

	got := e.feed(t, mine, "")
	if len(got) != 1 {
		t.Fatalf("got %d articles, want only the one about a held company: %+v", len(got), got)
	}
	if got[0].ID != "cnyes:1" {
		t.Errorf("feed carried %s, want cnyes:1", got[0].ID)
	}

	// The admin holds the other company, and sees that one and nothing else.
	adminFeed := e.feed(t, theirs, "")
	if len(adminFeed) != 1 || adminFeed[0].ID != "cnyes:2" {
		t.Errorf("admin feed = %+v, want only cnyes:2", adminFeed)
	}
}

// An article naming three companies a reader holds is one row on their feed
// carrying three badges, not three rows.
func TestAnArticleNamingSeveralHoldingsIsOneRowWithEveryTicker(t *testing.T) {
	collector := &stubCollector{}
	e := setupWithProviders(t, &stubFetcher{permissive: true}, collector)
	token := e.token(t, "reader", models.RoleUser)

	a := e.seedInstrument(t, "2330", nil)
	b := e.seedInstrument(t, "3374", nil)
	c := e.seedInstrument(t, "6789", nil)
	for _, item := range []models.Instrument{a, b, c} {
		e.buy(t, token, item.ID, 100)
	}

	collector.build = func(h []models.Instrument) news.Report {
		return news.Report{
			Articles: []news.Article{article("cnyes:9", time.Now().UTC(), a.ID, b.ID, c.ID)},
			Results:  []news.SourceResult{synced(h)},
		}
	}
	e.syncResearch(t, token)

	got := e.feed(t, token, "")
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(got), got)
	}
	if len(got[0].Symbols) != 3 {
		t.Errorf("symbols = %v, want all three tickers", got[0].Symbols)
	}
}

// The same article arrives twice when it is filed under two holdings' feeds.
// Storing it once with both attributions is what keeps the count honest and the
// second holding's badge on the row.
func TestTheSameArticleFromTwoSourcesIsStoredOnceWithBothHoldings(t *testing.T) {
	collector := &stubCollector{}
	e := setupWithProviders(t, &stubFetcher{permissive: true}, collector)
	token := e.token(t, "reader", models.RoleUser)

	a := e.seedInstrument(t, "2330", nil)
	b := e.seedInstrument(t, "2454", nil)
	e.buy(t, token, a.ID, 100)
	e.buy(t, token, b.ID, 100)

	at := time.Now().UTC()
	collector.build = func(h []models.Instrument) news.Report {
		// One identifier, delivered twice — once per ticker's feed.
		return news.Report{
			Articles: []news.Article{
				article("yahoo:same", at, a.ID),
				article("yahoo:same", at, b.ID),
			},
			Results: []news.SourceResult{synced(h)},
		}
	}
	report := e.syncResearch(t, token)
	if report.News.Collected != 1 {
		t.Errorf("collected = %d, want 1 distinct article", report.News.Collected)
	}

	got := e.feed(t, token, "")
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(got), got)
	}
	if len(got[0].Symbols) != 2 {
		t.Errorf("symbols = %v, want both holdings attributed", got[0].Symbols)
	}
}

// News about a company sold last year answers nothing a reader can act on, and
// "If you had never sold" is where that question already lives.
func TestAClosedHoldingDropsOutOfTheFeed(t *testing.T) {
	collector := &stubCollector{}
	e := setupWithProviders(t, &stubFetcher{permissive: true}, collector)
	token := e.token(t, "reader", models.RoleUser)

	item := e.seedInstrument(t, "2330", nil)
	e.buy(t, token, item.ID, 100)

	collector.build = func(h []models.Instrument) news.Report {
		return news.Report{
			Articles: []news.Article{article("cnyes:5", time.Now().UTC(), item.ID)},
			Results:  []news.SourceResult{synced(h)},
		}
	}
	e.syncResearch(t, token)
	if len(e.feed(t, token, "")) != 1 {
		t.Fatal("the article did not reach the feed while the holding was open")
	}

	rec := e.do(t, http.MethodPost, "/api/v1/transactions",
		tradePayload(item.ID, models.SideSell, 100, 12000, time.Now()), token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("sell: status %d, body %s", rec.Code, rec.Body.String())
	}

	if got := e.feed(t, token, ""); len(got) != 0 {
		t.Errorf("feed still carries %+v after the holding was closed", got)
	}
}

// A second press within the freshness window costs the providers nothing.
func TestSyncSkipsHoldingsCheckedRecently(t *testing.T) {
	collector := &stubCollector{}
	fetcher := &stubFetcher{permissive: true}
	e := setupWithProviders(t, fetcher, collector)
	token := e.token(t, "reader", models.RoleUser)

	item := e.seedInstrument(t, "2330", nil)
	e.buy(t, token, item.ID, 100)

	collector.build = func(h []models.Instrument) news.Report {
		return news.Report{Results: []news.SourceResult{synced(h)}}
	}
	fetcher.fundamentals = map[string]quotes.Fundamentals{
		"2330.TW": {Currency: models.CurrencyTWD, Figures: []quotes.Figure{
			{Metric: models.MetricRevenue, PeriodType: models.PeriodQuarter,
				AsOfDate: "2026-06-30", Value: 100},
		}},
	}

	first := e.syncResearch(t, token)
	if first.News.Fresh != 0 || first.Fundamentals.Synced != 1 {
		t.Fatalf("first run = %+v, want it to do the work", first)
	}

	second := e.syncResearch(t, token)
	if second.News.Fresh != 1 {
		t.Errorf("news fresh = %d, want the holding skipped", second.News.Fresh)
	}
	if second.Fundamentals.Fresh != 1 {
		t.Errorf("fundamentals fresh = %d, want the holding skipped", second.Fundamentals.Fresh)
	}
	if len(collector.calls) != 1 {
		t.Errorf("the news provider was called %d times, want 1", len(collector.calls))
	}
	if len(fetcher.fundamentalsCalls) != 1 {
		t.Errorf("the figures provider was called %d times, want 1", len(fetcher.fundamentalsCalls))
	}
}

// The inverse of the quote refresh's currency guard, and the point of the
// distinction: a quote in the wrong currency is refused because adopting it
// would reinterpret every cost basis behind it, while a reported figure is never
// added to anything and so only needs a label. An ADR reporting in USD against a
// TWD listing is the ordinary case, not an error.
func TestReportedCurrencyIsKeptWhenItDiffersFromTheTradingCurrency(t *testing.T) {
	fetcher := &stubFetcher{permissive: true}
	e := setupWithProviders(t, fetcher, &stubCollector{})
	token := e.token(t, "reader", models.RoleUser)

	item := e.seedInstrument(t, "2330", nil) // seeded on TWSE, so recorded in TWD
	e.buy(t, token, item.ID, 100)

	fetcher.fundamentals = map[string]quotes.Fundamentals{
		"2330.TW": {Currency: models.CurrencyUSD, Figures: []quotes.Figure{
			{Metric: models.MetricRevenue, PeriodType: models.PeriodQuarter,
				AsOfDate: "2026-06-30", Value: 93379200000000},
			{Metric: models.MetricDilutedEPS, PeriodType: models.PeriodQuarter,
				AsOfDate: "2026-06-30", Value: 1536},
		}},
	}

	report := e.syncResearch(t, token)
	if report.Fundamentals.Failed != 0 {
		t.Fatalf("a differing reporting currency was treated as a failure: %+v", report.Fundamentals)
	}
	if got := report.Fundamentals.Results[0].Currency; got != string(models.CurrencyUSD) {
		t.Errorf("reported currency = %q, want USD", got)
	}

	rec := e.do(t, http.MethodGet,
		"/api/v1/research/fundamentals?instrument_id="+item.ID+"&period=quarterly", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("fundamentals: status %d, body %s", rec.Code, rec.Body.String())
	}
	var got financialsResponse
	decodeData(t, rec, &got)

	if len(got) != 1 {
		t.Fatalf("got %d periods, want 1: %+v", len(got), got)
	}
	if got[0].Currency != string(models.CurrencyUSD) {
		t.Errorf("stored currency = %q, want the one the provider reported", got[0].Currency)
	}
	if got[0].Revenue == nil || *got[0].Revenue != 93379200000000 {
		t.Errorf("revenue = %v, want the reported figure", got[0].Revenue)
	}
	// Net income was never reported, and unknown is not zero.
	if got[0].NetIncome != nil {
		t.Errorf("net income = %v, want nil for a metric the provider did not report", *got[0].NetIncome)
	}
}

// One holding's provider failing must not cost the others their figures.
func TestAPartialFundamentalsFailureDoesNotStopTheRun(t *testing.T) {
	fetcher := &stubFetcher{permissive: true}
	e := setupWithProviders(t, fetcher, &stubCollector{})
	token := e.token(t, "reader", models.RoleUser)

	good := e.seedInstrument(t, "2330", nil)
	bad := e.seedInstrument(t, "2454", nil)
	e.buy(t, token, good.ID, 100)
	e.buy(t, token, bad.ID, 100)

	fetcher.fundamentals = map[string]quotes.Fundamentals{
		"2330.TW": {Currency: models.CurrencyTWD, Figures: []quotes.Figure{
			{Metric: models.MetricRevenue, PeriodType: models.PeriodQuarter,
				AsOfDate: "2026-06-30", Value: 100},
		}},
	}
	fetcher.fundamentalsErr = map[string]error{
		"2454.TW": errors.New("No fundamentals data found, symbol may be delisted"),
	}

	report := e.syncResearch(t, token)
	if report.Fundamentals.Synced != 1 || report.Fundamentals.Failed != 1 {
		t.Fatalf("report = %+v, want one synced and one failed", report.Fundamentals)
	}
	for _, result := range report.Fundamentals.Results {
		if result.Symbol == "2454" && result.Error != "No fundamentals data found, symbol may be delisted" {
			t.Errorf("failure dropped the provider's own wording: %q", result.Error)
		}
	}
}

// A collection run is proportional to the caller's own open holdings, not to the
// master data: fetching news for companies nobody owns reaches the providers on
// their behalf for nothing.
func TestSyncOnlyAsksAboutTheCallersOpenHoldings(t *testing.T) {
	collector := &stubCollector{}
	e := setupWithProviders(t, &stubFetcher{permissive: true}, collector)
	token := e.token(t, "reader", models.RoleUser)

	held := e.seedInstrument(t, "2330", nil)
	e.seedInstrument(t, "2454", nil) // in the master data, owned by nobody
	e.buy(t, token, held.ID, 100)

	e.syncResearch(t, token)

	if len(collector.calls) != 1 {
		t.Fatalf("the provider was called %d times, want 1", len(collector.calls))
	}
	asked := collector.calls[0]
	if len(asked) != 1 || asked[0] != "2330" {
		t.Errorf("asked about %v, want only the held company", asked)
	}
}

// A holding whose news source failed is not stamped as checked, so the next
// press retries it immediately rather than suppressing it for the freshness
// window — the rule a failed quote fetch already follows.
func TestAFailedNewsSourceIsRetriedRatherThanSuppressed(t *testing.T) {
	collector := &stubCollector{}
	e := setupWithProviders(t, &stubFetcher{permissive: true}, collector)
	token := e.token(t, "reader", models.RoleUser)

	item := e.seedInstrument(t, "2330", nil)
	e.buy(t, token, item.ID, 100)

	collector.build = func(h []models.Instrument) news.Report {
		result := synced(h)
		result.Status = "failed"
		result.Error = "news provider returned 503 Service Unavailable"
		return news.Report{Results: []news.SourceResult{result}}
	}

	e.syncResearch(t, token)
	second := e.syncResearch(t, token)

	if second.News.Fresh != 0 {
		t.Errorf("news fresh = %d, want the failed holding retried", second.News.Fresh)
	}
	if len(collector.calls) != 2 {
		t.Errorf("the provider was called %d times, want it retried", len(collector.calls))
	}
}

// The feed is filterable to one holding, which is how the page's per-company
// view is served without a second endpoint.
func TestTheFeedCanBeNarrowedToOneHolding(t *testing.T) {
	collector := &stubCollector{}
	e := setupWithProviders(t, &stubFetcher{permissive: true}, collector)
	token := e.token(t, "reader", models.RoleUser)

	a := e.seedInstrument(t, "2330", nil)
	b := e.seedInstrument(t, "2454", nil)
	e.buy(t, token, a.ID, 100)
	e.buy(t, token, b.ID, 100)

	now := time.Now().UTC()
	collector.build = func(h []models.Instrument) news.Report {
		return news.Report{
			Articles: []news.Article{
				article("cnyes:a", now, a.ID),
				article("cnyes:b", now.Add(-time.Hour), b.ID),
			},
			Results: []news.SourceResult{synced(h)},
		}
	}
	e.syncResearch(t, token)

	got := e.feed(t, token, "?instrument_id="+a.ID)
	if len(got) != 1 || got[0].ID != "cnyes:a" {
		t.Errorf("filtered feed = %+v, want only cnyes:a", got)
	}
}

// The feed is read newest first, which is the whole point of a merged timeline.
func TestTheFeedIsNewestFirst(t *testing.T) {
	collector := &stubCollector{}
	e := setupWithProviders(t, &stubFetcher{permissive: true}, collector)
	token := e.token(t, "reader", models.RoleUser)

	item := e.seedInstrument(t, "2330", nil)
	e.buy(t, token, item.ID, 100)

	now := time.Now().UTC()
	collector.build = func(h []models.Instrument) news.Report {
		return news.Report{
			Articles: []news.Article{
				article("cnyes:old", now.Add(-48*time.Hour), item.ID),
				article("cnyes:new", now, item.ID),
			},
			Results: []news.SourceResult{synced(h)},
		}
	}
	e.syncResearch(t, token)

	got := e.feed(t, token, "")
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0].ID != "cnyes:new" {
		t.Errorf("feed led with %s, want the newest article first", got[0].ID)
	}
}

// The endpoints are the caller's own, like every other personal read.
func TestResearchEndpointsRequireAuthentication(t *testing.T) {
	e := setupWithProviders(t, &stubFetcher{permissive: true}, &stubCollector{})

	for _, path := range []string{
		"/api/v1/research/news",
		"/api/v1/research/fundamentals?instrument_id=x",
	} {
		if rec := e.do(t, http.MethodGet, path, nil, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a token: status %d, want 401", path, rec.Code)
		}
	}
	if rec := e.do(t, http.MethodPost, "/api/v1/research/sync", nil, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("sync without a token: status %d, want 401", rec.Code)
	}
}
